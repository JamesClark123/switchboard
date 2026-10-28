package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SWI-17: a run's sequence on its row, the R/S/X controls of a halted step and
// the steps in the prompt overlay (contracts/jobs-ui.md).

// seqSteps is the default sequence plus one custom pre- and post-prompt:
// Model, Effort, pre, action, post, Clear (indexes 0..5).
func seqSteps() []*pb.RunStep {
	return []*pb.RunStep{
		{Role: pb.StepRole_STEP_ROLE_SETUP, Kind: pb.StepKind_STEP_KIND_MODEL, Text: "/model opus", Expect: "Opus"},
		{Role: pb.StepRole_STEP_ROLE_SETUP, Kind: pb.StepKind_STEP_KIND_EFFORT, Text: "/effort medium", Expect: "medium"},
		{Role: pb.StepRole_STEP_ROLE_SETUP, Kind: pb.StepKind_STEP_KIND_CUSTOM, Text: "/pre-setupskill"},
		{Role: pb.StepRole_STEP_ROLE_ACTION, Kind: pb.StepKind_STEP_KIND_ACTION_PROMPT, Text: "Review 412"},
		{Role: pb.StepRole_STEP_ROLE_CLEANUP, Kind: pb.StepKind_STEP_KIND_CUSTOM, Text: "/tear-down-skill"},
		{Role: pb.StepRole_STEP_ROLE_CLEANUP, Kind: pb.StepKind_STEP_KIND_CLEAR, Text: "/clear"},
	}
}

// seqRun is a run of review-pr on w-run at step i with status st.
func seqRun(id string, state pb.RunState, step int32, st pb.StepStatus) *pb.Run {
	return &pb.Run{Id: id, Action: "review-pr", State: state, SandboxId: "w-run", SandboxHostId: "test-host",
		StartedAt: at(-time.Minute), Steps: seqSteps(), Step: step, StepStatus: st, Prompt: "Review 412"}
}

func halted(r *pb.Run, note string) *pb.Run {
	r.StepStatus, r.StepNote = pb.StepStatus_STEP_STATUS_HALTED, note
	return r
}

// stepJobs opens Jobs (Overview) on one daemon with the given in-flight runs,
// worker w-run listed and running.
func stepJobs(t *testing.T, inFlight ...*pb.Run) (Model, *fakeDaemon) {
	t.Helper()
	m, d := jobsWith(t, jobsOverview, []*pb.Action{reviewAction()}, &pb.ListRunsResponse{InFlight: inFlight})
	m = withSandboxes(m, []*pb.Sandbox{runningWorker("w-run")})
	m.screen = screenJobs
	return m, d
}

func plainView(m Model) string { return ansi.Strip(m.View()) }

