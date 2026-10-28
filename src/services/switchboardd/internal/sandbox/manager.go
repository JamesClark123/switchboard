package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/escapehatch"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/portforward"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store is the subset of the registry the Manager needs (eases testing).
type Store interface {
	Put(*pb.Sandbox) error
	Get(string) (*pb.Sandbox, error)
	Delete(string) error
	List() ([]*pb.Sandbox, error)
	Update(string, func(*pb.Sandbox) error) (*pb.Sandbox, error)
}

// Manager orchestrates sandbox lifecycle on a single host.
type Manager struct {
	store         Store
	runner        Runner
	workspaceRoot string
	hostID        string

	// onChange is invoked after any sandbox state change so the gRPC layer can
	// fan it out on the event stream. MAY be nil.
	onChange func(*pb.Sandbox)

	// injectHooks, when set, is called after a workspace is seeded so the agent's
	// Claude Code hooks can be written into it (US4). MAY be nil.
	injectHooks func(sandboxID, workspacePath string) error

	// injectEscapeHatch, when set, is called after a workspace is seeded to write the
	// escape-hatch wrapper + CLAUDE.md rule block (feature 005), given the sandbox's
	// resolved command set. MAY be nil.
	injectEscapeHatch func(sandboxID, workspacePath string, commands []*pb.EscapeHatchCommand) error

	// ops is the per-sandbox operation latch shared by Refresh, AddSources, and
	// RemoveSources (feature 007, FR-066): seed-mutating operations on one
	// workspace never interleave. See oplock.go.
	ops opLock
	// lifecycle marks sandboxes Stop/Restart/AddKit/Destroy/Rename are working
	// on, and started is when this manager was built: reconciliation skips the
	// former and settles only transitional records older than the latter (SWI-13).
	lifecycle inflight
	started   time.Time
	// unknownStatuses records the sbx status words reconciliation has already
	// warned about (warnUnknownStatus).
	unknownStatuses sync.Map

	// --- feature 009 (orchestrator worker side; research R3/R4) ---
	// onDestroy, when set, is called with the record just before it is deleted
	// (Destroy, or the vanished-workspace prune in List) so the worker service
	// can revoke the sandbox's gateway token and registration. MAY be nil.
	onDestroy func(sb *pb.Sandbox)
	// onBringUp, when set, is called after Restart/Refresh bring a sandbox back
	// to RUNNING, so a controlled sandbox's gateway registration can be
	// re-loaded (the attach-time load is skipped while it is not running). MAY be nil.
	onBringUp func(sb *pb.Sandbox)
	// goBringUp, when set, replaces `go onBringUp` where the manager does not
	// wait on the observer (Announce): it returns once the bring-up is under way,
	// so nothing reads the sandbox as settled in between. MAY be nil.
	goBringUp func(sb *pb.Sandbox)
	// onLaunched, when set, is called once a controlled sandbox's Launch has
	// turned it RUNNING, so the worker service can warm-start its agent (SWI-15:
	// applyController runs before RUNNING, when no agent may start). MAY be nil.
	onLaunched func(sb *pb.Sandbox)
	// applyController, when set, is called by Launch after create and the MCP
	// marks and BEFORE the record turns RUNNING (FR-110) with the launching
	// record (container_ref filled in) and the LaunchRequest's controller +
	// briefing: the worker service records the controller, mints the token,
	// registers + loads the gateway server and writes the briefing. A failure is
	// logged and never fails the launch. MAY be nil (controller ignored).
	applyController func(ctx context.Context, sb *pb.Sandbox, controller *pb.SandboxController, briefing string) error
}

// SetOnDestroy registers the destroy observer (feature 009).
func (m *Manager) SetOnDestroy(fn func(sb *pb.Sandbox)) { m.onDestroy = fn }

// SetOnBringUp registers the bring-up observer (feature 009).
func (m *Manager) SetOnBringUp(fn func(sb *pb.Sandbox)) { m.onBringUp = fn }

// SetGoBringUp registers the non-blocking form of the bring-up observer (SWI-15).
func (m *Manager) SetGoBringUp(fn func(sb *pb.Sandbox)) { m.goBringUp = fn }

// SetOnLaunched registers the launched-worker observer (SWI-15).
func (m *Manager) SetOnLaunched(fn func(sb *pb.Sandbox)) { m.onLaunched = fn }

// SetControllerApplier registers the launch-attach step (feature 009, FR-110).
func (m *Manager) SetControllerApplier(fn func(ctx context.Context, sb *pb.Sandbox, controller *pb.SandboxController, briefing string) error) {
	m.applyController = fn
}

// Controller returns the sandbox's recorded controller (nil when it is not a
// worker), or the registry's error for an unknown id.
func (m *Manager) Controller(id string) (*pb.SandboxController, error) {
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	return sb.GetController(), nil
}

// leaveRunning records a transition OUT of RUNNING. A run recorded on the
// sandbox cannot survive it (the agent session is gone; the orchestrator folds
// the status event into a failed run, research R7) — except a run the
// orchestrator has already handed to the developer for review, which a stop
// keeps so the review can still be closed (FR-156 edge case).
//
// Nor can the agent's status: WORKING, WAITING (SWI-12) and NEEDS_INPUT name a
// session that is gone. Kept across a restart, the first would leave the
// sandbox busy with no agent left to report otherwise (a worker never free
// again), and NEEDS_INPUT would show a question nothing is asking any more
// (its run was failed on the stop) and keep the worker from dispatch until it
// is marked free. It becomes IDLE; the next session reports its own status. A
// run awaiting review stays visible through the kept controller run.
//
// A run that still has cleanup to run (any hold, or a cleanup step on it) is
// kept too, held NEEDS_DEVELOPER (SWI-17, research R7): the agent's session
// was never cleared, so a restarted sandbox must not read free before the
// developer (or a fresh session's SessionStart) clears it (FR-216, SC-001).
func leaveRunning(s *pb.Sandbox, state pb.SandboxState) {
	s.State = state
	if state == pb.SandboxState_SANDBOX_STATE_RUNNING {
		return
	}
	if c := s.GetController(); c != nil && c.GetRun() != nil {
		switch run := c.GetRun(); {
		case cleanupPending(run):
			holdForDeveloper(run)
		case !run.GetNeedsReview():
			c.Run = nil
		}
	}
	if a := s.GetAgent(); a != nil &&
		(a.GetStatus() == pb.AgentStatus_AGENT_STATUS_WORKING || a.GetStatus() == pb.AgentStatus_AGENT_STATUS_WAITING ||
			a.GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT) {
		a.Status = pb.AgentStatus_AGENT_STATUS_IDLE
	}
}

