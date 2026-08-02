package portforward

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// defaultReadinessTimeout bounds how long a service may take to start listening
	// before it is reported as failing to become ready (research R9). A cold
	// `pnpm dev` on a first run needs considerably more than a few seconds.
	defaultReadinessTimeout = 60 * time.Second
	// stopGracePeriod is how long a process group gets to shut down cleanly after
	// SIGTERM before it is force-killed (clarification Q4, research R6).
	stopGracePeriod = 10 * time.Second
)

// ErrUnknownSandbox is returned when a request names a sandbox this daemon does
// not own.
var ErrUnknownSandbox = errors.New("unknown sandbox")

// ErrServiceNotDeclared is returned when a request names a service absent from the
// sandbox's persisted set. This is the security boundary: only a declared name can
// resolve to something startable (FR-045).
var ErrServiceNotDeclared = errors.New("service not declared by this sandbox's kits")

// SandboxLookup resolves a sandbox record by id (implemented by the sandbox
// Manager). The supervisor needs the workspace path, the container ref, the run
// state, and the resolved service set.
type SandboxLookup interface {
	Get(id string) (*pb.Sandbox, error)
}

// Runner is the subset of the sandbox package's Runner this package needs. It is
// redeclared here rather than imported because the dependency runs the other way:
// internal/sandbox imports this package for Resolve.
type Runner interface {
	PublishPort(ctx context.Context, containerRef string, hostPort, sandboxPort uint32) error
	UnpublishPort(ctx context.Context, containerRef string, hostPort, sandboxPort uint32) error
	Exec(ctx context.Context, containerRef string, argv []string) *exec.Cmd
}

// Emitter is the subset of agent.Hub the supervisor publishes through.
type Emitter interface {
	PublishServiceInstance(inst *pb.ServiceInstance)
	EmitNotification(sandboxID string, kind pb.NotificationKind, message string, now time.Time) *pb.NotificationEvent
}

// procHandle is the supervisor's grip on one running service: the cancel func for
// its context, and (for an in-sandbox service) the process-group id announced by
// the setsid wrapper, which is what lets the tree be killed from outside.
type procHandle struct {
	cancel   context.CancelFunc
	pgid     int
	inSbx    bool
	ref      string // container ref, for the in-sandbox kill and unpublish
	hostPort uint32 // published host port to unpublish on stop (0 => none)
	sbxPort  uint32 // the port the command binds in its own environment
	proc     *runningProc
}

// Supervisor owns the daemon-side lifecycle of every service instance on this
// host: starting, readiness, failure classification, stopping, and the relay's
// view of where a service can be dialled.
type Supervisor struct {
	sandboxes SandboxLookup
	runner    Runner
	insts     *InstanceStore
	emitter   Emitter
	now       func() time.Time

	readinessTimeout time.Duration
	stopGrace        time.Duration

	mu      sync.Mutex
	handles map[string]*procHandle // instance id -> handle
}

// NewSupervisor constructs a Supervisor.
func NewSupervisor(sandboxes SandboxLookup, runner Runner, emitter Emitter) *Supervisor {
	return &Supervisor{
		sandboxes:        sandboxes,
		runner:           runner,
		insts:            NewInstanceStore(),
		emitter:          emitter,
		now:              time.Now,
		readinessTimeout: defaultReadinessTimeout,
		stopGrace:        stopGracePeriod,
		handles:          map[string]*procHandle{},
	}
}

// SetClock overrides the supervisor clock; used in tests for deterministic stamps.
func (s *Supervisor) SetClock(now func() time.Time) { s.now = now }

// SetReadinessTimeout overrides the default readiness window (test hook — avoids a
// 60-second wait).
func (s *Supervisor) SetReadinessTimeout(d time.Duration) { s.readinessTimeout = d }

// SetStopGrace overrides the graceful-shutdown window (test hook).
func (s *Supervisor) SetStopGrace(d time.Duration) { s.stopGrace = d }

// Instances exposes the instance store (for the List RPC and tests).
func (s *Supervisor) Instances() *InstanceStore { return s.insts }

