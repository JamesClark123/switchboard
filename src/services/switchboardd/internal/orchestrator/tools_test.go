package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func runCount(t *testing.T, env *testEnv, orchID string) int {
	t.Helper()
	runs, err := env.reg.ListRuns(orchID)
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

func TestToolCallActionFromWorkers(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, true)

	// A current worker queues a TRIGGERED run with itself as origin (SC-013).
	body, err := rt.runTool("box", "wa", toolCallAction, `{"action":"review-pr","arguments":{"number":"9","repo":"acme/web"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var out callActionOut
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != "queued" || out.Position != 1 || !strings.HasPrefix(out.RunID, "run-") {
		t.Errorf("call_action = %+v", out)
	}
	run, err := env.svc.GetRun(o.GetId(), out.RunID)
	if err != nil || run.GetEventKind() != pb.EventKind_EVENT_KIND_TRIGGERED || run.GetOrigin() != "wa" || run.GetOriginLabel() != "worker-wa" || run.GetArguments()["number"] != "9" {
		t.Errorf("run = %+v / %v", run, err)
	}

	// Strangers, detached sandboxes and the wrong host are refused; nothing queued.
	before := runCount(t, env, o.GetId())
	for _, tc := range [][2]string{{"box", "stranger"}, {"elsewhere", "wa"}, {"box", ""}} {
		if _, err := rt.runTool(tc[0], tc[1], toolCallAction, `{"action":"review-pr","arguments":{"number":"1","repo":"r"}}`); err == nil || err.Error() != "not a worker of orchestrator review-bot" {
			t.Errorf("call from %v = %v", tc, err)
		}
	}
	// Validation errors pass through verbatim; unknown tools and malformed
	// arguments are refused the same way.
	for _, tc := range []struct{ tool, args, want string }{
		{toolCallAction, `{"action":"review-pr","arguments":{"repo":"r"}}`, "argument number is required"},
		{toolCallAction, `{"action":"review-pr","arguments":{"number":"1","repo":"r","extra":"x"}}`, "argument extra is not declared"},
		{toolCallAction, `{"action":"nope","arguments":{}}`, "nope"},
		{"orchestrator_bogus", `{}`, "unknown tool orchestrator_bogus"},
		{toolCallAction, `{`, "invalid arguments"},
	} {
		if _, err := rt.runTool("box", "wb", tc.tool, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s = %v, want %q", tc.tool, tc.args, err, tc.want)
		}
	}
	if got := runCount(t, env, o.GetId()); got != before {
		t.Errorf("refusals queued runs: %d -> %d", before, got)
	}

	// A reason action from a worker is accepted and runs on the reasoning sandbox.
	if _, err := env.svc.PutAction(o.GetId(), &pb.Action{Name: "think", Type: pb.ActionType_ACTION_TYPE_REASON, Body: "Think about {topic}.", Arguments: []*pb.ActionArgument{{Name: "topic", Required: true}}}); err != nil {
		t.Fatal(err)
	}
	body, err = rt.runTool("box", "wb", toolCallAction, `{"action":"think","arguments":{"topic":"x"}}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(body), &out)
	settle(t, rt)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), out.RunID) == pb.RunState_RUN_STATE_RUNNING })
	if got, _ := env.svc.GetRun(o.GetId(), out.RunID); got.GetSandboxId() != "reasoning-1" || got.GetOrigin() != "wb" {
		t.Errorf("reason run = %+v", got)
	}
	if rec := w.get("reasoning-1"); rec.GetController().GetRun().GetId() != out.RunID {
		t.Errorf("reasoning sandbox record = %+v", rec.GetController())
	}

	// handleCall wraps the result frame by id.
	res := rt.handleCall(bg(), "box", &pb.OrchestratorLinkFrame_Call{Id: "7", SandboxId: "stranger", Tool: toolListActions})
	if res.GetId() != "7" || res.GetOk() || !strings.Contains(res.GetError(), "not a worker") {
		t.Errorf("refused frame = %+v", res)
	}
	res = rt.handleCall(bg(), "box", &pb.OrchestratorLinkFrame_Call{Id: "8", SandboxId: "wb", Tool: toolListActions})
	if res.GetId() != "8" || !res.GetOk() || !strings.Contains(res.GetBodyJson(), `"orchestrator":"review-bot"`) {
		t.Errorf("ok frame = %+v", res)
	}
}