// SetHookInjector registers a callback invoked after seeding to inject agent
// hooks into the workspace (US4).
func (m *Manager) SetHookInjector(fn func(sandboxID, workspacePath string) error) {
	m.injectHooks = fn
}

// SetEscapeHatchInjector registers a callback invoked after seeding to inject the
// escape-hatch wrapper + rule block (feature 005).
func (m *Manager) SetEscapeHatchInjector(fn func(sandboxID, workspacePath string, commands []*pb.EscapeHatchCommand) error) {
	m.injectEscapeHatch = fn
}

// injectEscapeHatchInto runs the escape-hatch injector for a sandbox, if registered.
// Best-effort: a failure is logged, never fatal (mirrors hook injection).
func (m *Manager) injectEscapeHatchInto(sb *pb.Sandbox, onLog func(string)) {
	if m.injectEscapeHatch == nil {
		return
	}
	if err := m.injectEscapeHatch(sb.GetId(), sb.GetWorkspacePath(), sb.GetEscapeHatchCommands()); err != nil && onLog != nil {
		onLog("warning: escape-hatch injection failed: " + err.Error())
	}
}

// NewManager constructs a Manager.
func NewManager(store Store, runner Runner, workspaceRoot, hostID string) *Manager {
	return &Manager{store: store, runner: runner, workspaceRoot: workspaceRoot, hostID: hostID, started: time.Now()}
}

// SetOnChange registers a sandbox-change observer.
func (m *Manager) SetOnChange(fn func(*pb.Sandbox)) { m.onChange = fn }

func (m *Manager) emit(s *pb.Sandbox) {
	if m.onChange != nil && s != nil {
		m.onChange(s)
	}
}

// Emit publishes a record another component changed through the registry
// directly (feature 009: the worker side's controller writes), so it reaches
// the event stream exactly like the manager's own transitions.
func (m *Manager) Emit(s *pb.Sandbox) { m.emit(s) }

// LaunchRequest captures everything needed to create a sandbox.
type LaunchRequest struct {
	Config        *pb.ConfigSnapshot
	Sources       []*pb.SourceRef
	AgentOverride *pb.AgentSpec
	DisplayName   string
	// KitSources are already-resolved `--kit` sources (feature 004, FR-032). The
	// gRPC layer materializes inline KitSpecs to local paths before calling in, so
	// the Manager deals only in strings sbx understands.
	KitSources []string
	// EscapeHatchCommands is the resolved escape-hatch command set for this sandbox
	// (feature 005, FR-036) — the gRPC layer merges the attached kits' commands with
	// later-kit-wins before calling in. Persisted on the sandbox so the allowlist is
	// enforceable and survives a container recreate.
	EscapeHatchCommands []*pb.EscapeHatchCommand
	// Services is the resolved service set for this sandbox (feature 006, FR-044) —
	// the gRPC layer merges the attached kits' services with later-kit-wins before
	// calling in. Persisted on the sandbox so the startable set is enforceable and
	// survives a container recreate.
	Services []*pb.KitService
	// Mcp is the daemon's resolved attach-by-default set and mode for this launch
	// (feature 008, FR-081/FR-100), computed by the gRPC layer from the gateway
	// manager. nil = no gateway on this host: launch exactly as before.
	Mcp *McpAttach
	// EmptyWorkspace (feature 009, research R11) launches with NO seeded folders:
	// zero sources are accepted, the workspace directory is created empty and
	// the record carries no sources. The orchestrator's reasoning sandbox uses it.
	EmptyWorkspace bool
	// Controller + ControllerBriefing (feature 009, FR-110): when set, the
	// sandbox is attached to this orchestrator right after create and before the
	// agent can start, through the registered controller applier. A failure is
	// logged and never fails the launch.
	Controller         *pb.SandboxController
	ControllerBriefing string
}

// McpAttach is what one launch applies from the daemon's MCP marks (data-model
// "Launch resolution"): ADDITIVE loads each server after create, before the agent
// starts; EXCLUSIVE pre-loads the set statically. Skipped names (stale marks) are
// only reported in the launch log.
type McpAttach struct {
	Mode    pb.McpAttachMode
	Servers []string
	Skipped []string
}

// mcpLoadTimeout bounds each `mcp load` at launch (research R11). A package
// constant, never an env var.
const mcpLoadTimeout = 60 * time.Second

// List returns all sandboxes on this host, first pruning any whose retained
// workspace directory has vanished (an unusable, out-of-sync record) so stale
// entries disappear from the UI automatically.
func (m *Manager) List() ([]*pb.Sandbox, error) {
	all, err := m.store.List()
	if err != nil {
		return nil, err
	}
	out := make([]*pb.Sandbox, 0, len(all))
	for _, sb := range all {
		if m.workspaceGone(sb) {
			if m.onDestroy != nil {
				m.onDestroy(sb)
			}
			if delErr := m.store.Delete(sb.GetId()); delErr == nil {
				m.emit(&pb.Sandbox{Id: sb.GetId(), State: pb.SandboxState_SANDBOX_STATE_DESTROYING})
			}
			continue
		}
		out = append(out, sb)
	}
	return out, nil
}

// workspaceGone reports whether a sandbox's retained copy is missing on disk. A
// CREATING sandbox is exempt (its directory may not exist yet mid-launch).
func (m *Manager) workspaceGone(sb *pb.Sandbox) bool {
	if sb.GetState() == pb.SandboxState_SANDBOX_STATE_CREATING {
		return false
	}
	wp := sb.GetWorkspacePath()
	if wp == "" || !within(m.workspaceRoot, wp) {
		return false
	}
	_, err := os.Stat(wp)
	return os.IsNotExist(err)
}

// Get returns a single sandbox by id.
func (m *Manager) Get(id string) (*pb.Sandbox, error) { return m.store.Get(id) }