func TestRunRowsShowTheStep(t *testing.T) {
	closedHold := func(id string, hold pb.WorkerHold) *pb.Run {
		r := seqRun(id, pb.RunState_RUN_STATE_COMPLETED, 4, pb.StepStatus_STEP_STATUS_PENDING)
		r.Hold, r.EndedAt, r.Summary = hold, at(-time.Second), "left 2 comments"
		return r
	}
	restart := halted(seqRun("run-rst", pb.RunState_RUN_STATE_RUNNING, 2, 0), "")
	restart.StepSentBeforeRestart = true
	unconfirmed := halted(seqRun("run-unc", pb.RunState_RUN_STATE_RUNNING, 2, 0), "")
	unconfirmed.StepUnconfirmed = true
	waiting := seqRun("run-wait", pb.RunState_RUN_STATE_RUNNING, 1, pb.StepStatus_STEP_STATUS_PENDING)
	waiting.StepNote = "waiting: input box not empty"
	clearHalt := halted(closedHold("run-clr", pb.WorkerHold_WORKER_HOLD_CLEANING_UP), "picker opened")
	clearHalt.Step = 5
	cases := map[*pb.Run]string{
		seqRun("run-set", pb.RunState_RUN_STATE_RUNNING, 1, pb.StepStatus_STEP_STATUS_SENT):       "setting up 2/3 on worker-w-run",
		seqRun("run-act", pb.RunState_RUN_STATE_RUNNING, 3, pb.StepStatus_STEP_STATUS_TURN):       "running         alpha-bot @ test-host     on worker-w-run",
		closedHold("run-cln", pb.WorkerHold_WORKER_HOLD_CLEANING_UP):                              "cleaning up 1/2 on worker-w-run — left 2 comments",
		closedHold("run-dev", pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER):                          "needing the developer on worker-w-run",
		closedHold("run-frg", pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING):                      "needs input (freeing) on worker-w-run",
		halted(seqRun("run-bog", pb.RunState_RUN_STATE_RUNNING, 0, 0), "Model 'bogus' not found"): "setting up 1/3 not accepted: Model 'bogus' not found",
		restart:     "setting up 3/3 not accepted (sent before a restart)",
		unconfirmed: "setting up 3/3 not accepted (may have run)",
		waiting:     "setting up 2/3 waiting: input box not empty",
		clearHalt:   "Clear not accepted: picker opened",
	}
	var runs []*pb.Run
	for r := range cases {
		runs = append(runs, r)
	}
	m, _ := stepJobs(t, runs...)
	m, _ = update(m, tea.WindowSizeMsg{Width: 200, Height: 60})
	v := plainView(m)
	for r, want := range cases {
		if !strings.Contains(v, want) {
			t.Errorf("%s: row missing %q:\n%s", r.GetId(), want, v)
		}
	}
	// Held and halted closed runs are listed once, under In progress.
	if n := strings.Count(v, "run-") + strings.Count(v, "completed"); n == 0 {
		t.Fatal("no rows")
	}
	inProgress := 0
	for _, it := range m.overviewItems() {
		if it.run != nil && it.section == sectionInProgress {
			inProgress++
		}
		if it.run != nil && it.section == sectionHistory {
			t.Errorf("%s listed under History", it.run.run.GetId())
		}
	}
	if inProgress != len(cases) {
		t.Errorf("in progress rows = %d, want %d", inProgress, len(cases))
	}
	// The progress follows the run's state: none on a run awaiting review (it
	// is at its action step), setup only while open, cleanup only once closed.
	for _, r := range []*pb.Run{
		seqRun("x", pb.RunState_RUN_STATE_AWAITING_REVIEW, 0, pb.StepStatus_STEP_STATUS_SENT),
		seqRun("x", pb.RunState_RUN_STATE_COMPLETED, 1, pb.StepStatus_STEP_STATUS_SENT),
		seqRun("x", pb.RunState_RUN_STATE_RUNNING, 4, pb.StepStatus_STEP_STATUS_PENDING),
	} {
		if d := runStepDetail(r); d != "" {
			t.Errorf("%v at step %d: detail %q", r.GetState(), r.GetStep(), d)
		}
	}
	// A run without steps, or with no step detail, renders as before.
	if d := runStepDetail(&pb.Run{}); d != "" {
		t.Errorf("no steps: %q", d)
	}
	if currentStep(&pb.Run{Steps: seqSteps(), Step: 9}) != nil || stepName(&pb.Run{}) != "step" || stepProgress(&pb.Run{}) != "" {
		t.Error("an index out of range has no step")
	}
}

// openRunOfEvent skips a closed run still listed in flight for its hold, so its
// timed event is not "already open".
func TestOpenRunOfEventSkipsClosedHolds(t *testing.T) {
	held := seqRun("run-h", pb.RunState_RUN_STATE_COMPLETED, 5, pb.StepStatus_STEP_STATUS_PENDING)
	held.Hold, held.EventKind, held.Origin = pb.WorkerHold_WORKER_HOLD_CLEANING_UP, pb.EventKind_EVENT_KIND_TIMED, "tev-1"
	open := &pb.Run{Id: "run-o", State: pb.RunState_RUN_STATE_RUNNING, EventKind: pb.EventKind_EVENT_KIND_TIMED, Origin: "tev-1"}
	if got := openRunOfEvent(&pb.ListRunsResponse{InFlight: []*pb.Run{held}}, "tev-1"); got != nil {
		t.Errorf("a holding closed run is not open: %v", got.GetId())
	}
	if got := openRunOfEvent(&pb.ListRunsResponse{InFlight: []*pb.Run{held, open}}, "tev-1"); got.GetId() != "run-o" {
		t.Errorf("open run = %v", got.GetId())
	}
}

