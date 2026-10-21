package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Manager is the sandbox.Manager surface the worker side uses.
type Manager interface {
	Get(id string) (*pb.Sandbox, error)
	List() ([]*pb.Sandbox, error)
	Emit(sb *pb.Sandbox)
}

// Registry is the registry surface: the sandbox record (CAS under Update) and
// the daemon-local worker_tokens bucket.
type Registry interface {
	Get(id string) (*pb.Sandbox, error)
	Update(id string, mutate func(*pb.Sandbox) error) (*pb.Sandbox, error)
	PutWorkerToken(sandboxID, token string) error
	GetWorkerToken(sandboxID string) (string, error)
	DeleteWorkerToken(sandboxID string) error
	ListWorkerTokens() (map[string]string, error)
}

// Deps wires a Service.
type Deps struct {
	Manager  Manager
	Registry Registry
	Agents   *agent.Registry
	Hub      *agent.Hub
	CLI      *mcp.CLI
	HookPort string
	HostID   string
	Version  string
	Logf     func(format string, args ...any)
	Clock    func() time.Time
}

// Service is the worker side every daemon runs (research R1/R3): the controller
// record on its sandboxes, the per-worker gateway registration + token, the
// link relay, guarded delivery and the briefing block.
type Service struct {
	mgr      Manager
	reg      Registry
	agents   *agent.Registry
	terms    *terminal.Registry
	hub      *agent.Hub
	gw       *gateway
	hookPort string
	hostID   string
	version  string
	logf     func(string, ...any)
	now      func() time.Time
	links    *linkIndex

	// tokens is the in-memory token → sandbox index the MCP endpoint resolves
	// against (loaded from the registry at start, kept in sync by attach/detach).
	tokMu  sync.RWMutex
	tokens map[string]string

	// servers caches one MCP server per live token (endpoint.go).
	srvMu   sync.Mutex
	servers map[string]*sdk.Server
}

// NewService constructs a Service and loads the token index.
func NewService(d Deps) *Service {
	s := &Service{
		mgr: d.Manager, reg: d.Registry, agents: d.Agents, hub: d.Hub,
		hookPort: d.HookPort, hostID: d.HostID, version: d.Version, logf: d.Logf, now: d.Clock,
		links: newLinkIndex(), tokens: map[string]string{}, servers: map[string]*sdk.Server{},
	}
	if s.logf == nil {
		s.logf = log.Printf
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.gw = &gateway{cli: d.CLI, hookPort: d.HookPort, logf: s.logf}
	if s.reg != nil {
		if all, err := s.reg.ListWorkerTokens(); err == nil {
			for sb, tok := range all {
				s.tokens[tok] = sb
			}
		}
	}
	return s
}

// SetTerminals wires the terminal registry delivery drains through (the gRPC
// server owns it).
func (s *Service) SetTerminals(t *terminal.Registry) { s.terms = t }

// HostID returns this daemon's host id.
func (s *Service) HostID() string { return s.hostID }

// Attach records the controller on a sandbox (compare-and-set, FR-108), mints
// the token, registers + loads the gateway server and writes the briefing
// (FR-107). Idempotent for the same orchestrator: the token is re-minted and the
// registration repeated. Never touches the sandbox's state or session (FR-109).
func (s *Service) Attach(ctx context.Context, req *pb.AttachControllerRequest) (*pb.Sandbox, error) {
	return s.attach(ctx, req, nil)
}

// attach is Attach with an optional launching record: at launch-attach the
// persisted record is still CREATING while the container already exists, so
// the caller's view (container ref, RUNNING) decides whether to load now.
func (s *Service) attach(ctx context.Context, req *pb.AttachControllerRequest, launching *pb.Sandbox) (*pb.Sandbox, error) {
	ctl := req.GetController()
	if req.GetSandboxId() == "" || ctl.GetOrchestratorId() == "" {
		return nil, fmt.Errorf("%w: sandbox id and controller identity are required", ErrInvalid)
	}
	now := timestamppb.New(s.now())
	sb, err := s.reg.Update(req.GetSandboxId(), func(x *pb.Sandbox) error {
		cur := x.GetController()
		if cur != nil && cur.GetOrchestratorId() != ctl.GetOrchestratorId() {
			return &AlreadyControlledError{Name: cur.GetName()}
		}
		attachedAt := now
		var run *pb.WorkerRun
		if cur != nil {
			if cur.GetAttachedAt() != nil {
				attachedAt = cur.GetAttachedAt()
			}
			run = cur.GetRun()
		}
		x.Controller = &pb.SandboxController{
			OrchestratorId: ctl.GetOrchestratorId(), Name: ctl.GetName(), HostId: ctl.GetHostId(),
			AttachedAt: attachedAt, Reasoning: ctl.GetReasoning(), Run: run,
		}
		x.UpdatedAt = now
		return nil
	})
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return nil, fmt.Errorf("%w: sandbox %s", ErrNotFound, req.GetSandboxId())
		}
		return nil, err
	}
	token := mintToken()
	if err := s.reg.PutWorkerToken(sb.GetId(), token); err != nil {
		return nil, err
	}
	s.indexToken(sb.GetId(), token)
	live := sb
	if launching != nil {
		live = launching
	}
	running := live.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING
	if err := s.gw.register(ctx, sb.GetId(), sandboxRef(live), running, token); err != nil {
		// The controller stands; the registration is retried at the next bring-up.
		s.logf("[worker] attach %s: gateway registration failed (retried at bring-up): %v", sb.GetId(), err)
	}
	if err := WriteBriefing(sb.GetWorkspacePath(), req.GetBriefing()); err != nil {
		s.logf("[worker] attach %s: briefing not written: %v", sb.GetId(), err)
	}
	s.mgr.Emit(sb)
	return sb, nil
}