func TestToolListCheckAndReport(t *testing.T) {
	env, w, o, rt, run := startOne(t)

	// list_actions: the golden shape of the contract.
	body, err := rt.runTool("box", "wa", toolListActions, "")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"orchestrator":"review-bot","actions":[{"name":"review-pr","description":"","type":"prompt","worker_tag":"","arguments":[{"name":"number","description":"","required":true,"default":""},{"name":"repo","description":"","required":true,"default":""},{"name":"focus","description":"","required":false,"default":""}]}]}`
	if body != want {
		t.Errorf("list_actions =\n%s\nwant\n%s", body, want)
	}

	// check_run on the running run; unknown ids are refused.
	body, err = rt.runTool("box", "wa", toolCheckRun, `{"run_id":"`+run.GetId()+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var cr checkRunOut
	_ = json.Unmarshal([]byte(body), &cr)
	if cr.RunID != run.GetId() || cr.Action != "review-pr" || cr.State != "running" || cr.Summary != "" {
		t.Errorf("check_run = %+v", cr)
	}
	if _, err := rt.runTool("box", "wa", toolCheckRun, `{"run_id":"run-x"}`); err == nil || err.Error() != "unknown run run-x" {
		t.Errorf("unknown run = %v", err)
	}

	// report_completion refusals: no summary, oversized, a stranger, a worker
	// with no run.
	w.put(running("wb", "worker-wb"))
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wb"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ sb, args, want string }{
		{"wa", `{"summary":""}`, "a summary is required"},
		{"wa", `{"summary":"` + strings.Repeat("x", maxSummaryLen+1) + `"}`, "longer than 2000 characters"},
		{"stranger", `{"summary":"ok"}`, "not a worker of orchestrator review-bot"},
		{"wb", `{"summary":"ok"}`, "no run is in flight on this sandbox"},
	} {
		if _, err := rt.runTool("box", tc.sb, toolReportCompletion, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("report from %s %s = %v, want %q", tc.sb, tc.args, err, tc.want)
		}
	}
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_RUNNING {
		t.Fatalf("refusals must not close the run: %v", st)
	}

	// A report from the sandbox the run is on closes it in the same tick
	// (SC-018), with the summary, and frees the worker.
	body, err = rt.runTool("box", "wa", toolReportCompletion, `{"summary":"Left 3 comments"}`)
	if err != nil {
		t.Fatal(err)
	}
	var ro reportOut
	_ = json.Unmarshal([]byte(body), &ro)
	if !ro.OK || ro.RunID != run.GetId() || ro.State != "completed" {
		t.Errorf("report = %+v", ro)
	}
	got, _ := env.svc.GetRun(o.GetId(), run.GetId())
	if got.GetState() != pb.RunState_RUN_STATE_COMPLETED || got.GetSummary() != "Left 3 comments" || got.GetClosedByDeveloper() || got.GetEndedAt() == nil {
		t.Errorf("closed run = %+v", got)
	}
	waitFor(t, func() bool { return w.get("wa").GetController().GetRun() == nil })
	// Once per run: a second report finds nothing in flight.
	if _, err := rt.runTool("box", "wa", toolReportCompletion, `{"summary":"again"}`); err == nil || !strings.Contains(err.Error(), "no run is in flight") {
		t.Errorf("second report = %v", err)
	}
	body, _ = rt.runTool("box", "wa", toolCheckRun, `{"run_id":"`+run.GetId()+`"}`)
	_ = json.Unmarshal([]byte(body), &cr)
	if cr.State != "completed" || cr.Summary != "Left 3 comments" || cr.ClosedByDeveloper {
		t.Errorf("check_run after report = %+v", cr)
	}

	// A run awaiting review closes normally on a late report, and the grace
	// timer is dropped.
	run2 := call(t, env, o.GetId(), "2")
	settle(t, rt)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run2.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	got, _ = env.svc.GetRun(o.GetId(), run2.GetId())
	onSb := got.GetSandboxId()
	push(w, onSb, pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 1 })
	env.clock.Advance(reviewGrace)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run2.GetId()) == pb.RunState_RUN_STATE_AWAITING_REVIEW })
	if _, err := rt.runTool("box", onSb, toolReportCompletion, `{"summary":"late"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ = env.svc.GetRun(o.GetId(), run2.GetId()); got.GetState() != pb.RunState_RUN_STATE_COMPLETED || got.GetSummary() != "late" {
		t.Errorf("late report = %+v", got)
	}
	rt.mu.Lock()
	timers := len(rt.reviewTimers)
	rt.mu.Unlock()
	if timers != 0 {
		t.Errorf("review timers after report = %d", timers)
	}
	waitFor(t, func() bool { return w.get(onSb).GetController().GetRun() == nil })

	// A run awaiting input closes on a report too.
	run3 := call(t, env, o.GetId(), "3")
	settle(t, rt)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run3.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	got, _ = env.svc.GetRun(o.GetId(), run3.GetId())
	push(w, got.GetSandboxId(), pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run3.GetId()) == pb.RunState_RUN_STATE_AWAITING_INPUT })
	if _, err := rt.runTool("box", got.GetSandboxId(), toolReportCompletion, `{"summary":"answered"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ = env.svc.GetRun(o.GetId(), run3.GetId()); got.GetState() != pb.RunState_RUN_STATE_COMPLETED {
		t.Errorf("report while awaiting input = %+v", got)
	}
}