func TestStepKeysHelpAndAvailability(t *testing.T) {
	plain := halted(seqRun("run-p", pb.RunState_RUN_STATE_RUNNING, 2, 0), "picker opened")
	act := halted(seqRun("run-a", pb.RunState_RUN_STATE_RUNNING, 3, 0), "text still in the input box")
	clr := halted(seqRun("run-c", pb.RunState_RUN_STATE_COMPLETED, 5, 0), "picker opened")
	clr.Hold = pb.WorkerHold_WORKER_HOLD_CLEANING_UP
	before := halted(seqRun("run-b", pb.RunState_RUN_STATE_RUNNING, 2, 0), "")
	before.StepSentBeforeRestart = true
	detached := halted(seqRun("run-d", pb.RunState_RUN_STATE_RUNNING, 1, 0), "agent detached by a daemon restart")
	heldClear := seqRun("run-hc", pb.RunState_RUN_STATE_FAILED, 5, pb.StepStatus_STEP_STATUS_DONE)
	heldClear.Hold = pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
	running := seqRun("run-r", pb.RunState_RUN_STATE_RUNNING, 1, pb.StepStatus_STEP_STATUS_SENT)
	m := Model{}
	for _, c := range []struct {
		run  *pb.Run
		want string
	}{
		{plain, "R retry step • S skip step • X fail run"},
		{act, "R retry step • X fail run"},
		{clr, "R retry step • X fail run"},
		{before, "S skip step • R retry step • X fail run"},
		{detached, "R restart setup • X fail run"},
		{heldClear, "R retry Clear"},
	} {
		h := strings.ReplaceAll(ansi.Strip(m.renderHelp(stepKeysHelp(c.run))), "\n", " • ")
		if !strings.Contains(h, c.want) {
			t.Errorf("%s: help %q, want %q", c.run.GetId(), h, c.want)
		}
	}
	if len(stepKeysHelp(running)) != 0 || strings.Contains(ansi.Strip(m.renderHelp(runRowHelp(running))), "X") {
		t.Error("R/S/X are not offered on a run that is not halted")
	}
	if h := ansi.Strip(m.renderHelp(runRowHelp(heldClear))); !strings.Contains(h, "t terminal") {
		t.Errorf("a closed run holding its worker offers its terminal: %q", h)
	}
}

func TestStepKeysConfirmDefaultNoAndCall(t *testing.T) {
	run := halted(seqRun("run-p", pb.RunState_RUN_STATE_RUNNING, 2, 0), "picker opened")
	for _, c := range []struct {
		key, title, call string
	}{
		{"R", "Retry setting up 3/3 of run-p?", "ResolveRunStep o-a run-p 2 STEP_RESOLUTION_RETRY"},
		{"S", "Skip setting up 3/3 of run-p?", "ResolveRunStep o-a run-p 2 STEP_RESOLUTION_SKIP"},
		{"X", "Fail run-p at setting up 3/3?", "ResolveRunStep o-a run-p 2 STEP_RESOLUTION_FAIL"},
	} {
		m, d := stepJobs(t, run)
		m = overviewAt(t, m, "run-p")
		m, _ = update(m, press(c.key))
		if m.screen != screenConfirm || !strings.Contains(plainView(m), c.title) {
			t.Fatalf("%s: confirm %v:\n%s", c.key, m.screen, plainView(m))
		}
		if h := ansi.Strip(m.renderHelp(m.confirmHelp())); !strings.Contains(h, "n/enter/esc") {
			t.Errorf("%s: enter answers No: %q", c.key, h)
		}
		// enter is No: nothing is sent.
		mm, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || mm.screen != screenJobs || strings.Contains(d.orchCallsJoined(), "ResolveRunStep") {
			t.Fatalf("%s: enter should cancel", c.key)
		}
		m, cmd = update(m, press("y"))
		m = feed2(m, cmd)
		if !strings.Contains(d.orchCallsJoined(), c.call) || !strings.Contains(m.status, "requested for run-p") {
			t.Errorf("%s: calls %q status %q", c.key, d.orchCallsJoined(), m.status)
		}
	}
}

