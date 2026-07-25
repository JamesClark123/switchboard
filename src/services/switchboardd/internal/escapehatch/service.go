package escapehatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ErrCommandNotAvailable is returned when an invocation names a command not on the
// sandbox's persisted allowlist (the security boundary — nothing runs, SC-004).
var ErrCommandNotAvailable = errors.New("escape-hatch command not available")

// ErrUnknownSandbox is returned when an invocation names a sandbox the daemon does
// not own.
var ErrUnknownSandbox = errors.New("unknown sandbox")

// SandboxLookup resolves a sandbox record by id (implemented by the sandbox
// Manager). The service needs the workspace path, the resolved allowlist, and the
// agent spec for the result callback.
type SandboxLookup interface {
	Get(id string) (*pb.Sandbox, error)
}

// Emitter is the subset of agent.Hub the service publishes through.
type Emitter interface {
	PublishEscapeHatchRun(run *pb.EscapeHatchRun)
	EmitNotification(sandboxID string, kind pb.NotificationKind, message string, now time.Time) *pb.NotificationEvent
}

// PromptFunc injects a line into a sandbox's agent (agent.Registry.Prompt). It is
// how a run's outcome is delivered back to the agent, surviving terminal detachment.
type PromptFunc func(sandboxID string, spec *pb.AgentSpec, text string) error

// Service orchestrates escape-hatch invocation, consent, execution, and result
// delivery. It is the daemon-side owner of the run lifecycle (research R1/R5/R6).
// The executor (US2) and consent gate (US4) are wired in by those feature slices.
type Service struct {
	runs      *RunStore
	sandboxes SandboxLookup
	emitter   Emitter
	prompt    PromptFunc
	exec      *Executor
	consent   *ConsentGate
	now       func() time.Time

	mu       sync.Mutex
	seq      int
	inflight map[string]map[int]context.CancelFunc // sandboxID -> token -> cancel
}

// New constructs a Service.
func New(sandboxes SandboxLookup, emitter Emitter, prompt PromptFunc) *Service {
	return &Service{
		runs:      NewRunStore(),
		sandboxes: sandboxes,
		emitter:   emitter,
		prompt:    prompt,
		exec:      NewExecutor(),
		consent:   NewConsentGate(),
		now:       time.Now,
		inflight:  map[string]map[int]context.CancelFunc{},
	}
}

// SetClock overrides the service clock; used in tests for deterministic timestamps.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// SetApprovalWindow overrides the consent window (test hook — avoids a 5-min wait).
func (s *Service) SetApprovalWindow(d time.Duration) { s.consent.SetWindow(d) }

// Invoke starts an escape-hatch run for a named command on a sandbox and returns the
// created run immediately (async — the HTTP handler does not wait for completion,
// research R1). It is the daemon's enforcement point: the name MUST resolve against
// the sandbox's persisted allowlist or nothing runs (SC-004).
func (s *Service) Invoke(sandboxID, name string) (*pb.EscapeHatchRun, error) {
	sb, err := s.sandboxes.Get(sandboxID)
	if err != nil || sb == nil {
		return nil, ErrUnknownSandbox
	}
	cmd, ok := lookupCommand(sb, name)
	if !ok {
		return nil, ErrCommandNotAvailable
	}

	approval := cmd.GetConsentMode() == pb.ConsentMode_CONSENT_MODE_REQUIRES_APPROVAL
	run := &pb.EscapeHatchRun{
		SandboxId:   sandboxID,
		CommandName: name,
		Command:     cmd.GetCommand(),
		Status:      pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING,
	}
	if approval {
		run.Status = pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL
	} else {
		run.StartedAt = timestamppb.New(s.now())
	}
	created := s.runs.Create(run)

	if approval {
		// Register BEFORE announcing so a decision that races in early is not lost.
		s.consent.Register(created.GetId())
	}
	s.publish(created)

	ctx, cancel := context.WithCancel(context.Background())
	token := s.trackCancel(sandboxID, cancel)
	go s.run(ctx, cancel, sandboxID, token, sb.GetWorkspacePath(), created.GetId(), cmd, approval)
	return created, nil
}

// run drives one invocation to a terminal outcome on its own goroutine.
func (s *Service) run(ctx context.Context, cancel context.CancelFunc, sandboxID string, token int, workspacePath, runID string, cmd *pb.EscapeHatchCommand, approval bool) {
	defer cancel()
	defer s.untrackCancel(sandboxID, token)

	if approval {
		if !s.consent.Await(ctx, runID) {
			// Distinguish an explicit denial / elapsed window (DENIED) from a sandbox
			// stop that cancelled the context (CANCELLED).
			status := pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED
			if ctx.Err() != nil {
				status = pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED
			}
			s.finalize(runID, Outcome{Status: status})
			return
		}
		// Approved: transition to RUNNING (execution starts now).
		if running, ok := s.runs.Update(runID, func(r *pb.EscapeHatchRun) {
			r.Status = pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING
			r.StartedAt = timestamppb.New(s.now())
		}); ok {
			s.emitter.PublishEscapeHatchRun(running) // a state change, but not notification-worthy
		}
	}

	outcome := s.exec.Run(ctx, workspacePath, cmd)
	s.finalize(runID, outcome)
}

// finalize records a run's terminal outcome and publishes it (which delivers the
// result back to the agent and emits the RUN_COMPLETE notification).
func (s *Service) finalize(runID string, o Outcome) {
	run, ok := s.runs.Update(runID, func(r *pb.EscapeHatchRun) {
		r.Status = o.Status
		r.ExitStatus = o.ExitCode
		r.Output = o.Output
		r.OutputTruncated = o.Truncated
		r.EndedAt = timestamppb.New(s.now())
	})
	if ok {
		s.publish(run)
	}
}