// List returns the sandbox's declared services joined with their current instance
// state (FR-045). The set is exactly Sandbox.services — a service that has never
// been started this session simply carries no instance.
func (s *Supervisor) List(sandboxID string) ([]*pb.SandboxService, error) {
	sb, err := s.sandboxes.Get(sandboxID)
	if err != nil || sb == nil {
		return nil, ErrUnknownSandbox
	}
	out := make([]*pb.SandboxService, 0, len(sb.GetServices()))
	for _, declared := range sb.GetServices() {
		row := &pb.SandboxService{Declared: declared}
		if inst, ok := s.insts.GetByService(sandboxID, declared.GetName()); ok {
			row.Instance = inst
		}
		out = append(out, row)
	}
	return out, nil
}

// transition applies mutate to an instance and publishes the result. It is the
// single choke-point every state change flows through, which is what makes "one
// event per transition, and a notification only for a failure" a testable
// invariant rather than a convention (FR-052).
func (s *Supervisor) transition(id string, mutate func(*pb.ServiceInstance)) *pb.ServiceInstance {
	inst, ok := s.insts.Update(id, mutate)
	if !ok {
		return nil
	}
	s.publish(inst)
	return inst
}

// publish emits exactly one Event.service_instance for the change, plus — and only
// for a service that has entered FAILED — one notification.
//
// Successful starts and developer-initiated stops are deliberately silent: they
// are actions the developer just performed, so echoing them back would train them
// to ignore the inbox, which is exactly where an unattended crash needs to land.
func (s *Supervisor) publish(inst *pb.ServiceInstance) {
	if inst == nil {
		return
	}
	s.emitter.PublishServiceInstance(inst)
	if inst.GetState() == pb.ServiceState_SERVICE_STATE_FAILED {
		s.emitter.EmitNotification(
			inst.GetSandboxId(),
			pb.NotificationKind_NOTIFICATION_KIND_SERVICE_FAILED,
			failureMessage(inst),
			s.now(),
		)
	}
}

// markFailed drives an instance to FAILED with a reason and a human-readable
// detail, releasing its resources first so no port is held by a dead service.
func (s *Supervisor) markFailed(id string, reason pb.ServiceFailureReason, detail string) *pb.ServiceInstance {
	s.release(id)
	return s.transition(id, func(i *pb.ServiceInstance) {
		i.State = pb.ServiceState_SERVICE_STATE_FAILED
		i.FailureReason = reason
		i.FailureDetail = detail
		i.LocalPort = 0
		i.EndedAt = timestamppb.New(s.now())
	})
}

// release drops the supervisor's grip on an instance and gives back the host-side
// resources it held: the published sandbox port, and the handle itself. It is
// idempotent, because both the stop path and the crash path reach it.
//
// The developer-machine port needs no action here — the client owns that listener
// and closes it when the instance leaves RUNNING (research R1).
func (s *Supervisor) release(id string) {
	s.mu.Lock()
	h, ok := s.handles[id]
	delete(s.handles, id)
	s.mu.Unlock()
	if !ok {
		return
	}
	if h.cancel != nil {
		h.cancel()
	}
	if h.hostPort != 0 && h.ref != "" {
		// Best-effort and deliberately not on the caller's context: a cancelled
		// start must still give the port back. Unpublish replays the exact triple
		// that was published.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.runner.UnpublishPort(ctx, h.ref, h.hostPort, h.sbxPort)
	}
}

// handleFor returns the live handle for an instance, if any.
func (s *Supervisor) handleFor(id string) (*procHandle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handles[id]
	return h, ok
}

// failureMessage renders the inbox line for a failed service. It always names the
// sandbox-scoped service and the reason, because the developer reading it is by
// definition looking at something else (FR-052).
func failureMessage(inst *pb.ServiceInstance) string {
	msg := fmt.Sprintf("service %q %s", inst.GetServiceName(), reasonLabel(inst.GetFailureReason()))
	if detail := inst.GetFailureDetail(); detail != "" {
		msg += ": " + firstLine(detail)
	}
	return msg
}

func reasonLabel(r pb.ServiceFailureReason) string {
	switch r {
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED:
		return "could not be launched"
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_PORT_IN_USE:
		return "could not start — port already in use"
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING:
		return "never started listening"
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING_LOOPBACK:
		return "is listening on loopback only"
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_EARLY:
		return "exited before becoming ready"
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_UNEXPECTEDLY:
		return "exited unexpectedly"
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_SANDBOX_NOT_RUNNING:
		return "cannot start while the sandbox is stopped"
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NO_LOCAL_PORT:
		return "could not be assigned a local port"
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_HOST_UNREACHABLE:
		return "is unreachable"
	default:
		return "failed"
	}
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
