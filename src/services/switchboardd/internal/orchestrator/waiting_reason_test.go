package orchestrator

import (
	"fmt"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Waiting reasons (FR-315, research R9) over a hand-built index.

func reasonCounts(rec *pb.Orchestrator, states map[string]bool, names map[string]string) *laneCounts {
	c := &laneCounts{rec: rec, live: map[string]int{}, avail: map[string]int{}, names: map[string][]string{}}
	for _, w := range rec.GetWorkers() {
		if w.GetLanesPending() {
			c.pending++
			continue
		}
		keys := w.GetLaneIds()
		if len(keys) == 0 {
			keys = []string{""}
		}
		for _, k := range keys {
			c.live[k]++
			c.names[k] = append(c.names[k], names[w.GetSandboxId()])
			if states[w.GetSandboxId()] {
				c.avail[k]++
			}
		}
	}
	return c
}

func TestWaitingReasons(t *testing.T) {
	rec := &pb.Orchestrator{
		Lanes: []*pb.Lane{{Id: "ln-a", Name: "a"}, {Id: "ln-b", Name: "b"}, {Id: "ln-c", Name: "c"}},
		Actions: []*pb.Action{
			{Name: "excl", Type: pb.ActionType_ACTION_TYPE_PROMPT, LaneIds: []string{"ln-a", "ln-b"}, LaneMode: pb.LaneMode_LANE_MODE_EXCLUSIVE},
			{Name: "incl", Type: pb.ActionType_ACTION_TYPE_PROMPT, LaneIds: []string{"ln-a"}, LaneMode: pb.LaneMode_LANE_MODE_INCLUSIVE},
			{Name: "plain", Type: pb.ActionType_ACTION_TYPE_PROMPT},
			{Name: "in-c", Type: pb.ActionType_ACTION_TYPE_PROMPT, LaneIds: []string{"ln-c"}, LaneMode: pb.LaneMode_LANE_MODE_EXCLUSIVE},
		},
	}
	q := func(action string) *pb.Run {
		return &pb.Run{Action: action, Type: pb.ActionType_ACTION_TYPE_PROMPT, State: pb.RunState_RUN_STATE_QUEUED}
	}
	// No live worker can take it.
	empty := reasonCounts(rec, nil, nil)
	for action, want := range map[string]string{
		"excl":  "waiting for a worker in a, b",
		"incl":  "waiting for a worker in a or an open-lane worker",
		"plain": "waiting for an open-lane worker",
	} {
		if got := waitingReason(q(action), empty, false); got != want {
			t.Errorf("%s = %q, want %q", action, got, want)
		}
	}
	// Every worker that could take it is unavailable: named (≤5, then +N).
	withC := proto2(rec)
	names := map[string]string{}
	for i := range 7 {
		id := fmt.Sprintf("c%d", i)
		withC.Workers = append(withC.Workers, &pb.Worker{SandboxId: id, LaneIds: []string{"ln-c"}})
		names[id] = "worker-" + id
	}
	c := reasonCounts(withC, nil, names)
	if got := waitingReason(q("in-c"), c, false); got != "waiting for worker-c0, worker-c1, worker-c2, worker-c3, worker-c4 +2 more, stopped or unreachable" {
		t.Errorf("unavailable = %q", got)
	}
	if got := waitingReason(q("in-c"), c, true); got != "waiting: every worker that could take it is stopped or unreachable" {
		t.Errorf("agent text = %q", got)
	}
	// An available worker (busy or not) means no reason.
	c = reasonCounts(withC, map[string]bool{"c3": true}, names)
	if got := waitingReason(q("in-c"), c, false); got != "" {
		t.Errorf("available = %q", got)
	}
	// The pending suffix.
	pend := proto2(rec)
	pend.Workers = []*pb.Worker{{SandboxId: "p1", LanesPending: true}}
	if got := waitingReason(q("plain"), reasonCounts(pend, nil, nil), false); got != "waiting for an open-lane worker; 1 worker awaiting lane assignment" {
		t.Errorf("pending 1 = %q", got)
	}
	pend.Workers = append(pend.Workers, &pb.Worker{SandboxId: "p2", LanesPending: true})
	if got := waitingReason(q("plain"), reasonCounts(pend, nil, nil), true); !strings.HasSuffix(got, "; 2 workers awaiting lane assignment") {
		t.Errorf("pending 2 = %q", got)
	}
	// No reason for non-queued, reason-type or setting-less runs.
	running := q("plain")
	running.State = pb.RunState_RUN_STATE_RUNNING
	reason := q("plain")
	reason.Type = pb.ActionType_ACTION_TYPE_REASON
	for name, r := range map[string]*pb.Run{"running": running, "reason": reason, "deleted": q("gone")} {
		if got := waitingReason(r, empty, false); got != "" {
			t.Errorf("%s = %q", name, got)
		}
	}
}

func proto2(rec *pb.Orchestrator) *pb.Orchestrator {
	out := &pb.Orchestrator{Lanes: rec.GetLanes(), Actions: rec.GetActions()}
	return out
}

// A reason read through the service: it names the lanes, the agent's check
// sees the same text, and it is gone once a worker joins the lane (US1
// scenario 6). A detaching worker still counts as live.
func TestWaitingReasonThroughReads(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"frontend": nil})
	if _, err := putAction(env.svc, id, laned("review-pr", pb.LaneMode_LANE_MODE_EXCLUSIVE, ids["frontend"])); err != nil {
		t.Fatal(err)
	}
	setAgent(rt, w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING)
	run := call(t, env, id, "1")
	settle(t, rt)
	list, _ := env.svc.ListRuns(id)
	if got := list.GetQueued()[0].GetWaitingReason(); got != "waiting for a worker in frontend" {
		t.Errorf("ListRuns reason = %q", got)
	}
	out, err := rt.toolCheckRun(checkRunIn{RunID: run.GetId()})
	if err != nil || out.WaitingReason != "waiting for a worker in frontend" {
		t.Errorf("check_run = %+v / %v", out, err)
	}
	seedLanes(t, rt, map[string][]string{"frontend": {"wa"}})
	rt.mu.Lock()
	rt.detaching["wa"] = struct{}{}
	rt.mu.Unlock()
	if got, _ := env.svc.GetRun(id, run.GetId()); got.GetWaitingReason() != "" {
		t.Errorf("after joining = %q", got.GetWaitingReason())
	}
	// Stored runs never carry a reason.
	stored, _ := env.reg.GetRun(id, run.GetId())
	if stored.GetWaitingReason() != "" {
		t.Errorf("stored reason = %q", stored.GetWaitingReason())
	}
	rt.clearDetaching("wa")
}
