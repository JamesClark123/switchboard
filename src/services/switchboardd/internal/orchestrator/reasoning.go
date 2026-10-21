package orchestrator

import (
	"context"
	"fmt"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// The reasoning sandbox (research R11, FR-145–FR-147): one per orchestrator,
// created on the orchestrator's own host right after Create, attached as its
// `reasoning` worker, rebuilt by destroy + launch, destroyed with the
// orchestrator. The orchestrator package never shells out itself: the gRPC
// layer supplies the launcher (it resolves kits and MCP exactly as
// LaunchSandbox does) and the sandbox manager destroys.

// ReasoningLaunch is what the launcher creates.
type ReasoningLaunch struct {
	DisplayName string
	Setup       *pb.ReasoningSetup
	Controller  *pb.SandboxController
	Briefing    string
}

// ReasoningLauncher creates the reasoning sandbox on this host.
type ReasoningLauncher func(ctx context.Context, req ReasoningLaunch, onLog func(string)) (*pb.Sandbox, error)

// reasoningLostReason is the error (and the failure reason of reason runs)
// after the sandbox vanished out of band.
const reasoningLostReason = "destroyed outside switchboard — rebuild"

type reasoningState struct {
	err       string
	launching bool
	log       []string
}

// reasoningName is the display name: reasoning-<orchestrator8>.
func (rt *runtime) reasoningName() string {
	short := rt.id
	if len(short) > 8 {
		short = short[:8]
	}
	return "reasoning-" + short
}

// launchReasoningAsync kicks the first launch off in the background (Create
// returns before the sandbox exists; it arrives through the events).
func (rt *runtime) launchReasoningAsync() {
	if rt.svc.launch == nil {
		rt.setReasoningErr("no reasoning sandbox launcher on this daemon")
		return
	}
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		ctx, cancel := context.WithTimeout(rt.ctx, reasoningLaunchTimeout)
		defer cancel()
		if _, err := rt.launchReasoning(ctx, rt.record().GetReasoningSetup(), nil); err != nil {
			rt.svc.logf("[orchestrator %s] reasoning sandbox: %v", rt.name(), err)
		}
	}()
}

func (rt *runtime) setReasoningErr(msg string) {
	rt.mu.Lock()
	rt.reasoning.err = msg
	rt.mu.Unlock()
}

// launchReasoning creates the sandbox through the launcher and records it as
// the orchestrator's reasoning worker.
func (rt *runtime) launchReasoning(ctx context.Context, setup *pb.ReasoningSetup, onLog func(string)) (*pb.Sandbox, error) {
	rt.mu.Lock()
	if rt.reasoning.launching {
		rt.mu.Unlock()
		return nil, fmt.Errorf("%w: the reasoning sandbox is already being built", ErrPrecondition)
	}
	rt.reasoning = reasoningState{launching: true}
	rt.mu.Unlock()
	rt.svc.emitChanged(rt.id)
	defer func() {
		rt.mu.Lock()
		rt.reasoning.launching = false
		rt.mu.Unlock()
	}()
	rec := rt.record()
	log := func(line string) {
		rt.mu.Lock()
		rt.reasoning.log = append(rt.reasoning.log, line)
		rt.mu.Unlock()
		if onLog != nil {
			onLog(line)
		}
	}
	sb, err := rt.svc.launch(ctx, ReasoningLaunch{
		DisplayName: rt.reasoningName(),
		Setup:       setup,
		Controller:  &pb.SandboxController{OrchestratorId: rt.id, Name: rec.GetName(), HostId: rt.svc.hostID, Reasoning: true},
		Briefing:    renderBriefing(rec.GetName(), true, rec.GetActions()),
	}, log)
	if err != nil {
		rt.setReasoningErr(err.Error())
		rt.svc.emitChanged(rt.id)
		return nil, err
	}
	rt.reconcileMu.Lock()
	defer rt.reconcileMu.Unlock()
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		o.ReasoningSandboxId = sb.GetId()
		if !rosterHas(o, sb.GetId()) {
			o.Workers = append(o.Workers, &pb.Worker{SandboxId: sb.GetId(), HostId: rt.svc.hostID, AttachedAt: sb.GetController().GetAttachedAt(), Reasoning: true})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	rt.mu.Lock()
	rt.sbs[sb.GetId()] = proto.Clone(sb).(*pb.Sandbox)
	rt.reasoning.err = ""
	rt.mu.Unlock()
	rt.svc.emitChanged(rt.id)
	rt.triggerDispatch()
	return proto.Clone(sb).(*pb.Sandbox), nil
}

// destroyReasoning destroys the current reasoning sandbox (rebuild, Remove)
// and forgets it. The record is cleared first, so the removal event that
// follows is not mistaken for an out-of-band loss.
func (rt *runtime) destroyReasoning() {
	old := rt.record().GetReasoningSandboxId()
	if old == "" {
		return
	}
	_, _ = rt.update(func(o *pb.Orchestrator) error {
		o.ReasoningSandboxId = ""
		kept := o.Workers[:0]
		for _, w := range o.Workers {
			if w.GetSandboxId() != old {
				kept = append(kept, w)
			}
		}
		o.Workers = kept
		return nil
	})
	rt.mu.Lock()
	delete(rt.sbs, old)
	rt.mu.Unlock()
	if rt.svc.mgr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), reasoningDestroyTimeout)
	defer cancel()
	if _, err := rt.svc.mgr.Destroy(ctx, old); err != nil {
		rt.svc.logf("[orchestrator %s] destroy reasoning sandbox %s: %v", rt.name(), old, err)
	}
}

