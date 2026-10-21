package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Emitter is the hub surface the service publishes through: the
// orchestrator_changed event after every mutation (R14) and the three run
// notifications (FR-133). *agent.Hub implements it.
type Emitter interface {
	Publish(ev *pb.Event)
	EmitNotification(sandboxID string, kind pb.NotificationKind, message string, now time.Time) *pb.NotificationEvent
}

// Deps wires a Service.
type Deps struct {
	Store  Store
	Hub    Emitter
	HostID string
	// Dial reaches a worker machine over SSH (dial.Batch in production; tests
	// inject an in-process connection, R16).
	Dial dial.Func
	// Local is the worker side of this daemon, served in-process (R2). MAY be
	// nil, in which case the orchestrator's own host is reported unreachable.
	Local worker.API
	// Sandboxes is the daemon's sandbox manager, for the reasoning sandbox (R11).
	// MAY be nil until user story 7 wires it.
	Sandboxes SandboxManager
	Clock     Clock
	// Logf receives diagnostics (log.Printf when nil).
	Logf func(format string, args ...any)
	// DaemonVersion is advertised in the link hello and the worker briefing.
	DaemonVersion string
	// Launch creates the reasoning sandbox on this host (research R11); the
	// gRPC layer supplies one that resolves kits and MCP like LaunchSandbox.
	// nil ⇒ orchestrators report "no reasoning sandbox launcher".
	Launch ReasoningLauncher
}

// SandboxManager is the sandbox.Manager surface the reasoning sandbox uses.
type SandboxManager interface {
	Get(id string) (*pb.Sandbox, error)
	Destroy(ctx context.Context, id string) (bool, error)
}

// Service is the orchestrator side a daemon hosts (research R1): the persisted
// records and one runtime per orchestrator.
type Service struct {
	reg     Store
	hub     Emitter
	hostID  string
	dial    dial.Func
	local   worker.API
	mgr     SandboxManager
	clock   Clock
	logf    func(string, ...any)
	version string
	launch  ReasoningLauncher

	mu       sync.Mutex
	runtimes map[string]*runtime
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewService constructs a Service; it runs nothing until Start.
func NewService(d Deps) *Service {
	s := &Service{
		reg:      d.Store,
		hub:      d.Hub,
		hostID:   d.HostID,
		dial:     d.Dial,
		local:    d.Local,
		mgr:      d.Sandboxes,
		clock:    d.Clock,
		logf:     d.Logf,
		version:  d.DaemonVersion,
		launch:   d.Launch,
		runtimes: map[string]*runtime{},
	}
	if s.clock == nil {
		s.clock = realClock{}
	}
	if s.logf == nil {
		s.logf = log.Printf
	}
	if s.dial == nil {
		s.dial = dial.Batch
	}
	return s
}

// Start loads every record and starts its runtime (FR-105: an orchestrator
// lives and dies with the daemon on its machine).
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil {
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	recs, err := s.reg.ListOrchestrators()
	if err != nil {
		return err
	}
	for _, rec := range recs {
		s.startRuntimeLocked(rec)
	}
	return nil
}

// Stop ends every runtime (links, timers). Records stay.
func (s *Service) Stop() {
	s.mu.Lock()
	rts := make([]*runtime, 0, len(s.runtimes))
	for _, rt := range s.runtimes {
		rts = append(rts, rt)
	}
	cancel := s.cancel
	s.mu.Unlock()
	for _, rt := range rts {
		rt.stop()
	}
	if cancel != nil {
		cancel()
	}
}

// HostID returns the hosting daemon's host id.
func (s *Service) HostID() string { return s.hostID }

func (s *Service) startRuntimeLocked(rec *pb.Orchestrator) *runtime {
	if rt, ok := s.runtimes[rec.GetId()]; ok {
		return rt
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	rt := newRuntime(ctx, s, rec)
	s.runtimes[rec.GetId()] = rt
	rt.start()
	return rt
}

// runtimeFor returns the running runtime for an orchestrator id.
func (s *Service) runtimeFor(id string) (*runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.runtimes[id]
	if !ok {
		return nil, fmt.Errorf("%w: orchestrator %s", ErrNotFound, id)
	}
	return rt, nil
}

// emitChanged publishes Event.orchestrator_changed for an orchestrator (R14).
func (s *Service) emitChanged(id string) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(&pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: s.hostID, OrchestratorId: id}}})
}