func TestStepKeysRefusalsAndWarnings(t *testing.T) {
	act := halted(seqRun("run-a", pb.RunState_RUN_STATE_RUNNING, 3, 0), "")
	clr := halted(seqRun("run-c", pb.RunState_RUN_STATE_COMPLETED, 5, 0), "")
	clr.Hold, clr.EndedAt = pb.WorkerHold_WORKER_HOLD_CLEANING_UP, at(0)
	unc := halted(seqRun("run-u", pb.RunState_RUN_STATE_RUNNING, 2, 0), "")
	unc.StepUnconfirmed = true
	det := halted(seqRun("run-d", pb.RunState_RUN_STATE_RUNNING, 1, 0), "agent detached by a daemon restart")
	running := seqRun("run-r", pb.RunState_RUN_STATE_RUNNING, 1, pb.StepStatus_STEP_STATUS_SENT)
	cln := halted(seqRun("run-x", pb.RunState_RUN_STATE_COMPLETED, 4, 0), "")
	cln.Hold, cln.EndedAt = pb.WorkerHold_WORKER_HOLD_CLEANING_UP, at(0)
	m, d := stepJobs(t, act, clr, unc, det, running, cln)
	m.sandboxes[0].Agent = &pb.AgentSession{AgentCheck: &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED, LastStatus: pb.AgentStatus_AGENT_STATUS_WORKING}}
	refused := func(id, key, want string) {
		t.Helper()
		mm := overviewAt(t, m, id)
		mm.jobs.status = ""
		mm, cmd := update(mm, press(key))
		if cmd != nil || mm.screen != screenJobs || !strings.Contains(mm.jobs.status, want) {
			t.Errorf("%s %s: screen %v status %q, want %q", id, key, mm.screen, mm.jobs.status, want)
		}
	}
	refused("run-a", "S", "action prompt can't be skipped")
	refused("run-c", "S", "Clear can't be skipped")
	refused("run-d", "S", "R restarts setup")
	refused("run-r", "X", "R, S and X are for a run halted at a step")
	confirm := func(id, key string, want ...string) {
		t.Helper()
		mm := overviewAt(t, m, id)
		mm, _ = update(mm, press(key))
		v := plainView(mm)
		for _, w := range want {
			if !strings.Contains(v, w) {
				t.Errorf("%s %s: confirm missing %q:\n%s", id, key, w, v)
			}
		}
	}
	confirm("run-u", "R", "may already have run", "Retry anyway?")
	confirm("run-d", "R", "Restart setup of run-d?", "last seen working", "Anything it was doing is lost.")
	confirm("run-c", "X", "left needing the developer")
	confirm("run-x", "X", "remaining post-prompts are dropped")
	confirm("run-u", "X", "No post-prompt follows")
	// FAILED_PRECONDITION: the run moved on — shown, and the view reloads.
	d.orchErr = status.Error(codes.FailedPrecondition, "the run moved on; reload")
	mm := overviewAt(t, m, "run-u")
	mm, _ = update(mm, press("S"))
	mm, cmd := update(mm, press("y"))
	before := strings.Count(d.orchCallsJoined(), "ListRuns")
	msg := runCmd(cmd).(orchDoneMsg)
	if !msg.reload || !strings.Contains(msg.status, "the run moved on; reload") {
		t.Errorf("precondition: %+v", msg)
	}
	d.orchErr = nil
	mm = feed2(mm, func() tea.Msg { return msg })
	if !strings.Contains(mm.status, "the run moved on") || strings.Count(d.orchCallsJoined(), "ListRuns") <= before {
		t.Errorf("status %q, reloaded %v", mm.status, strings.Count(d.orchCallsJoined(), "ListRuns") > before)
	}
	// Any other error is a plain refusal.
	d.orchErr = status.Error(codes.Unavailable, "down")
	mm = overviewAt(t, m, "run-u")
	mm, _ = update(mm, press("S"))
	_, cmd = update(mm, press("y"))
	if msg := runCmd(cmd).(orchDoneMsg); msg.err == nil {
		t.Error("an unavailable daemon is an error")
	}
}