// Launch duplicates (or clones) the sources, starts the sandbox via the runner,
// and persists a running record. Progress and sbx output are streamed via the
// callbacks (FR-028). The originals are never modified (SC-002).
func (m *Manager) Launch(ctx context.Context, req LaunchRequest, onProgress func(duplicate.Progress), onLog func(string)) (*pb.Sandbox, error) {
	if req.Config == nil {
		return nil, errors.New("config is required")
	}
	if len(req.Sources) == 0 && !req.EmptyWorkspace {
		return nil, errors.New("at least one source is required")
	}

	id := uuid.NewString()
	now := timestamppb.Now()

	// The name is unique per host and IS the workspace directory (FR-012e). The
	// uuid stays the registry key / cross-host identity and the sbx handle.
	name, err := m.resolveName(strings.TrimSpace(req.DisplayName), req.Config.GetName(), id)
	if err != nil {
		return nil, err
	}
	workspacePath := filepath.Join(m.workspaceRoot, name)

	agent := req.Config.GetAgent()
	if agent == nil || agent.GetKind() == "" {
		agent = req.AgentOverride // FR-016b: user-chosen agent when config omits one
	}

	sb := &pb.Sandbox{
		Id:                  id,
		DisplayName:         name,
		State:               pb.SandboxState_SANDBOX_STATE_CREATING,
		HostId:              m.hostID,
		ConfigSnapshot:      req.Config,
		ConfigLabel:         req.Config.GetName(),
		Sources:             req.Sources,
		SeedingMode:         req.Config.GetSeedingMode(),
		WorkspacePath:       workspacePath,
		Agent:               &pb.AgentSession{Spec: agent, Status: pb.AgentStatus_AGENT_STATUS_IDLE, LastEventAt: now},
		Kits:                req.KitSources,
		EscapeHatchCommands: req.EscapeHatchCommands,
		Services:            req.Services,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := m.store.Put(sb); err != nil {
		return nil, err
	}
	m.emit(sb)

	if err := m.seed(ctx, sb, onProgress, onLog); err != nil {
		return m.fail(sb, err)
	}

	// Drop a workspace marker so `sxb` run from inside the copy can auto-resolve
	// this sandbox (feature 003, FR-017). Best-effort; ResolveWorkspace is the
	// authoritative fallback.
	if err := writeWorkspaceMarker(workspacePath, m.hostID, id); err != nil && onLog != nil {
		onLog("warning: workspace marker not written: " + err.Error())
	}

	// Inject agent hooks into the seeded workspace so the agent reports task
	// completion / needs-prompting back to the daemon (US4). Non-fatal.
	if m.injectHooks != nil {
		if err := m.injectHooks(id, workspacePath); err != nil && onLog != nil {
			onLog("warning: hook injection failed: " + err.Error())
		}
	}
	m.injectEscapeHatchInto(sb, onLog)

	var staticMcp []string
	if req.Mcp != nil && req.Mcp.Mode == pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
		staticMcp = req.Mcp.Servers
	}
	launchLog, captured := captureOutput(onLog)
	ref, err := m.runner.Launch(ctx, LaunchSpec{
		SandboxID:     id,
		Name:          name,
		WorkspacePath: workspacePath,
		KitOptions:    req.Config.GetKitOptions(),
		SeedingMode:   req.Config.GetSeedingMode(),
		Sources:       req.Sources,
		KitSources:    req.KitSources,
		StaticMcp:     staticMcp,
	}, launchLog)
	if err != nil {
		// Exclusive mode pins the set at create, so a tool-name clash fails the
		// create itself; name it and the remedy instead of only the raw text.
		if hint := collisionHint(err, captured); len(staticMcp) > 0 && hint != "" {
			err = fmt.Errorf("%w: %s; additive mode skips the clashing server instead (runtime: %v)", ErrMcpToolCollision, hint, err)
			if onLog != nil {
				onLog("mcp: " + err.Error())
			}
		}
		return m.fail(sb, err)
	}

	// Feature 008: apply the daemon's attach-by-default servers now — after
	// create, before the record turns RUNNING and any agent session is started
	// (research R4). A failed or stale server is logged and skipped, never a
	// failed launch (FR-083/FR-084).
	attached, mode := m.applyMcp(ctx, ref, req.Mcp, onLog)

	// Feature 009 (FR-110): a launch-attached sandbox is a worker before its
	// agent starts — the controller, token, gateway registration + load and the
	// briefing are applied here, at the same point additive marks were loaded,
	// still before the record turns RUNNING. Never a failed launch.
	if req.Controller != nil && m.applyController != nil {
		cur := proto.Clone(sb).(*pb.Sandbox)
		cur.ContainerRef = ref
		cur.State = pb.SandboxState_SANDBOX_STATE_RUNNING
		if err := m.applyController(ctx, cur, req.Controller, req.ControllerBriefing); err != nil {
			if onLog != nil {
				onLog("orchestrator: skipped — " + err.Error())
			}
			log.Printf("[warn] launch %s: orchestrator attach skipped: %v", id, err)
		} else if onLog != nil {
			onLog("orchestrator: attached to " + req.Controller.GetName())
		}
	}

	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.ContainerRef = ref
		s.State = pb.SandboxState_SANDBOX_STATE_RUNNING
		s.McpServers = attached
		s.McpAttachMode = mode
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	if m.onLaunched != nil && out.GetController() != nil {
		m.onLaunched(out)
	}
	return out, nil
}

// seed performs duplicate or clone into the controlled folder. A record with no
// sources (feature 009, an EmptyWorkspace launch) gets an empty directory.
func (m *Manager) seed(ctx context.Context, sb *pb.Sandbox, onProgress func(duplicate.Progress), onLog func(string)) error {
	if len(sb.GetSources()) == 0 {
		return os.MkdirAll(sb.GetWorkspacePath(), 0o755)
	}
	if sb.GetSeedingMode() == pb.SeedingMode_SEEDING_MODE_CLONE {
		if err := os.MkdirAll(sb.GetWorkspacePath(), 0o755); err != nil {
			return err
		}
		for _, src := range sb.GetSources() {
			if !src.GetIsRepo() {
				return fmt.Errorf("clone mode requires a git repo: %s", src.GetPath())
			}
			dest := filepath.Join(sb.GetWorkspacePath(), filepath.Base(src.GetPath()))
			if err := m.runner.CloneRepo(ctx, src.GetPath(), dest, onLog); err != nil {
				return err
			}
		}
		return nil
	}

	paths := make([]string, 0, len(sb.GetSources()))
	for _, s := range sb.GetSources() {
		paths = append(paths, s.GetPath())
	}
	_, err := duplicate.CopyAll(paths, sb.GetWorkspacePath(), onProgress)
	return err
}