// --- queries ---

// List returns every orchestrator this daemon hosts with derived counts (FR-142).
func (s *Service) List() ([]*pb.OrchestratorSummary, error) {
	recs, err := s.reg.ListOrchestrators()
	if err != nil {
		return nil, err
	}
	out := make([]*pb.OrchestratorSummary, 0, len(recs))
	for _, rec := range recs {
		sum, err := s.summary(rec)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, nil
}

// summary derives the counts from the runs bucket and the roster.
func (s *Service) summary(rec *pb.Orchestrator) (*pb.OrchestratorSummary, error) {
	runs, err := s.reg.ListRuns(rec.GetId())
	if err != nil {
		return nil, err
	}
	sum := &pb.OrchestratorSummary{Orchestrator: rec}
	for _, w := range rec.GetWorkers() {
		if !w.GetReasoning() {
			sum.WorkerCount++
		}
	}
	for _, r := range runs {
		switch r.GetState() {
		case pb.RunState_RUN_STATE_QUEUED:
			sum.Queued++
		case pb.RunState_RUN_STATE_RUNNING, pb.RunState_RUN_STATE_AWAITING_INPUT:
			sum.InFlight++
		case pb.RunState_RUN_STATE_AWAITING_REVIEW:
			sum.InFlight++
			sum.AwaitingReview++
		}
	}
	if rt, err := s.runtimeFor(rec.GetId()); err == nil {
		sum.ReasoningReady, sum.ReasoningError = rt.reasoningStatus()
	}
	return sum, nil
}

// Get returns one orchestrator with its workers and host reachability.
func (s *Service) Get(id string) (*pb.OrchestratorDetail, error) {
	rec, err := s.reg.GetOrchestrator(id)
	if err != nil {
		return nil, notFound(err)
	}
	sum, err := s.summary(rec)
	if err != nil {
		return nil, err
	}
	det := &pb.OrchestratorDetail{Summary: sum}
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	det.Workers = rt.workerViews()
	for _, h := range rec.GetHosts() {
		h = proto.Clone(h).(*pb.OrchestratorHost)
		h.Reachable, h.Error = rt.reachability(h.GetHostId())
		det.Hosts = append(det.Hosts, h)
	}
	return det, nil
}

// --- mutations ---

// validName checks an orchestrator name (1–64 characters, no control chars).
func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: orchestrator name is required", ErrInvalid)
	}
	if len(name) > maxNameLen {
		return "", fmt.Errorf("%w: orchestrator name is longer than %d characters", ErrInvalid, maxNameLen)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: orchestrator name contains a control character", ErrInvalid)
		}
	}
	return name, nil
}

// nameTaken reports whether another orchestrator on this daemon has the name.
func (s *Service) nameTaken(name, exceptID string) (bool, error) {
	recs, err := s.reg.ListOrchestrators()
	if err != nil {
		return false, err
	}
	for _, r := range recs {
		if r.GetId() != exceptID && r.GetName() == name {
			return true, nil
		}
	}
	return false, nil
}

// Create records a new orchestrator on this daemon (FR-105, FR-148), starts its
// runtime and kicks off the reasoning sandbox (R11, once user story 7 installs
// the hook).
func (s *Service) Create(name string) (*pb.Orchestrator, error) {
	name, err := validName(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	taken, err := s.nameTaken(name, "")
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, fmt.Errorf("%w: an orchestrator named %q already exists on this host", ErrNameTaken, name)
	}
	rec := &pb.Orchestrator{
		Id:        uuid.NewString(),
		Name:      name,
		HostId:    s.hostID,
		CreatedAt: timestamppb.New(s.clock.Now()),
	}
	if err := s.reg.PutOrchestrator(rec); err != nil {
		return nil, err
	}
	rt := s.startRuntimeLocked(rec)
	s.emitChanged(rec.GetId())
	rt.launchReasoningAsync()
	return proto.Clone(rec).(*pb.Orchestrator), nil
}

