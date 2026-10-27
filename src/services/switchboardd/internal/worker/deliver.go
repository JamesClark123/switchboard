package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Guarded delivery (research R7, contracts/orchestrator-mcp.md §Run frame):
// the run is typed into the worker's agent only while the sandbox is RUNNING,
// its agent IDLE, the controller matches and no run is recorded — all checked
// under the registry write that records the run — with the PTY drained into
// the persistent terminal session first so an unattended turn never stalls.

// runFrameHead / runFrameTail wrap the composed prompt (the recorded Run.prompt
// is the composed prompt only; the frame is this constant).
const (
	runFrameTail = "[When this run is finished, call orchestrator_report_completion with a short summary. If the call fails, retry; a failed report does not end the run.]"
)

// Frame renders the delivered text for a run.
func Frame(orchName, runID, action, prompt string) string {
	return fmt.Sprintf("[switchboard orchestrator %q · run %s · action %q]\n\n%s\n\n%s", orchName, runID, action, prompt, runFrameTail)
}

// Deliver records the run on the sandbox and types the framed prompt
// (DeliverWorkerPrompt). FAILED_PRECONDITION (false, ErrPrecondition) when the
// guards fail; a failed prompt rolls the recorded run back and returns its error.
func (s *Service) Deliver(ctx context.Context, req *pb.DeliverWorkerPromptRequest) (bool, error) {
	var name string
	sb, err := s.reg.Update(req.GetSandboxId(), func(x *pb.Sandbox) error {
		c := x.GetController()
		switch {
		case x.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING:
			return fmt.Errorf("%w: sandbox is %s", ErrPrecondition, strings.ToLower(strings.TrimPrefix(x.GetState().String(), "SANDBOX_STATE_")))
		case c == nil || c.GetOrchestratorId() != req.GetOrchestratorId():
			return fmt.Errorf("%w: sandbox is not a worker of orchestrator %s", ErrPrecondition, req.GetOrchestratorId())
		case c.GetRun() != nil:
			return fmt.Errorf("%w: run %s is already on this sandbox", ErrPrecondition, c.GetRun().GetId())
		case x.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE:
			// WAITING (on background work, SWI-12) is refused too: the agent
			// will resume on its own and a new run would interleave with it.
			return fmt.Errorf("%w: agent is %s", ErrPrecondition, strings.ToLower(strings.TrimPrefix(x.GetAgent().GetStatus().String(), "AGENT_STATUS_")))
		}
		name = c.GetName()
		c.Run = &pb.WorkerRun{Id: req.GetRunId(), Action: req.GetAction()}
		x.UpdatedAt = timestamppb.New(s.now())
		return nil
	})
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return false, fmt.Errorf("%w: sandbox %s", ErrNotFound, req.GetSandboxId())
		}
		return false, err
	}
	// Ensure the terminal broadcaster exists so the PTY is read even with no
	// client attached (research R7), then type the framed prompt.
	if s.terms != nil {
		if _, err := s.terms.Broadcaster(sb.GetId()); err != nil {
			s.logf("[worker] deliver %s: terminal session: %v", sb.GetId(), err)
		}
	}
	if s.agents == nil {
		return s.rollbackRun(sb.GetId(), fmt.Errorf("agent registry not configured"))
	}
	if err := s.agents.Prompt(sb.GetId(), sb.GetAgent().GetSpec(), Frame(name, req.GetRunId(), req.GetAction(), req.GetPrompt())); err != nil {
		return s.rollbackRun(sb.GetId(), err)
	}
	s.mgr.Emit(sb)
	return true, nil
}

// rollbackRun clears a run that could not be typed.
func (s *Service) rollbackRun(sandboxID string, cause error) (bool, error) {
	if _, err := s.reg.Update(sandboxID, func(x *pb.Sandbox) error {
		if c := x.GetController(); c != nil {
			c.Run = nil
		}
		return nil
	}); err != nil {
		s.logf("[worker] deliver %s: rollback: %v", sandboxID, err)
	}
	return false, fmt.Errorf("deliver to %s: %w", sandboxID, cause)
}

// PromptFunc is the prompt-injection signature (agent.Registry.Prompt).
type PromptFunc = func(sandboxID string, spec *pb.AgentSpec, text string) error

// EnsureDrained wraps a prompt function so the sandbox's persistent terminal
// session exists before text is typed — the escape hatch's result delivery
// shares the unattended-PTY exposure delivery fixes (research R7). terms is
// resolved at call time because the gRPC server owns the registry.
func EnsureDrained(terms func() *terminal.Registry, prompt PromptFunc) PromptFunc {
	return func(sandboxID string, spec *pb.AgentSpec, text string) error {
		if t := terms(); t != nil {
			_, _ = t.Broadcaster(sandboxID)
		}
		return prompt(sandboxID, spec, text)
	}
}