// Decide applies a developer's approval decision to a pending run (FR-039). It
// returns the run's resolved status and is idempotent on an already-resolved run.
func (s *Service) Decide(runID string, approved bool) pb.EscapeHatchRunStatus {
	s.consent.Decide(runID, approved)
	if run, ok := s.runs.Get(runID); ok {
		return run.GetStatus()
	}
	return pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_UNSPECIFIED
}

// ListRuns returns the session's runs for a sandbox (empty id => all).
func (s *Service) ListRuns(sandboxID string) []*pb.EscapeHatchRun {
	return s.runs.ListBySandbox(sandboxID)
}

// Cancel aborts every in-flight run for a sandbox (called from the non-RUNNING
// teardown hook). A running command is killed (CANCELLED); a pending-approval run is
// resolved CANCELLED. No orphaned host process is left (FR-041).
func (s *Service) Cancel(sandboxID string) {
	s.mu.Lock()
	cancels := s.inflight[sandboxID]
	delete(s.inflight, sandboxID)
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// InFlight reports the number of in-flight runs for a sandbox (test/observability).
func (s *Service) InFlight(sandboxID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inflight[sandboxID])
}

func (s *Service) trackCancel(sandboxID string, cancel context.CancelFunc) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	token := s.seq
	if s.inflight[sandboxID] == nil {
		s.inflight[sandboxID] = map[int]context.CancelFunc{}
	}
	s.inflight[sandboxID][token] = cancel
	return token
}

func (s *Service) untrackCancel(sandboxID string, token int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight[sandboxID], token)
	if len(s.inflight[sandboxID]) == 0 {
		delete(s.inflight, sandboxID)
	}
}

// Runs exposes the run store for the ListEscapeHatchRuns RPC.
func (s *Service) Runs() *RunStore { return s.runs }

// isTerminal reports whether a status is a final outcome (no further transitions).
func isTerminal(st pb.EscapeHatchRunStatus) bool {
	switch st {
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_TIMED_OUT,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED:
		return true
	}
	return false
}

// publish is the single choke-point every run-state change flows through: it emits
// exactly one Event.escape_hatch_run, and — for the two notification-worthy states
// — one NotificationEvent. Terminal states additionally push the outcome back into
// the agent. Keeping this centralized is what makes "one event + the right
// notification per transition" a testable invariant (T012).
func (s *Service) publish(run *pb.EscapeHatchRun) {
	s.emitter.PublishEscapeHatchRun(run)
	switch {
	case run.GetStatus() == pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL:
		s.emitter.EmitNotification(run.GetSandboxId(),
			pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_NEEDS_APPROVAL,
			fmt.Sprintf("escape-hatch %q awaiting approval", run.GetCommandName()), s.now())
	case isTerminal(run.GetStatus()):
		s.emitter.EmitNotification(run.GetSandboxId(),
			pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_RUN_COMPLETE,
			fmt.Sprintf("escape-hatch %q %s", run.GetCommandName(), statusLabel(run.GetStatus())), s.now())
		s.deliver(run)
	}
}

// deliver pushes a terminal run's outcome back into the invoking agent (FR-040,
// SC-005). Best-effort: a delivery failure (no live agent) must not break the run.
func (s *Service) deliver(run *pb.EscapeHatchRun) {
	if s.prompt == nil {
		return
	}
	sb, err := s.sandboxes.Get(run.GetSandboxId())
	if err != nil {
		return
	}
	_ = s.prompt(run.GetSandboxId(), sb.GetAgent().GetSpec(), callbackMessage(run))
}

// callbackMessage renders the injected turn the agent reads on completion.
func callbackMessage(run *pb.EscapeHatchRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[escape-hatch] %q finished: %s", run.GetCommandName(), statusLabel(run.GetStatus()))
	switch run.GetStatus() {
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED:
		fmt.Fprintf(&b, " (exit %d)", run.GetExitStatus())
	}
	b.WriteString(".")
	if out := run.GetOutput(); out != "" {
		b.WriteString("\n--- output")
		if run.GetOutputTruncated() {
			b.WriteString(" (truncated)")
		}
		b.WriteString(" ---\n")
		b.WriteString(out)
	}
	return b.String()
}

// statusLabel is the human-readable form used in notifications and callbacks.
func statusLabel(st pb.EscapeHatchRunStatus) string {
	switch st {
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL:
		return "pending approval"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING:
		return "running"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED:
		return "succeeded"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED:
		return "failed"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_TIMED_OUT:
		return "timed out"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED:
		return "cancelled"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED:
		return "declined"
	default:
		return "unknown"
	}
}

// statusSlug is the lowercase, underscore form used in the HTTP response body.
func statusSlug(st pb.EscapeHatchRunStatus) string {
	switch st {
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL:
		return "pending_approval"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING:
		return "running"
	default:
		return statusLabel(st)
	}
}

// lookupCommand returns the resolved allowlist entry for name, or (nil, false).
// This is the security boundary: only a name already on the sandbox's persisted
// escape_hatch_commands can resolve to something runnable (SC-004).
func lookupCommand(sb *pb.Sandbox, name string) (*pb.EscapeHatchCommand, bool) {
	for _, cmd := range sb.GetEscapeHatchCommands() {
		if cmd.GetName() == name {
			return cmd, true
		}
	}
	return nil, false
}