// Apply is the launch-attach step (FR-110): the record is still CREATING but
// the container exists, so the registration is loaded now, before the agent
// starts. It is sandbox.Manager's controller applier.
func (s *Service) Apply(ctx context.Context, sb *pb.Sandbox, ctl *pb.SandboxController, briefing string) error {
	_, err := s.attach(ctx, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: ctl, Briefing: briefing}, sb)
	return err
}

// Detach clears the controller (FR-111/FR-112): the token is revoked FIRST so
// the endpoint refuses even if `mcp rm` fails, the registration removed, the
// briefing stripped and the controller cleared in one write.
func (s *Service) Detach(ctx context.Context, sandboxID, orchID string, force bool) (*pb.Sandbox, error) {
	sb, err := s.reg.Get(sandboxID)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return nil, fmt.Errorf("%w: sandbox %s", ErrNotFound, sandboxID)
		}
		return nil, err
	}
	cur := sb.GetController()
	if cur == nil {
		return nil, fmt.Errorf("%w: sandbox %s is not a worker", ErrNotFound, sandboxID)
	}
	if orchID != "" && cur.GetOrchestratorId() != orchID {
		return nil, fmt.Errorf("%w: sandbox is controlled by %s", ErrForbidden, cur.GetName())
	}
	if cur.GetReasoning() && !force {
		return nil, fmt.Errorf("%w: reasoning sandbox is not detachable", ErrPrecondition)
	}
	s.revokeToken(sandboxID)
	s.gw.unregister(ctx, sandboxID)
	if err := StripBriefing(sb.GetWorkspacePath()); err != nil {
		s.logf("[worker] detach %s: briefing not stripped: %v", sandboxID, err)
	}
	out, err := s.reg.Update(sandboxID, func(x *pb.Sandbox) error {
		x.Controller = nil
		x.UpdatedAt = timestamppb.New(s.now())
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.mgr.Emit(out)
	return out, nil
}

// Update refreshes the last-known name, the current run and/or the briefing
// (research R3). PERMISSION_DENIED when the orchestrator is not the controller.
func (s *Service) Update(ctx context.Context, req *pb.UpdateWorkerRequest) (*pb.Sandbox, error) {
	out, err := s.reg.Update(req.GetSandboxId(), func(x *pb.Sandbox) error {
		cur := x.GetController()
		if cur == nil || cur.GetOrchestratorId() != req.GetOrchestratorId() {
			return fmt.Errorf("%w: orchestrator %s is not this sandbox's controller", ErrForbidden, req.GetOrchestratorId())
		}
		if req.Name != nil {
			cur.Name = req.GetName()
		}
		if req.GetSetRun() {
			cur.Run = req.GetRun()
		}
		x.UpdatedAt = timestamppb.New(s.now())
		return nil
	})
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return nil, fmt.Errorf("%w: sandbox %s", ErrNotFound, req.GetSandboxId())
		}
		return nil, err
	}
	if req.Briefing != nil {
		if err := WriteBriefing(out.GetWorkspacePath(), req.GetBriefing()); err != nil {
			s.logf("[worker] update %s: briefing not written: %v", out.GetId(), err)
		}
	}
	if req.Name != nil {
		// The cached server's instructions name the orchestrator; rebuild it.
		s.dropServers(s.tokenOf(out.GetId()))
	}
	s.mgr.Emit(out)
	return out, nil
}

