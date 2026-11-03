package grpc

import (
	"context"
	"errors"
	"sync"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// replaceConcurrency bounds how many detached agents a bulk Replace stops at a
// time: each reap is one `sbx exec` of up to orphanKillWait + a probe.
const replaceConcurrency = 4

// CapabilityReplaceDetached is the DaemonInfo capability that advertises
// ReplaceDetachedAgent (SWI-25 A1), so the TUI offers R only against a daemon
// that has it.
const CapabilityReplaceDetached = "replace-detached"

// ReplaceDetachedAgent is the developer's bulk Replace (SWI-25 A1; `R` on the
// Workers view): for each named sandbox, or every idle detached one on this
// host, the detached agent is stopped (sandbox.Manager.ReapOrphan, the same
// stop as `t` → Replace) and a fresh agent started the way AttachAgent's
// replace path does, through the terminal registry. Each sandbox gets an
// outcome; a refusal or failure on one stops no other. A worker with a run is
// skipped (the run would fail: that Replace stays a per-sandbox decision), and
// so is a sandbox whose daemon has a live agent session.
func (s *Server) ReplaceDetachedAgent(ctx context.Context, req *pb.ReplaceDetachedAgentRequest) (*pb.ReplaceDetachedAgentResponse, error) {
	if s.mgr == nil {
		return nil, status.Error(codes.Unavailable, "sandbox manager not configured")
	}
	ids := req.GetSandboxIds()
	if len(ids) == 0 && req.GetAllIdle() {
		ids = s.mgr.IdleDetached()
	}
	seen := map[string]bool{}
	var todo []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			todo = append(todo, id)
		}
	}
	out := make([]*pb.ReplaceDetachedAgentResponse_Outcome, len(todo))
	// Each reap is the developer's stop of an agent, TERM then KILL, bounded by
	// its own timeout (ReapOrphan): a client that gives up mid-batch must not cut
	// one short, nor fail the ones still queued (as SaveMcpServer does).
	rctx := context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	sem := make(chan struct{}, replaceConcurrency)
	for i, id := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = s.replaceDetached(rctx, id)
		}()
	}
	wg.Wait()
	return &pb.ReplaceDetachedAgentResponse{Outcomes: out}, nil
}

// replaceDetached replaces one sandbox's detached agent, reporting the outcome.
func (s *Server) replaceDetached(ctx context.Context, id string) *pb.ReplaceDetachedAgentResponse_Outcome {
	o := &pb.ReplaceDetachedAgentResponse_Outcome{SandboxId: id}
	sb, err := s.mgr.Get(id)
	if err != nil {
		o.Result, o.Error = pb.ReplaceDetachedAgentResponse_RESULT_ERROR, err.Error()
		return o
	}
	o.DisplayName = sb.GetDisplayName()
	if sb.GetAgent().GetAgentCheck().GetState() != pb.AgentCheck_STATE_DETACHED {
		o.Result, o.Error = pb.ReplaceDetachedAgentResponse_RESULT_SKIPPED_NOT_DETACHED, "no detached agent"
		return o
	}
	if run := sb.GetController().GetRun(); run != nil {
		o.Result, o.Error = pb.ReplaceDetachedAgentResponse_RESULT_SKIPPED_RUN, "holds run "+run.GetId()+"; t → Replace decides it"
		return o
	}
	switch err := s.mgr.ReapOrphan(ctx, id, sandbox.ReapReplace); {
	case err == nil:
	case errors.Is(err, sandbox.ErrNothingToStop):
		// A probe cleared the sandbox since the check above: the daemon's own
		// agent (or none) is what runs there, and nothing was replaced.
		o.Result, o.Error = pb.ReplaceDetachedAgentResponse_RESULT_SKIPPED_NOT_DETACHED, "the check cleared meanwhile"
		return o
	case errors.Is(err, agent.ErrBusy):
		o.Result, o.Error = pb.ReplaceDetachedAgentResponse_RESULT_SKIPPED_BUSY, err.Error()
		return o
	case errors.Is(err, sandbox.ErrLiveSession):
		o.Result, o.Error = pb.ReplaceDetachedAgentResponse_RESULT_SKIPPED_LIVE_SESSION, err.Error()
		return o
	default:
		o.Result, o.Error = pb.ReplaceDetachedAgentResponse_RESULT_ERROR, err.Error()
		return o
	}
	if err := s.startAgent(id); err != nil {
		o.Result, o.Error = pb.ReplaceDetachedAgentResponse_RESULT_ERROR, "the detached agent was stopped, but no fresh one started: "+err.Error()
		return o
	}
	o.Result = pb.ReplaceDetachedAgentResponse_RESULT_REPLACED
	return o
}

// startAgent starts a sandbox's agent through the worker side
// (worker.Service.StartAgent: the terminal registry's broadcaster spawns the
// session and drains its output with no client attached, and a sandbox mid
// bring-up is left to that bring-up, so a worker's fresh agent has its gateway
// tools). A server without a worker side (tests) spawns through the agent
// registry alone.
func (s *Server) startAgent(id string) error {
	switch {
	case s.worker != nil:
		return s.worker.StartAgent(id)
	case s.agents != nil:
		_, err := s.agents.Session(id, s.agentSpecFor(id))
		return err
	}
	return errors.New("no agent registry")
}