// t on a closed run that still holds its worker opens the worker's terminal.
func TestTerminalOnHoldingRun(t *testing.T) {
	held := seqRun("run-h", pb.RunState_RUN_STATE_COMPLETED, 5, pb.StepStatus_STEP_STATUS_PENDING)
	held.Hold, held.EndedAt = pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, at(0)
	m, d := stepJobs(t, held)
	m = overviewAt(t, m, "run-h")
	m, cmd := update(m, press("t"))
	opened, ok := runCmd(cmd).(termOpenedMsg)
	if !ok {
		t.Fatalf("t should attach: status %q", m.jobs.status)
	}
	m, _ = update(m, opened)
	if m.screen != screenTerminal || d.attachedID != "w-run" {
		t.Errorf("screen %v attached %q", m.screen, d.attachedID)
	}
}

func TestPromptOverlayListsSteps(t *testing.T) {
	m, _ := stepJobs(t, seqRun("run-s", pb.RunState_RUN_STATE_RUNNING, 2, pb.StepStatus_STEP_STATUS_SENT))
	m = overviewAt(t, m, "run-s")
	m, cmd := update(m, press("enter"))
	m = feed(m, cmd)
	v := plainView(m)
	for _, want := range []string{"pre     ✓ /model opus", "✓ /effort medium", "▶ /pre-setupskill", "action", "(composed prompt, below)", "post", "/tear-down-skill", "/clear", "Review 412"} {
		if !strings.Contains(v, want) {
			t.Errorf("overlay missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "✓ /tear-down-skill") || strings.Contains(v, "▶ /clear") {
		t.Error("later steps are unmarked")
	}
	// Done: every step ticked. Queued: none marked. No steps: the prompt alone.
	done := seqRun("d", pb.RunState_RUN_STATE_COMPLETED, 5, pb.StepStatus_STEP_STATUS_DONE)
	if v := ansi.Strip(m.promptView(done)); !strings.Contains(v, "✓ /clear") {
		t.Errorf("done:\n%s", v)
	}
	// A Clear failed with X (held for the developer) is not ticked.
	held := seqRun("h", pb.RunState_RUN_STATE_COMPLETED, 5, pb.StepStatus_STEP_STATUS_DONE)
	held.Hold = pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
	if v := ansi.Strip(m.promptView(held)); !strings.Contains(v, "✗ /clear") || !strings.Contains(v, "✓ /tear-down-skill") {
		t.Errorf("held Clear:\n%s", v)
	}
	q := seqRun("q", pb.RunState_RUN_STATE_QUEUED, 0, 0)
	if v := ansi.Strip(m.promptView(q)); strings.Contains(v, "✓") || strings.Contains(v, "▶") {
		t.Errorf("queued:\n%s", v)
	}
	if v := ansi.Strip(m.promptView(&pb.Run{Id: "p", Prompt: "just the prompt"})); strings.Contains(v, "pre") || !strings.Contains(v, "just the prompt") {
		t.Errorf("no steps:\n%s", v)
	}
}

func TestLongStepDetailLeavesTheOrigin(t *testing.T) {
	r := seqRun("run-w", pb.RunState_RUN_STATE_RUNNING, 1, pb.StepStatus_STEP_STATUS_PENDING)
	r.StepNote = "waiting: input box not empty after a very long while indeed"
	r.EventKind, r.OriginLabel = pb.EventKind_EVENT_KIND_TRIGGERED, "review-bot-worker"
	m, _ := stepJobs(t, r)
	m.width = 120
	line := ansi.Strip(m.jobsRunLine(jobsRunRow{run: r, target: orchTarget{}}))
	if !strings.Contains(line, "triggered by review-b") || !strings.Contains(line, "setting up 2/3") {
		t.Fatalf("the origin should keep its first columns beside a long detail:\n%s", line)
	}
}

// FR-216: m on a worker whose closed run is cleaning up while its agent asks
// for input frees it directly, with no outcome dialog (the run is already
// closed and keeps its outcome); a cleanup that isn't asking is refused, as is
// a worker held for the developer.
func TestMarkFreeCleaningUpWorker(t *testing.T) {
	m, a, b := attachEnv(t)
	held := func(hold pb.WorkerHold, st pb.AgentStatus) Model {
		b.sandboxes[0].Controller = &pb.SandboxController{OrchestratorId: "o-a", Name: "review-bot", HostId: "hosta",
			Run: &pb.WorkerRun{Id: "run-7", Action: "review-pr", Hold: hold, StepRole: pb.StepRole_STEP_ROLE_CLEANUP}}
		b.sandboxes[0].Agent = &pb.AgentSession{Status: st}
		m = feed2(m, m.listDataCmd())
		m.refreshListItems()
		return selectSandbox(m, "sb")
	}
	for _, c := range []struct {
		hold pb.WorkerHold
		st   pb.AgentStatus
	}{
		{pb.WorkerHold_WORKER_HOLD_CLEANING_UP, pb.AgentStatus_AGENT_STATUS_WORKING},
		{pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT},
		{pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT},
	} {
		m = held(c.hold, c.st)
		before := a.orchCallsJoined()
		mm, cmd := update(m, press("m"))
		if cmd != nil || !strings.Contains(mm.status, "no run awaiting review") || a.orchCallsJoined() != before {
			t.Errorf("%v/%v: m should be refused locally: status %q", c.hold, c.st, mm.status)
		}
	}
	m = held(pb.WorkerHold_WORKER_HOLD_CLEANING_UP, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "sand-b") || !strings.Contains(v, "cleaning up run-7 · needs input") || !strings.Contains(v, "mark free") {
		t.Fatalf("a held worker asking for input is surfaced, with m offered:\n%s", v)
	}
	before := a.orchCallsJoined()
	m, cmd := update(m, press("m"))
	if cmd == nil || m.screen != screenList || m.orchDetail.form != nil || m.markFreeFromList {
		t.Fatalf("m frees a cleaning-up worker directly: cmd %v screen %v status %q", cmd != nil, m.screen, m.status)
	}
	done, ok := runCmd(cmd).(orchDoneMsg)
	if !ok || done.err != nil || !strings.Contains(done.status, "marked sand-b free") {
		t.Errorf("result = %+v", done)
	}
	if calls := strings.TrimPrefix(a.orchCallsJoined(), before); !strings.Contains(calls, `MarkWorkerFree o-a sb false ""`) {
		t.Errorf("calls = %q", calls)
	}
}

// The Workers view frees a cleaning-up worker asking for input the same way.
func TestWorkersViewMarkFreeCleaningUp(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	a.orchDetail = &pb.OrchestratorDetail{Summary: a.orchestrators[0], Workers: []*pb.WorkerView{
		{SandboxId: "w1", DisplayName: "worker-one", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT, RunId: "run-7", Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP},
	}}
	m := sized(New(a, "/work"))
	m = openDetail(t, m)
	m, cmd := update(m, press("m"))
	if cmd == nil || m.orchDetail.form != nil {
		t.Fatal("m on a cleaning-up worker asking for input frees it directly")
	}
	if done, ok := runCmd(cmd).(orchDoneMsg); !ok || done.err != nil || !strings.Contains(done.status, "marked worker-one free") {
		t.Errorf("result = %+v", done)
	}
	if !strings.Contains(a.orchCallsJoined(), `MarkWorkerFree o-a w1 false ""`) {
		t.Errorf("calls = %q", a.orchCallsJoined())
	}
}