// Rename changes the label only (FR-148); workers' last-known names and
// briefings are refreshed through their links as they are reachable.
func (s *Service) Rename(id, name string) (*pb.Orchestrator, error) {
	name, err := validName(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	taken, err := s.nameTaken(name, id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if taken {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: an orchestrator named %q already exists on this host", ErrNameTaken, name)
	}
	rt := s.runtimes[id]
	s.mu.Unlock()
	if rt == nil {
		return nil, fmt.Errorf("%w: orchestrator %s", ErrNotFound, id)
	}
	rec, err := rt.update(func(o *pb.Orchestrator) error {
		o.Name = name
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.emitChanged(id)
	rt.pushBriefings()
	return rec, nil
}

// Remove is DESTRUCTIVE (FR-153): every reachable worker is detached, the
// reasoning sandbox destroyed, in-flight runs failed, queued runs cancelled, and
// the record with its actions, events and history discarded.
func (s *Service) Remove(id string) (*pb.RemoveOrchestratorResponse, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	resp := &pb.RemoveOrchestratorResponse{}
	resp.RunsFailed, resp.RunsCancelled = rt.closeAllRuns("orchestrator removed")
	resp.WorkersDetached = rt.detachAll()
	rt.destroyReasoning()
	rt.stop()
	s.mu.Lock()
	delete(s.runtimes, id)
	s.mu.Unlock()
	if err := s.reg.DeleteOrchestrator(id); err != nil {
		return nil, err
	}
	s.emitChanged(id)
	return resp, nil
}

// SetHosts replaces the orchestrator's host entries (FR-106). Host ids must be
// unique and name a machine other than the orchestrator's own; removing an
// entry whose host still has workers is refused naming them.
func (s *Service) SetHosts(id string, hosts []*pb.OrchestratorHost) (*pb.Orchestrator, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	clean := make([]*pb.OrchestratorHost, 0, len(hosts))
	for _, h := range hosts {
		hid := strings.TrimSpace(h.GetHostId())
		switch {
		case hid == "":
			return nil, fmt.Errorf("%w: a host entry needs a host id", ErrInvalid)
		case hid == s.hostID:
			return nil, fmt.Errorf("%w: %s is this orchestrator's own host and needs no entry", ErrInvalid, hid)
		case seen[hid]:
			return nil, fmt.Errorf("%w: host %s is listed twice", ErrInvalid, hid)
		case strings.TrimSpace(h.GetSshTarget()) == "":
			return nil, fmt.Errorf("%w: host %s needs an ssh target", ErrInvalid, hid)
		}
		seen[hid] = true
		clean = append(clean, &pb.OrchestratorHost{
			HostId: hid, DisplayName: strings.TrimSpace(h.GetDisplayName()),
			SshTarget: strings.TrimSpace(h.GetSshTarget()), SshOptions: append([]string(nil), h.GetSshOptions()...),
		})
	}
	rec, err := rt.update(func(o *pb.Orchestrator) error {
		var stranded []string
		for _, w := range o.GetWorkers() {
			if w.GetHostId() != s.hostID && !seen[w.GetHostId()] {
				stranded = append(stranded, w.GetSandboxId())
			}
		}
		if len(stranded) > 0 {
			return fmt.Errorf("%w: host still has workers %s — detach them first", ErrPrecondition, strings.Join(stranded, ", "))
		}
		o.Hosts = clean
		return nil
	})
	if err != nil {
		return nil, err
	}
	rt.syncLinks()
	s.emitChanged(id)
	return rec, nil
}

// --- workers (user story 2) ---

// Bundle returns what a launching sandbox carries to be a worker from before
// its agent starts (FR-110).
func (s *Service) Bundle(id string) (*pb.WorkerBundle, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	return rt.bundle(), nil
}

// AttachWorker attaches an existing sandbox on hostID through the
// orchestrator's link to that host (FR-107/FR-108/FR-109).
func (s *Service) AttachWorker(ctx context.Context, id, hostID, sandboxID string) (*pb.WorkerView, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	if hostID == "" || sandboxID == "" {
		return nil, fmt.Errorf("%w: host id and sandbox id are required", ErrInvalid)
	}
	return rt.attach(ctx, hostID, sandboxID)
}

// DetachWorker detaches a worker (FR-111); refused while a run is on it and
// for the reasoning sandbox.
func (s *Service) DetachWorker(ctx context.Context, id, sandboxID string) error {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return err
	}
	return rt.detach(ctx, sandboxID)
}

// errTooOld is the message for a worker daemon that predates the worker-side RPCs.
func errTooOld(host string) string {
	return fmt.Sprintf("daemon on %s is too old — update with u", host)
}