func (m *Manager) fail(sb *pb.Sandbox, cause error) (*pb.Sandbox, error) {
	out, err := m.store.Update(sb.GetId(), func(s *pb.Sandbox) error {
		leaveRunning(s, pb.SandboxState_SANDBOX_STATE_ERROR)
		s.Error = cause.Error()
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, cause
}

// sbxHandle is the name sbx knows a sandbox by. The daemon creates with
// `sbx create --name <DisplayName>` (the per-host-unique human name), so that
// name is the handle for stop/start/rm/status. It falls back to the uuid only if
// a record somehow has no name.
func sbxHandle(sb *pb.Sandbox) string {
	if n := sb.GetDisplayName(); n != "" {
		return n
	}
	return sb.GetId()
}

// sbxRefs lists the handles a sandbox might be addressed by in sbx, most-specific
// first: the human name, then the uuid id as a fallback. The id fallback recovers
// records that are out of sync with sbx — e.g. renamed before names drove the
// sbx --name, so the container is still named by the id.
func sbxRefs(sb *pb.Sandbox) []string {
	name, id := sb.GetDisplayName(), sb.GetId()
	if name == "" || name == id {
		return []string{id}
	}
	return []string{name, id}
}

// tryRefs runs op against each candidate handle until one succeeds, returning the
// last error if all fail.
func tryRefs(refs []string, op func(string) error) error {
	var err error
	for _, r := range refs {
		if err = op(r); err == nil {
			return nil
		}
	}
	if err == nil {
		return nil
	}
	// Report every handle tried so a failure isn't misread as "only tried the id".
	return fmt.Errorf("no sbx sandbox matched any of %v: %w", refs, err)
}

// Stop stops the container but RETAINS the workspace copy (FR-012a).
func (m *Manager) Stop(ctx context.Context, id string) (*pb.Sandbox, error) {
	defer m.lifecycle.begin(id)()
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	log.Printf("[debug] stop sandbox %s via %v (stored container_ref=%q)", id, sbxRefs(sb), sb.GetContainerRef())
	if sb.GetContainerRef() != "" {
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Stop(ctx, r) }); err != nil {
			return nil, err
		}
	}
	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		leaveRunning(s, pb.SandboxState_SANDBOX_STATE_STOPPED)
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// Restart brings a stopped sandbox back up from its retained copy (FR-012b). It
// first tries to resume an existing container (`sbx start`, by name then id), but
// many sbx setups don't keep a startable container across a stop, so on failure —
// or when there's no handle — it relaunches from the retained workspace copy,
// clearing any stale container first.
func (m *Manager) Restart(ctx context.Context, id string, onLog func(string)) (*pb.Sandbox, error) {
	defer m.lifecycle.begin(id)()
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}

	ref, err := m.bringUp(ctx, sb, onLog)
	if err != nil {
		return nil, err
	}
	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.ContainerRef = ref
		s.State = pb.SandboxState_SANDBOX_STATE_RUNNING
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	m.broughtUp(out)
	return out, nil
}

// broughtUp runs the bring-up observer for a controlled sandbox (feature 009).
func (m *Manager) broughtUp(sb *pb.Sandbox) {
	if m.onBringUp != nil && sb.GetController() != nil {
		m.onBringUp(sb)
	}
}

// applyMcp attaches the resolved MCP set to a freshly created sandbox and returns
// what was actually attached plus the mode in effect. Exclusive mode was already
// pre-loaded by `--static-mcp` on create; additive mode loads each server now.
// Nothing here can fail the launch.
func (m *Manager) applyMcp(ctx context.Context, ref string, att *McpAttach, onLog func(string)) ([]string, pb.McpAttachMode) {
	logf := func(format string, args ...any) {
		if onLog != nil {
			onLog(fmt.Sprintf(format, args...))
		}
	}
	if att == nil {
		return nil, pb.McpAttachMode_MCP_ATTACH_MODE_UNSPECIFIED
	}
	for _, name := range att.Skipped {
		logf("mcp: skipped stale mark %s (no longer registered on this host)", name)
	}
	mode := att.Mode
	if mode == pb.McpAttachMode_MCP_ATTACH_MODE_UNSPECIFIED {
		mode = pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE
	}
	if len(att.Servers) == 0 {
		return nil, mode
	}
	if mode == pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
		logf("mcp: pre-loaded %s (exclusive — the agent gets only these)", strings.Join(att.Servers, ", "))
		return append([]string(nil), att.Servers...), mode
	}
	var attached []string
	for _, name := range att.Servers {
		lctx, cancel := context.WithTimeout(ctx, mcpLoadTimeout)
		logLine, captured := captureOutput(onLog)
		err := m.runner.LoadMcp(lctx, ref, name, logLine)
		cancel()
		if err != nil {
			if hint := collisionHint(err, captured); hint != "" {
				// Actionable line first; the raw runtime text stays in the log.
				logf("mcp: skipped %s: %s", name, hint)
				logf("mcp: runtime said: %v", err)
				continue
			}
			logf("mcp: skipped %s: %v", name, err)
			continue
		}
		attached = append(attached, name)
		logf("mcp: attached %s", name)
	}
	return attached, mode
}

// bringUp resumes sb's container and returns the resulting handle, relaunching
// from the retained workspace copy when sbx cannot resume it. Shared by Restart
// (FR-012b) and Refresh (FR-030); it neither re-seeds nor writes the record.
//
// It deliberately re-applies neither MCP marks nor `--static-mcp` (feature 008,
// FR-082): the runtime persists a sandbox's loaded/static servers across restarts,
// and the attached set is fixed at launch.
//
// The relaunch path re-passes sb.Kits so kits attached to this sandbox survive a
// container recreate — `--kit` is only honoured at creation, so a relaunch that
// dropped them would silently return a differently-provisioned sandbox.
func (m *Manager) bringUp(ctx context.Context, sb *pb.Sandbox, onLog func(string)) (string, error) {
	if sb.GetContainerRef() != "" {
		log.Printf("[debug] bring up sandbox %s: resume via %v", sb.GetId(), sbxRefs(sb))
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Start(ctx, r) }); err == nil {
			return sb.GetContainerRef(), nil
		} else {
			// sbx couldn't resume it (no such container any more, runtime gone,
			// etc.). Drop any stale container and relaunch from the retained copy.
			log.Printf("[debug] bring up %s: start failed (%v); relaunching from the retained copy", sb.GetId(), err)
			_ = tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Destroy(ctx, r) })
		}
	}
	return m.runner.Launch(ctx, LaunchSpec{
		SandboxID:     sb.GetId(),
		Name:          sb.GetDisplayName(),
		WorkspacePath: sb.GetWorkspacePath(),
		KitOptions:    sb.GetConfigSnapshot().GetKitOptions(),
		SeedingMode:   sb.GetSeedingMode(),
		Sources:       sb.GetSources(),
		KitSources:    sb.GetKits(),
	}, onLog)
}