// reasoningLost records that the sandbox vanished out of band: reason runs
// fail with the reason until a rebuild (dispatch has no target).
func (rt *runtime) reasoningLost() {
	_, _ = rt.update(func(o *pb.Orchestrator) error {
		o.ReasoningSandboxId = ""
		return nil
	})
	rt.setReasoningErr(reasoningLostReason)
	rt.svc.emitChanged(rt.id)
	rt.triggerDispatch()
}

// reasoningStatus reports whether the reasoning sandbox exists and, when it
// does not, why ("" while it is still coming up).
func (rt *runtime) reasoningStatus() (bool, string) {
	rec := rt.record()
	rt.mu.Lock()
	st := rt.reasoning
	rt.mu.Unlock()
	if st.launching {
		return false, ""
	}
	if rec.GetReasoningSandboxId() == "" {
		return false, st.err
	}
	return rt.sandboxRecord(rec.GetReasoningSandboxId()) != nil, st.err
}

// RebuildReasoningSandbox saves setup when given, then destroys the current
// reasoning sandbox and launches a new one, streaming the launcher's log
// lines (FR-146). Refused while a run is on it.
func (s *Service) RebuildReasoningSandbox(ctx context.Context, id string, setup *pb.ReasoningSetup, onLog func(string)) (*pb.Sandbox, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	if s.launch == nil {
		return nil, fmt.Errorf("%w: no reasoning sandbox launcher on this daemon", ErrPrecondition)
	}
	rec := rt.record()
	if old := rec.GetReasoningSandboxId(); old != "" {
		if run := rt.runOn(old); run != nil {
			return nil, fmt.Errorf("%w: run %s is on the reasoning sandbox", ErrPrecondition, run.GetId())
		}
		if r := rt.sandboxRecord(old).GetController().GetRun(); r != nil {
			return nil, fmt.Errorf("%w: run %s is on the reasoning sandbox", ErrPrecondition, r.GetId())
		}
	}
	if setup != nil {
		if _, err := rt.update(func(o *pb.Orchestrator) error {
			o.ReasoningSetup = proto.Clone(setup).(*pb.ReasoningSetup)
			return nil
		}); err != nil {
			return nil, err
		}
	} else {
		setup = rec.GetReasoningSetup()
	}
	rt.destroyReasoning()
	if onLog != nil {
		onLog("reasoning: building " + rt.reasoningName())
	}
	return rt.launchReasoning(ctx, setup, onLog)
}
