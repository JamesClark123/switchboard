package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Guarded delivery (research R7, contracts/orchestrator-mcp.md §Run frame):
// the run is typed into the worker's agent only while the sandbox is RUNNING,
// its agent IDLE, the controller matches and no run is recorded — all checked
// under the registry write that records the run — with the PTY drained into
// the persistent terminal session first so an unattended turn never stalls.
//
// The agent must also have reported ready (SWI-15): Claude Code takes a few
// seconds to start, and a prompt typed before its input box is up is lost, so
// the first run on a fresh worker never started. An agent that is not ready is
// refused without typing — and started if it has no session yet — and the run
// stays queued; the orchestrator delivers it once the agent reports IDLE. A
// live unready agent whose record a hook moved to IDLE is put back to STARTING
// (or NEEDS_INPUT after its start timeout), so it stops reading free.

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
	resp, err := s.DeliverResponse(ctx, req)
	return resp.GetAccepted(), err
}

// DeliverResponse is Deliver answering with the accepted delivery's record
// revision (SWI-18), so the orchestrator's cache can tell the delivery write's
// own record from later ones.
func (s *Service) DeliverResponse(ctx context.Context, req *pb.DeliverWorkerPromptRequest) (*pb.DeliverWorkerPromptResponse, error) {
	var name string
	ready := s.agents == nil || s.agents.Ready(req.GetSandboxId())
	starting, stray := false, false
	var seen pb.AgentStatus // the stray status, when stray
	sb, err := s.reg.Update(req.GetSandboxId(), func(x *pb.Sandbox) error {
		c := x.GetController()
		switch {
		case x.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING:
			return fmt.Errorf("%w: sandbox is %s", ErrPrecondition, strings.ToLower(strings.TrimPrefix(x.GetState().String(), "SANDBOX_STATE_")))
		case c == nil || c.GetOrchestratorId() != req.GetOrchestratorId():
			return fmt.Errorf("%w: sandbox is not a worker of orchestrator %s", ErrPrecondition, req.GetOrchestratorId())
		case c.GetRun() != nil:
			return fmt.Errorf("%w: run %s is already on this sandbox", ErrPrecondition, c.GetRun().GetId())
		case !ready:
			// The record may still read IDLE (no agent ever started, or the
			// daemon restarted): the registry, not the record, knows.
			starting = true
			seen = x.GetAgent().GetStatus()
			stray = seen != pb.AgentStatus_AGENT_STATUS_STARTING && seen != pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
			return fmt.Errorf("%w: agent is starting", ErrPrecondition)
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
			return nil, fmt.Errorf("%w: sandbox %s", ErrNotFound, req.GetSandboxId())
		}
		if starting && s.agents.Live(req.GetSandboxId()) {
			if stray {
				// A hook wrote over the not-ready status; put it back so the
				// worker stops reading free (and mark-free can vouch again).
				// On its own goroutine: the write's observers may dispatch. Only
				// while the record still reads what was seen here.
				go s.agents.ReassertUnready(req.GetSandboxId(), seen)
			}
		} else if starting {
			if serr := s.startAgentErr(req.GetSandboxId()); serr != nil {
				return nil, gateRefusal(serr)
			}
		}
		return nil, err
	}
	// Ensure the terminal broadcaster exists so the PTY is read even with no
	// client attached (research R7), then type the framed prompt.
	if s.terms != nil {
		if _, err := s.terms.Broadcaster(sb.GetId()); err != nil {
			s.logf("[worker] deliver %s: terminal session: %v", sb.GetId(), err)
		}
	}
	if s.agents == nil {
		return nil, s.rollbackRunErr(sb.GetId(), fmt.Errorf("agent registry not configured"))
	}
	if err := s.agents.Prompt(sb.GetId(), sb.GetAgent().GetSpec(), Frame(name, req.GetRunId(), req.GetAction(), req.GetPrompt())); err != nil {
		return nil, s.rollbackRunErr(sb.GetId(), err)
	}
	s.mgr.Emit(sb)
	return &pb.DeliverWorkerPromptResponse{Accepted: true, Revision: sb.GetRevision()}, nil
}

// startAgent starts the sandbox's agent through its persistent terminal
// session (so the PTY is drained from the start, research R7) unless one is
// already live. The agent comes up STARTING and reports IDLE through its
// SessionStart hook. Best-effort: a failure is logged. Nothing starts while a
// bring-up is reloading the sandbox's gateway server, whatever the caller: the
// agent would come up without the orchestrator's tools, and OnBringUp starts it
// once the load is done.
func (s *Service) startAgent(sandboxID, why string) {
	if err := s.startAgentErr(sandboxID); err != nil {
		s.logf("[worker] %s %s: agent not started: %v", why, sandboxID, err)
	}
}

// startAgentErr is startAgent returning why the agent was not started. Every
// spawn goes through the orphan gate (agent.Registry.Session), which refuses
// beside an agent detached by a daemon restart (research R14).
func (s *Service) startAgentErr(sandboxID string) error {
	if s.agents == nil || s.agents.Live(sandboxID) || s.bringingUp(sandboxID) {
		return nil
	}
	if s.terms != nil {
		_, err := s.terms.Broadcaster(sandboxID)
		return err
	}
	sb, err := s.reg.Get(sandboxID)
	if err != nil {
		return nil
	}
	_, err = s.agents.Session(sandboxID, sb.GetAgent().GetSpec())
	return err
}

// gateRefusal is the delivery's refusal when its agent could not be started:
// FAILED_PRECONDITION with the orphan gate's reason (research R14), or
// `agent not started: <err>` for any other failure, so a spawn that keeps
// failing is not read as an agent forever starting.
func gateRefusal(startErr error) error {
	if errors.Is(startErr, agent.ErrOrphan) || errors.Is(startErr, agent.ErrBusy) {
		return fmt.Errorf("%w: %v", ErrPrecondition, startErr)
	}
	return fmt.Errorf("%w: agent not started: %v", ErrPrecondition, startErr)
}

// WarmStart starts a running worker's agent ahead of its first run (SWI-15), so
// the agent is up by the time a run is delivered rather than starting then. It
// is called once a controlled sandbox is RUNNING: at attach, at the end of a
// launch, at bring-up and when the daemon starts. Uncontrolled and non-running
// sandboxes are left alone, and so is a worker with a run in flight: across a
// daemon restart its agent outlives the host-side client (pty.go agentCommand)
// and may still be working — a second agent's SessionStart would read as the
// run going idle.
func (s *Service) WarmStart(sb *pb.Sandbox) {
	if sb.GetController() == nil || sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || sb.GetController().GetRun() != nil {
		return
	}
	s.startAgent(sb.GetId(), "warm start")
}

// rollbackRun clears a run that could not be typed.
func (s *Service) rollbackRunErr(sandboxID string, cause error) error {
	if _, err := s.reg.Update(sandboxID, func(x *pb.Sandbox) error {
		if c := x.GetController(); c != nil {
			c.Run = nil
		}
		return nil
	}); err != nil {
		s.logf("[worker] deliver %s: rollback: %v", sandboxID, err)
	}
	return fmt.Errorf("deliver to %s: %w", sandboxID, cause)
}

// PromptFunc is the prompt-injection signature (agent.Registry.Prompt).
type PromptFunc = func(sandboxID string, spec *pb.AgentSpec, text string) error

// EnsureDrained wraps the agent registry's Prompt so the sandbox's persistent
// terminal session exists before text is typed — the escape hatch's result
// delivery shares the unattended-PTY exposure delivery fixes (research R7).
// terms is resolved at call time because the gRPC server owns the registry.
//
// It starts no agent for a held sandbox (agent.ErrHeld, SWI-17 research R13):
// the broadcaster would spawn one, and a result arriving after the agent
// exited must not start the session that ends the hold. A live agent still
// gets the result.
func EnsureDrained(terms func() *terminal.Registry, agents *agent.Registry) PromptFunc {
	return func(sandboxID string, spec *pb.AgentSpec, text string) error {
		if !agents.Live(sandboxID) && agents.Held(sandboxID) {
			return agent.ErrHeld
		}
		if t := terms(); t != nil {
			_, _ = t.Broadcaster(sandboxID)
		}
		return agents.Prompt(sandboxID, spec, text)
	}
}