// Refresh deletes a sandbox's retained workspace copy, re-seeds it from the
// recorded sources, and brings the sandbox back up on the SAME container
// (feature 004, FR-030).
//
// DESTRUCTIVE: everything the agent wrote into the workspace — including
// uncommitted work — is lost. Container state (installed packages, images, agent
// history) survives, because the container is resumed rather than recreated.
//
// The old copy MUST be deleted rather than copied over: duplicate.copyTree creates
// symlinks via os.Symlink, which fails EEXIST on a second pass, and it never
// deletes, so copying over a populated workspace would yield a union of the old and
// new trees rather than the fresh one the caller asked for.
func (m *Manager) Refresh(ctx context.Context, id string, onProgress func(duplicate.Progress), onLog func(string)) (*pb.Sandbox, error) {
	// Refresh deletes and re-seeds the whole workspace, so it takes the same
	// per-sandbox latch as the feature-007 source edits (FR-066): an add renaming
	// into a workspace a refresh is simultaneously wiping is exactly the
	// interleaving the latch exists to refuse.
	if err := m.ops.acquire(id, opRefresh); err != nil {
		return nil, err
	}
	defer m.ops.release(id)

	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	// A record with no sources is an EmptyWorkspace launch (feature 009): a
	// refresh recreates the empty directory rather than refusing.
	wp := sb.GetWorkspacePath()
	if wp == "" || !within(m.workspaceRoot, wp) {
		return nil, fmt.Errorf("refuse to refresh %s: workspace %q is outside the controlled folder %q", id, wp, m.workspaceRoot)
	}

	// Stop first — the workspace is mounted into the running container and is about
	// to be deleted underneath it. Best-effort: an already-stopped or vanished
	// container must not block a refresh.
	leave := pb.SandboxState_SANDBOX_STATE_STOPPED
	if sb.GetContainerRef() != "" {
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Stop(ctx, r) }); err != nil {
			log.Printf("[warn] refresh %s: stop failed (continuing): %v", id, err)
			if onLog != nil {
				onLog("warning: stop failed, continuing: " + err.Error())
			}
			if m.listedRunning(ctx, sb) {
				// STOPPED tells the teardown the container is down, so its
				// in-sandbox services are not signalled (SWI-13). This one is
				// still up: CREATING, as for a kit being attached, has them
				// stopped rather than orphaned.
				leave = pb.SandboxState_SANDBOX_STATE_CREATING
			}
		}
	}
	// Emitting a non-RUNNING state ends the terminal session and drops the cached
	// PTY (server on-change hook, FR-006). Required, not incidental: those sessions
	// sit in the workspace we are about to delete, and a stale PTY would hand the
	// next attach an immediate EOF.
	stopped, err := m.store.Update(id, func(s *pb.Sandbox) error {
		leaveRunning(s, leave)
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(stopped)

	if err := os.RemoveAll(wp); err != nil {
		return m.fail(sb, fmt.Errorf("delete workspace copy: %w", err))
	}
	if err := m.seed(ctx, sb, onProgress, onLog); err != nil {
		return m.fail(sb, err)
	}
	// The wipe took the marker and the injected hooks with it, so both are re-run —
	// unlike Restart, which reuses a retained copy that still has them.
	if err := writeWorkspaceMarker(wp, m.hostID, id); err != nil && onLog != nil {
		onLog("warning: workspace marker not written: " + err.Error())
	}
	if m.injectHooks != nil {
		if err := m.injectHooks(id, wp); err != nil && onLog != nil {
			onLog("warning: hook injection failed: " + err.Error())
		}
	}
	// The wipe took the wrapper + rule block too; re-inject from the persisted set.
	m.injectEscapeHatchInto(sb, onLog)

	ref, err := m.bringUp(ctx, sb, onLog)
	if err != nil {
		return m.fail(sb, err)
	}
	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.ContainerRef = ref
		s.State = pb.SandboxState_SANDBOX_STATE_RUNNING
		s.Error = "" // a previous failure is resolved once the refresh completes
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	m.broughtUp(out)
	return out, nil
}

// AddKit attaches a kit to an existing sandbox via `sbx kit add` (feature 004,
// FR-033) and records the source so a later container recreate re-attaches it.
//
// kitSource is already resolved (a materialized local path, a .zip, a git+URL, or
// an OCI ref) — the gRPC layer materializes inline kits before calling in.
//
// sbx restarts the sandbox to apply the kit, preserving VM state. That restart
// happens INSIDE sbx, so nothing else would tell the daemon that the PTY it holds
// has died. Emitting a non-RUNNING state first drives the server's on-change hook
// to end the terminal session and drop the cached PTY; without it the next attach
// is handed a dead PTY and the client sees an immediate EOF.
// newCommands carries the attached kit's escape-hatch commands (feature 005); they
// are merged into the sandbox's resolved set with later-kit-wins on name collision.
// newServices carries the attached kit's declared services (feature 006); they are
// merged into the sandbox's resolved set with the same later-kit-wins rule.
func (m *Manager) AddKit(ctx context.Context, id, kitSource string, newCommands []*pb.EscapeHatchCommand, newServices []*pb.KitService, onLog func(string)) (*pb.Sandbox, error) {
	defer m.lifecycle.begin(id)()
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	kitSource = strings.TrimSpace(kitSource)
	if kitSource == "" {
		return nil, errors.New("kit source is required")
	}
	// Merge up front so an invalid escape-hatch entry fails the attach before sbx
	// restarts the sandbox.
	mergedCommands, err := escapehatch.Resolve(sb.GetEscapeHatchCommands(), newCommands)
	if err != nil {
		return nil, err
	}
	// Same up-front merge for services: an invalid declaration must fail the attach
	// before sbx restarts the sandbox, not after.
	mergedServices, err := portforward.Resolve(sb.GetServices(), newServices)
	if err != nil {
		return nil, err
	}
	if sb.GetContainerRef() == "" {
		return nil, fmt.Errorf("sandbox %s has no container to attach a kit to", id)
	}

	applying, err := m.store.Update(id, func(s *pb.Sandbox) error {
		leaveRunning(s, pb.SandboxState_SANDBOX_STATE_CREATING)
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(applying)

	if err := tryRefs(sbxRefs(sb), func(r string) error {
		return m.runner.AddKit(ctx, r, kitSource, onLog)
	}); err != nil {
		return m.fail(sb, err)
	}

	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		if !slices.Contains(s.GetKits(), kitSource) {
			s.Kits = append(s.Kits, kitSource)
		}
		s.EscapeHatchCommands = mergedCommands
		s.Services = mergedServices
		s.State = pb.SandboxState_SANDBOX_STATE_RUNNING
		s.Error = ""
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The attached kit may have changed the escape-hatch set; re-inject the wrapper +
	// rule block from the merged set.
	m.injectEscapeHatchInto(out, onLog)
	m.emit(out)
	return out, nil
}

// ValidateKit checks a materialized kit directory with the host `sbx`
// (feature 004, FR-034), returning sbx's combined output alongside the exit error
// so callers can surface its diagnostics verbatim.
func (m *Manager) ValidateKit(ctx context.Context, kitDir string) (string, error) {
	return m.runner.ValidateKit(ctx, kitDir)
}

// Destroy removes the container and DELETES the retained copy (FR-012c).
func (m *Manager) Destroy(ctx context.Context, id string) (bool, error) {
	defer m.lifecycle.begin(id)()
	sb, err := m.store.Get(id)
	if err != nil {
		return false, err
	}
	if sb.GetContainerRef() != "" {
		// Best-effort: the container may already be gone or out of sync (renamed
		// without its folder, etc.). Try the name then the id; if both fail, log
		// and still remove the workspace + record so the entry can be cleared.
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Destroy(ctx, r) }); err != nil {
			log.Printf("[warn] destroy %s: sbx rm failed for %v (removing record anyway): %v", id, sbxRefs(sb), err)
		}
	}
	deletedWorkspace := false
	if wp := sb.GetWorkspacePath(); wp != "" && within(m.workspaceRoot, wp) {
		if err := os.RemoveAll(wp); err != nil {
			return false, err
		}
		deletedWorkspace = true
	}
	if m.onDestroy != nil {
		m.onDestroy(sb)
	}
	if err := m.store.Delete(id); err != nil {
		return false, err
	}
	m.emit(&pb.Sandbox{Id: id, State: pb.SandboxState_SANDBOX_STATE_DESTROYING})
	return deletedWorkspace, nil
}

// SetAgentStatus updates a sandbox's agent status (driven by injected Claude Code
// hooks — FR-024/025) and publishes the change so subscribers see it live.
//
// An agent that exits while its run still has cleanup to run holds the
// sandbox NEEDS_DEVELOPER (SWI-17, research R7): no hook reports an exit (it
// arrives from agent.Registry's exit watch), and the remaining custom cleanup
// steps must not be typed into the fresh session that follows, whose own
// SessionStart then ends the hold.
func (m *Manager) SetAgentStatus(sandboxID string, status pb.AgentStatus, now time.Time) (*pb.Sandbox, error) {
	out, err := m.store.Update(sandboxID, func(s *pb.Sandbox) error {
		if s.Agent == nil {
			s.Agent = &pb.AgentSession{}
		}
		s.Agent.Status = status
		s.Agent.LastEventAt = timestamppb.New(now)
		s.UpdatedAt = timestamppb.Now()
		if run := s.GetController().GetRun(); status == pb.AgentStatus_AGENT_STATUS_EXITED && cleanupPending(run) {
			holdForDeveloper(run)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// SetAgentEvidence records hook evidence on a sandbox's agent session (SWI-17,
// research R4: last_submit_at, last_turn_end_at, …) through fn and publishes
// the change once. The status is left as it is: SetAgentStatus owns it.
func (m *Manager) SetAgentEvidence(sandboxID string, fn func(*pb.AgentSession)) (*pb.Sandbox, error) {
	out, err := m.store.Update(sandboxID, func(s *pb.Sandbox) error {
		if s.Agent == nil {
			s.Agent = &pb.AgentSession{}
		}
		fn(s.Agent)
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// ClearHeldRun ends a NEEDS_DEVELOPER hold (SWI-17, FR-216, research R7): the
// sandbox's agent started a fresh session, so the held run's context is gone
// and the worker is free again. A compare-and-set: the run is cleared only
// while its hold is still NEEDS_DEVELOPER, and cleared reports whether it was.
func (m *Manager) ClearHeldRun(sandboxID string) (cleared bool, err error) {
	out, err := m.store.Update(sandboxID, func(s *pb.Sandbox) error {
		cleared = false
		if c := s.GetController(); c != nil && c.GetRun().GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
			c.Run = nil
			s.UpdatedAt = timestamppb.Now()
			cleared = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if cleared {
		m.emit(out)
	}
	return cleared, nil
}

// cleanupPending reports whether a worker run still has cleanup to run: it is
// held (cleaning up, freeing or already needing the developer), or its last
// step was a cleanup step (SWI-17, data-model.md WorkerRun).
func cleanupPending(run *pb.WorkerRun) bool {
	return run != nil && (run.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE || run.GetStepRole() == pb.StepRole_STEP_ROLE_CLEANUP)
}

// holdForDeveloper raises a run's hold to NEEDS_DEVELOPER. An existing
// NEEDS_DEVELOPER hold is left as it is. T046 records hold_gen here, on the
// transition into the hold only.
func holdForDeveloper(run *pb.WorkerRun) {
	if run.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		return
	}
	run.Hold = pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
}

// SetTag sets (or clears, when tag is "") a sandbox's mutable purpose tag
// (feature 003, FR-021/022). The tag is trimmed and capped; no other field
// changes, so tagging never affects identity or lifecycle. Persisted via the
// registry's proto marshaling and emitted so the list updates.
func (m *Manager) SetTag(sandboxID, tag string) (*pb.Sandbox, error) {
	tag = strings.TrimSpace(tag)
	if len(tag) > maxTagLen {
		tag = tag[:maxTagLen]
	}
	out, err := m.store.Update(sandboxID, func(s *pb.Sandbox) error {
		s.Tag = tag
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// maxTagLen bounds a sandbox tag (research.md R6).
const maxTagLen = 64

// workspaceMarkerDir/File name the on-disk marker `sxb` walks up to find so it can
// auto-open a sandbox's session from within its workspace copy (feature 003, R6).
const (
	workspaceMarkerDir  = ".switchboard"
	workspaceMarkerFile = "session.json"
)

// writeWorkspaceMarker records {host_id, sandbox_id} under <workspace>/.switchboard/
// session.json. Overwrites any prior marker.
func writeWorkspaceMarker(workspacePath, hostID, sandboxID string) error {
	if workspacePath == "" {
		return nil
	}
	dir := filepath.Join(workspacePath, workspaceMarkerDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	blob, err := json.Marshal(map[string]string{"host_id": hostID, "sandbox_id": sandboxID})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, workspaceMarkerFile), blob, 0o644)
}

// ResolveWorkspace returns the sandbox whose retained workspace copy contains
// path (or is path), matching the deepest workspace so nested subdirectories
// resolve correctly (feature 003, FR-017/018). ok is false when no sandbox owns
// the path.
func (m *Manager) ResolveWorkspace(path string) (*pb.Sandbox, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, err
	}
	all, err := m.store.List()
	if err != nil {
		return nil, false, err
	}
	var best *pb.Sandbox
	var bestLen int
	for _, sb := range all {
		wp := sb.GetWorkspacePath()
		if wp == "" {
			continue
		}
		if within(wp, abs) && len(wp) > bestLen {
			best, bestLen = sb, len(wp)
		}
	}
	if best == nil {
		return nil, false, nil
	}
	return best, true, nil
}

// sandboxNameRe constrains sandbox names: they become a filesystem directory and
// an sbx --name, so only a safe, path-segment-friendly character set is allowed.
var sandboxNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validName reports whether name is usable as a per-host sandbox name.
func validName(name string) error {
	switch {
	case name == "":
		return errors.New("sandbox name cannot be empty")
	case len(name) > 64:
		return errors.New("sandbox name too long (max 64 characters)")
	case !sandboxNameRe.MatchString(name):
		return fmt.Errorf("sandbox name %q must start with a letter or digit and use only letters, digits, '-', '_', '.'", name)
	}
	return nil
}

// nameTaken reports whether another sandbox on this host already uses name.
func (m *Manager) nameTaken(name, exceptID string) (bool, error) {
	all, err := m.store.List()
	if err != nil {
		return false, err
	}
	for _, s := range all {
		if s.GetId() != exceptID && s.GetDisplayName() == name {
			return true, nil
		}
	}
	return false, nil
}

// resolveName picks the sandbox's unique, filesystem-safe name. An explicit name
// must be valid and unused (error otherwise). A name derived from the config
// label is used when valid+free, auto-uniquified on collision, and falls back to
// a short-id default when there is no usable label.
func (m *Manager) resolveName(explicit, label, id string) (string, error) {
	if explicit != "" {
		if err := validName(explicit); err != nil {
			return "", err
		}
		taken, err := m.nameTaken(explicit, id)
		if err != nil {
			return "", err
		}
		if taken {
			return "", fmt.Errorf("a sandbox named %q already exists on this host", explicit)
		}
		return explicit, nil
	}

	fallback := "sandbox-" + id[:8]
	label = strings.TrimSpace(label)
	if label == "" || validName(label) != nil {
		return fallback, nil
	}
	taken, err := m.nameTaken(label, id)
	if err != nil {
		return "", err
	}
	if taken {
		return label + "-" + id[:8], nil
	}
	return label, nil
}

// Rename gives a sandbox a new unique per-host name and moves its retained
// workspace copy to match (FR-012e). The sandbox MUST be stopped: the running
// container's mount is tied to the old path, so the container is released and
// relaunched from the renamed copy on the next start. The uuid id is unchanged.
func (m *Manager) Rename(ctx context.Context, id, newName string) (*pb.Sandbox, error) {
	defer m.lifecycle.begin(id)()
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	newName = strings.TrimSpace(newName)
	if err := validName(newName); err != nil {
		return nil, err
	}
	if newName == sb.GetDisplayName() {
		return sb, nil // no-op
	}
	if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
		return nil, errors.New("stop the sandbox before renaming it")
	}
	taken, err := m.nameTaken(newName, id)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, fmt.Errorf("a sandbox named %q already exists on this host", newName)
	}

	// Release the old container (its --name/mount reference the old path); the
	// retained copy is preserved and relaunched under the new name on next start.
	if sb.GetContainerRef() != "" {
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Destroy(ctx, r) }); err != nil {
			return nil, err
		}
	}
	newPath := filepath.Join(m.workspaceRoot, newName)
	if old := sb.GetWorkspacePath(); old != "" && within(m.workspaceRoot, old) {
		if _, statErr := os.Stat(old); statErr == nil {
			if err := os.Rename(old, newPath); err != nil {
				return nil, err
			}
		}
	}

	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.DisplayName = newName
		s.WorkspacePath = newPath
		s.ContainerRef = "" // recreated from the renamed copy on next start
		leaveRunning(s, pb.SandboxState_SANDBOX_STATE_STOPPED)
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// Readopt is Reconcile without the changed records (FR-002a, SC-012): records
// follow a readable runtime, nothing changes when it cannot be read, and
// records are never deleted. sxbd calls Reconcile itself at startup so it can
// bring up the workers it re-adopts.
func (m *Manager) Readopt(ctx context.Context) error {
	_, err := m.Reconcile(ctx)
	return err
}

// Reconcile reads the runtime once and moves records that disagree with it
// (SWI-13), returning the records it changed (each already announced): it is
// ReconcileRecords followed by Announce.
func (m *Manager) Reconcile(ctx context.Context) ([]*pb.Sandbox, error) {
	changed, err := m.ReconcileRecords(ctx)
	m.Announce(changed)
	return changed, err
}

// ReconcileRecords is Reconcile's write phase: it moves records that disagree
// with the runtime and returns them, without announcing them. A caller that
// must not wait on the change observers (a stop's teardown can take tens of
// seconds) writes here and runs Announce on its own goroutine.
//
//   - When the runtime cannot be read, nothing changes and the error is
//     returned: "couldn't tell" never means "stopped".
//   - RUNNING and STOPPED records follow the listing. So does a transitional
//     record (CREATING, DESTROYING, ERROR) older than this manager: a previous
//     daemon died mid-operation and nothing will finish it.
//   - A record an operation is working on — a held operation latch, a
//     lifecycle operation in flight, a transitional state this daemon wrote, or
//     a write that landed while the runtime was being read — is left to it.
func (m *Manager) ReconcileRecords(ctx context.Context) ([]*pb.Sandbox, error) {
	// The records are read BEFORE the runtime: a write that lands in between
	// changes UpdatedAt, and the write below then skips the record rather than
	// overwrite a newer answer with an older one.
	all, err := m.store.List()
	if err != nil {
		return nil, err
	}
	statuses, err := m.runner.ListStatuses(ctx)
	if err != nil {
		log.Printf("[warn] reconcile: runtime unreadable, keeping last-known state: %v", err)
		return nil, fmt.Errorf("reconcile: runtime unreadable: %w", err)
	}
	var changed []*pb.Sandbox
	for _, sb := range all {
		state := sb.GetState()
		settled := state == pb.SandboxState_SANDBOX_STATE_RUNNING || state == pb.SandboxState_SANDBOX_STATE_STOPPED
		if (!settled && !sb.GetUpdatedAt().AsTime().Before(m.started)) || m.busy(sb.GetId()) {
			continue
		}
		want, unknown := listedState(statuses, sb)
		if unknown != "" {
			m.warnUnknownStatus(sb.GetId(), unknown, state)
			continue
		}
		if state == want {
			continue
		}
		seen := sb.GetUpdatedAt()
		out, err := m.store.Update(sb.GetId(), func(s *pb.Sandbox) error {
			// Re-checked under the registry write: an operation that began after
			// the check above owns the record now.
			if s.GetState() != state || !proto.Equal(s.GetUpdatedAt(), seen) || m.busy(s.GetId()) {
				return errReconcileSkip
			}
			leaveRunning(s, want)
			if want == pb.SandboxState_SANDBOX_STATE_RUNNING {
				s.Error = "" // running again: an old failure no longer applies
			}
			s.UpdatedAt = timestamppb.Now()
			return nil
		})
		switch {
		case errors.Is(err, errReconcileSkip), errors.Is(err, registry.ErrNotFound):
			continue // moved or destroyed meanwhile: an expected race
		case err != nil:
			// This record could not be written: one record never stops the pass
			// over the rest.
			log.Printf("[warn] reconcile: sandbox %s left as %s: %v", sb.GetId(), state, err)
			continue
		}
		log.Printf("[info] reconcile: sandbox %s %s -> %s", sb.GetId(), state, want)
		changed = append(changed, out)
	}
	return changed, nil
}

// Announce emits the records a ReconcileRecords pass changed, which runs the
// change observers (a stop's teardown of terminals, agents and services) and
// publishes them to clients. A worker found running again is brought up as
// after a Restart. A record written again since the pass (an operation, or a
// later pass) is skipped: that write announced itself, and replaying the older
// state could tear down a sandbox that is running again.
func (m *Manager) Announce(changed []*pb.Sandbox) {
	for _, sb := range changed {
		cur, err := m.store.Get(sb.GetId())
		if err != nil || !proto.Equal(cur.GetUpdatedAt(), sb.GetUpdatedAt()) {
			continue
		}
		if cur.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
			// The bring-up is marked before the record is published: the emit is
			// what wakes a local orchestrator's dispatch, which must find the
			// agent start held until the gateway is reloaded (PR #17 run-505).
			// The observer may take minutes; it never holds up the caller.
			if cur.GetController() != nil {
				if fn := m.goBringUp; fn != nil {
					fn(cur)
				} else if fn := m.onBringUp; fn != nil {
					go fn(cur)
				}
			}
			m.emit(cur)
			continue
		}
		m.emit(cur)
		// The teardown ran before the observer published, so a write that
		// landed during it (a restart's RUNNING) was published first and is now
		// behind this pass's state: publish the record as it is now.
		if latest, err := m.store.Get(sb.GetId()); err == nil && !proto.Equal(latest.GetUpdatedAt(), cur.GetUpdatedAt()) {
			m.emit(latest)
		}
	}
}

// warnUnknownStatus logs, once per status word, a sbx status the reconcile
// does not map, so a runtime that reports stopped under another word is seen
// in the log rather than leaving records unchanged silently.
func (m *Manager) warnUnknownStatus(id, status string, state pb.SandboxState) {
	if _, seen := m.unknownStatuses.LoadOrStore(status, true); seen {
		return
	}
	log.Printf("[warn] reconcile: sandbox %s: unknown sbx status %q; leaving it %s (logged once per status)", id, status, state)
}

// busy reports whether an operation is working on the sandbox (SWI-13).
func (m *Manager) busy(id string) bool {
	_, latched := m.ops.holder(id)
	return latched || m.lifecycle.has(id)
}

// listedRunning reports whether the runtime lists the sandbox's container as
// running. An unreadable runtime reads as not running: a teardown then skips
// `sbx exec`, which would start a stopped container again.
func (m *Manager) listedRunning(ctx context.Context, sb *pb.Sandbox) bool {
	statuses, err := m.runner.ListStatuses(ctx)
	if err != nil {
		return false
	}
	state, _ := listedState(statuses, sb)
	return state == pb.SandboxState_SANDBOX_STATE_RUNNING
}

// errReconcileSkip aborts a reconcile write whose record moved under it.
var errReconcileSkip = errors.New("record changed during reconcile")

// listedState is the state a successful listing gives a sandbox: RUNNING when
// any of its handles is listed running, STOPPED when one is listed stopped or
// none is listed (or it has no container at all). unknown names a status sbx
// has but this daemon does not (a transient "starting", say): the record is
// then left as it is rather than read as stopped.
func listedState(statuses map[string]string, sb *pb.Sandbox) (state pb.SandboxState, unknown string) {
	if sb.GetContainerRef() == "" {
		return pb.SandboxState_SANDBOX_STATE_STOPPED, ""
	}
	for _, r := range sbxRefs(sb) {
		status, ok := statuses[r]
		if !ok {
			continue
		}
		switch status {
		case "running":
			return pb.SandboxState_SANDBOX_STATE_RUNNING, ""
		case "stopped":
		default:
			unknown = status
		}
	}
	if unknown != "" {
		return pb.SandboxState_SANDBOX_STATE_UNSPECIFIED, unknown
	}
	return pb.SandboxState_SANDBOX_STATE_STOPPED, ""
}

// RefreshHooks rewrites the agent hook settings of every sandbox that still has
// a workspace, so a daemon upgrade's hook changes reach sandboxes launched by an
// older daemon (effective when their agent next starts; a running Claude Code
// keeps the hooks it started with). Best effort: a failure is reported through
// onLog and never stops the daemon.
func (m *Manager) RefreshHooks(onLog func(string)) {
	if m.injectHooks == nil {
		return
	}
	all, err := m.store.List()
	if err != nil {
		if onLog != nil {
			onLog("warning: hook refresh skipped: " + err.Error())
		}
		return
	}
	for _, sb := range all {
		wp := sb.GetWorkspacePath()
		if wp == "" {
			continue
		}
		if _, err := os.Stat(wp); err != nil {
			continue // the workspace is gone (stopped and wiped); a bring-up re-seeds and re-injects
		}
		if err := m.injectHooks(sb.GetId(), wp); err != nil && onLog != nil {
			onLog("warning: hook refresh for " + sb.GetId() + " failed: " + err.Error())
		}
	}
}

// within reports whether path is inside root (defense against deleting outside
// the controlled folder).
func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) && !startsWithDotDot(rel)
}

func startsWithDotDot(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.'
}