// OnDestroy revokes a controlled sandbox's token and registration (best effort)
// — sandbox.Manager's destroy hook.
func (s *Service) OnDestroy(sb *pb.Sandbox) {
	if sb.GetController() == nil {
		if _, err := s.reg.GetWorkerToken(sb.GetId()); err != nil {
			return
		}
	}
	s.revokeToken(sb.GetId())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.gw.unregister(ctx, sb.GetId())
}

// OnBringUp re-loads a controlled sandbox's registration after a restart or
// refresh (the attach-time load is skipped while it is not running) — the
// manager's bring-up hook.
func (s *Service) OnBringUp(sb *pb.Sandbox) {
	if sb.GetController() == nil {
		return
	}
	if _, err := s.reg.GetWorkerToken(sb.GetId()); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.gw.load(ctx, sb.GetId(), sandboxRef(sb)); err != nil {
		s.logf("[worker] bring-up %s: %v", sb.GetId(), err)
	}
}

// AcceptLink serves one OrchestratorLink stream (the gRPC handler and Local
// both call it).
func (s *Service) AcceptLink(stream ServerStream) error { return s.links.AcceptLink(stream) }

// Call relays a worker's tool call to its orchestrator over the link.
func (s *Service) Call(ctx context.Context, orchID, orchName, sandboxID, tool, argsJSON string) (string, error) {
	return s.links.Call(ctx, orchID, orchName, sandboxID, tool, argsJSON)
}

// --- token index ---

func (s *Service) indexToken(sandboxID, token string) {
	s.tokMu.Lock()
	var old []string
	for tok, sb := range s.tokens {
		if sb == sandboxID {
			delete(s.tokens, tok)
			old = append(old, tok)
		}
	}
	s.tokens[token] = sandboxID
	s.tokMu.Unlock()
	s.dropServers(old...)
}

func (s *Service) revokeToken(sandboxID string) {
	if err := s.reg.DeleteWorkerToken(sandboxID); err != nil {
		s.logf("[worker] revoke token %s: %v", sandboxID, err)
	}
	s.tokMu.Lock()
	var old []string
	for tok, sb := range s.tokens {
		if sb == sandboxID {
			delete(s.tokens, tok)
			old = append(old, tok)
		}
	}
	s.tokMu.Unlock()
	s.dropServers(old...)
}

// tokenOf returns the sandbox's current token ("" when it has none).
func (s *Service) tokenOf(sandboxID string) string {
	s.tokMu.RLock()
	defer s.tokMu.RUnlock()
	for tok, sb := range s.tokens {
		if sb == sandboxID {
			return tok
		}
	}
	return ""
}

// sandboxForToken resolves a token to its sandbox id ("" when unknown/revoked).
func (s *Service) sandboxForToken(token string) string {
	s.tokMu.RLock()
	defer s.tokMu.RUnlock()
	return s.tokens[token]
}

// sandboxRef is the handle sbx knows a sandbox by (its container ref, else its name).
func sandboxRef(sb *pb.Sandbox) string {
	if r := sb.GetContainerRef(); r != "" {
		return r
	}
	if n := sb.GetDisplayName(); n != "" {
		return n
	}
	return sb.GetId()
}
