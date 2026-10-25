package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func reviewAction() *pb.Action {
	return &pb.Action{Name: "review-pr", Description: "Review a PR", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "Review {number} in {repo}",
		Arguments: []*pb.ActionArgument{{Name: "number", Required: true, Description: "PR number"}, {Name: "repo", Required: true, DefaultValue: "acme/web"}, {Name: "focus"}}}
}

// detailWith opens the detail of an orchestrator carrying the given actions and runs.
func detailWith(t *testing.T, actions []*pb.Action, runs *pb.ListRunsResponse) (Model, *fakeDaemon) {
	t.Helper()
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host", Actions: actions}}}, orchRuns: runs}
	m := sized(New(a, "/work"))
	m = openDetail(t, m)
	return m, a
}

func TestActionFormRoundTripAndRefusal(t *testing.T) {
	m, a := detailWith(t, nil, nil)
	m.orchDetail.view = viewActions
	if !strings.Contains(m.View(), "(no actions)") {
		t.Error("empty actions view")
	}
	m, _ = update(m, press("a"))
	if m.orchDetail.form == nil || m.orchDetail.formKind != "action" {
		t.Fatal("a should open the action editor")
	}
	v := m.orchDetail.formVals.(*actionFormVals)
	v.name, v.description, v.body, v.workerTag = "review-pr", "Review a PR", "Review {number} in {repo}", "ci"
	v.args = "number * PR number\nrepo * = acme/web\nfocus"
	m, cmd := update(m, ctrlS())
	m = feed2(m, cmd)
	if len(a.putActions) != 1 {
		t.Fatalf("PutAction calls = %d (%q)", len(a.putActions), a.orchCallsJoined())
	}
	got := a.putActions[0]
	if got.GetName() != "review-pr" || got.GetType() != pb.ActionType_ACTION_TYPE_PROMPT || got.GetWorkerTag() != "ci" || len(got.GetArguments()) != 3 {
		t.Errorf("saved action = %+v", got)
	}
	if args := got.GetArguments(); !args[0].GetRequired() || args[0].GetDescription() != "PR number" || args[1].GetDefaultValue() != "acme/web" || !args[1].GetRequired() || args[2].GetRequired() {
		t.Errorf("arguments = %+v", args)
	}
	if m.orchDetail.form != nil || !strings.Contains(m.status, "saved action review-pr") {
		t.Errorf("after save: form %v status %q", m.orchDetail.form != nil, m.status)
	}
	v2 := m.View()
	for _, want := range []string{"review-pr", "(prompt)", "Review a PR", "#ci", "{number*, repo*, focus}"} {
		if !strings.Contains(v2, want) {
			t.Errorf("actions view missing %q", want)
		}
	}
	// e pre-fills the editor; an INVALID_ARGUMENT reply shows verbatim and keeps it open.
	m, _ = update(m, press("e"))
	v = m.orchDetail.formVals.(*actionFormVals)
	if v.original != "review-pr" || !strings.Contains(v.args, "number * PR number") || !strings.Contains(v.args, "repo * = acme/web") {
		t.Errorf("edit form pre-fill = %+v", v)
	}
	a.orchErr = status.Error(codes.InvalidArgument, "placeholder {nope} names no argument")
	m, cmd = update(m, ctrlS())
	m = feed(m, cmd)
	if m.orchDetail.form == nil || !strings.Contains(m.orchDetail.status, "placeholder {nope} names no argument") {
		t.Errorf("refusal should keep the form open with the text: form %v status %q", m.orchDetail.form != nil, m.orchDetail.status)
	}
	a.orchErr = nil
	// A bad argument line is refused client-side.
	v.args = "1st-arg * bad name"
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.orchDetail.status, "must be letters, digits and underscores") {
		t.Errorf("bad argument line status = %q", m.orchDetail.status)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.orchDetail.form != nil {
		t.Error("esc closes the form")
	}
	// d is confirm-gated and shows the disabled events.
	a.disabledEvents = []string{"poll"}
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Delete action review-pr?") {
		t.Fatalf("d should confirm: %v", m.screen)
	}
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "DeleteAction o-a review-pr") || !strings.Contains(m.status, "disabled events: poll") {
		t.Errorf("delete: calls %q status %q", a.orchCallsJoined(), m.status)
	}
}

func TestCallFormBuiltFromArguments(t *testing.T) {
	m, a := detailWith(t, []*pb.Action{reviewAction()}, nil)
	m.orchDetail.view = viewActions
	m, _ = update(m, press("enter"))
	if m.orchDetail.form == nil || !strings.HasPrefix(m.orchDetail.formKind, "call ") {
		t.Fatal("enter should open the call form")
	}
	v := m.orchDetail.formVals.(*callFormVals)
	if *v.values["repo"] != "acme/web" || *v.values["number"] != "" {
		t.Errorf("defaults should be pre-filled: %+v", v.values)
	}
	view := m.View()
	if !strings.Contains(view, "number *") || !strings.Contains(view, "PR number") {
		t.Error("the form shows names, descriptions and the required marker")
	}
	// Submission is refused while a required value is empty; nothing is sent.
	m, _ = update(m, ctrlS())
	if m.orchDetail.form == nil || !strings.Contains(m.orchDetail.status, "argument number is required") || strings.Contains(a.orchCallsJoined(), "CallAction") {
		t.Fatalf("empty required: status %q calls %q", m.orchDetail.status, a.orchCallsJoined())
	}
	*v.values["number"] = "412"
	*v.values["focus"] = "tests"
	m, cmd := update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "CallAction o-a review-pr focus=tests,number=412,repo=acme/web") {
		t.Errorf("calls = %q", a.orchCallsJoined())
	}
	if !strings.Contains(m.status, "queued run-1 at position 1") {
		t.Errorf("status = %q", m.status)
	}
	// A daemon INVALID_ARGUMENT keeps the call form open with the text.
	m, _ = update(m, press("enter"))
	v = m.orchDetail.formVals.(*callFormVals)
	*v.values["number"] = "9"
	a.orchErr = status.Error(codes.InvalidArgument, "argument number is larger than 16384 bytes")
	m, cmd = update(m, ctrlS())
	m = feed(m, cmd)
	if m.orchDetail.form == nil || !strings.Contains(m.orchDetail.status, "larger than 16384") {
		t.Error("daemon refusal should keep the call form open verbatim")
	}
}

func TestQueueViewGroupsPromptCancelAndMarkFree(t *testing.T) {
	runs := &pb.ListRunsResponse{
		Queued:   []*pb.Run{{Id: "run-q", Action: "review-pr", State: pb.RunState_RUN_STATE_QUEUED, Position: 1, EventKind: pb.EventKind_EVENT_KIND_TIMED, OriginLabel: "poll", Prompt: "Review 412 in acme/web\n\nfocus: tests"}},
		InFlight: []*pb.Run{{Id: "run-r", Action: "review-pr", State: pb.RunState_RUN_STATE_AWAITING_REVIEW, SandboxId: "sb-review", EventKind: pb.EventKind_EVENT_KIND_TRIGGERED, OriginLabel: "worker-two"}},
		History:  []*pb.Run{{Id: "run-h", Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, Summary: "closed well", ClosedByDeveloper: true}, {Id: "run-f", Action: "triage", State: pb.RunState_RUN_STATE_FAILED, Reason: "agent exited"}},
	}
	m, a := detailWith(t, []*pb.Action{reviewAction()}, runs)
	m.orchDetail.view = viewQueue
	view := m.View()
	for _, want := range []string{"Queued", "In progress", "History", "#1", "timed poll", "awaiting review", "on sb-revie", "closed by developer: closed well", "agent exited", "failed"} {
		if !strings.Contains(view, want) {
			t.Errorf("queue view missing %q:\n%s", want, view)
		}
	}
	// enter opens the prompt overlay for the highlighted run; esc closes it.
	m, cmd := update(m, press("enter"))
	m = feed(m, cmd)
	if m.orchDetail.prompt == nil || !strings.Contains(m.View(), "Review 412 in acme/web") || !strings.Contains(m.View(), "Run run-q") {
		t.Fatalf("enter should show the prompt: %+v", m.orchDetail.prompt)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.orchDetail.prompt != nil {
		t.Error("esc closes the overlay")
	}
	// x cancels only a queued row (confirm-gated).
	m, _ = update(m, press("x"))
	if m.screen != screenConfirm {
		t.Fatal("x on a queued run should confirm")
	}
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "CancelRun o-a run-q") {
		t.Errorf("cancel calls = %q", a.orchCallsJoined())
	}
	m, _ = update(m, press("j")) // the in-flight row
	m, _ = update(m, press("x"))
	if m.screen != screenOrchDetail || !strings.Contains(m.orchDetail.status, "only a queued run") {
		t.Errorf("x on a running row: %v %q", m.screen, m.orchDetail.status)
	}
	// m on the awaiting-review row opens the dialog; failed + note reach the daemon.
	m, _ = update(m, press("m"))
	if m.orchDetail.form == nil || m.orchDetail.formKind != "mark free" {
		t.Fatal("m should open the mark-free dialog")
	}
	v := m.orchDetail.formVals.(*markFreeVals)
	v.outcome, v.note = "failed", "did not finish"
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), `MarkWorkerFree o-a sb-review true "did not finish"`) || !strings.Contains(m.status, "closed run-reviewed as failed") {
		t.Errorf("mark free: calls %q status %q", a.orchCallsJoined(), m.status)
	}
	// m on a history row is refused locally.
	m.orchDetail.cursors[viewQueue] = 2
	m, _ = update(m, press("m"))
	if m.orchDetail.form != nil || !strings.Contains(m.orchDetail.status, "only a run awaiting review") {
		t.Error("m on a finished run is refused")
	}
	if runLine(&pb.Run{State: pb.RunState_RUN_STATE_RUNNING, Action: "x", SandboxId: "s"}) == "" || relTime(nil) != "—" {
		t.Error("renderers")
	}
}

func TestMarkFreeFromSandboxListAndWorkersView(t *testing.T) {
	a := &fakeDaemon{hostID: "test-host", orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host"}}}}
	a.sandboxes = []*pb.Sandbox{{Id: "w1", DisplayName: "worker-one", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Controller: &pb.SandboxController{OrchestratorId: "o-a", Name: "alpha-bot", HostId: "test-host", Run: &pb.WorkerRun{Id: "run-1", Action: "review-pr", NeedsReview: true}}}}
	m := withSandboxes(sized(New(a, "/work")), a.sandboxes)
	// The help offers m only while the row's run awaits review.
	if !strings.Contains(m.renderHelp(m.listHelp()), "mark free") {
		t.Error("help should offer m for a reviewed worker")
	}
	m, _ = update(m, press("m"))
	if m.screen != screenOrchDetail || m.orchDetail.form == nil || m.orchDetail.formKind != "mark free" {
		t.Fatalf("m on the row should open the dialog: screen %v", m.screen)
	}
	if !strings.Contains(m.View(), "Outcome") {
		t.Error("the dialog renders")
	}
	m, cmd := update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), `MarkWorkerFree o-a w1 false ""`) {
		t.Errorf("calls = %q", a.orchCallsJoined())
	}
	if m.screen != screenList {
		t.Errorf("the dialog returns to the list, got %v", m.screen)
	}
	// esc from the list-opened dialog also returns to the list.
	m, _ = update(m, press("m"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc returns to the list")
	}
	// Workers view: m opens the dialog on an AWAITING_REVIEW row, frees a row
	// parked awaiting input with no run outright, and is refused elsewhere.
	a.orchDetail = &pb.OrchestratorDetail{Summary: a.orchestrators[0], Workers: []*pb.WorkerView{
		{SandboxId: "w1", DisplayName: "worker-one", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_REVIEW},
		{SandboxId: "w2", DisplayName: "worker-two", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_FREE},
		{SandboxId: "w3", DisplayName: "worker-three", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT},
		{SandboxId: "w4", DisplayName: "worker-four", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT, RunId: "run-9"},
	}}
	m = openDetail(t, m)
	m, _ = update(m, press("m"))
	if m.orchDetail.form == nil {
		t.Fatal("m on an awaiting-review worker opens the dialog")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("j"))
	m, _ = update(m, press("m"))
	if m.orchDetail.form != nil || !strings.Contains(m.orchDetail.status, "no run awaiting review") {
		t.Error("m on a free worker is refused")
	}
	m, _ = update(m, press("j"))
	before := a.orchCallsJoined()
	m, cmd = update(m, press("m"))
	if cmd == nil || m.orchDetail.form != nil {
		t.Fatal("m on a parked worker with no run frees it directly")
	}
	if done, ok := runCmd(cmd).(orchDoneMsg); !ok || done.err != nil || !strings.Contains(done.status, "marked worker-three free") {
		t.Errorf("parked worker result = %+v", done)
	}
	if calls := strings.TrimPrefix(a.orchCallsJoined(), before); !strings.Contains(calls, `MarkWorkerFree o-a w3 false ""`) {
		t.Errorf("calls = %q", calls)
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("m"))
	if m.orchDetail.form != nil || !strings.Contains(m.orchDetail.status, "no run awaiting review") {
		t.Error("m on a worker awaiting input mid-run is refused (the run owns the wait)")
	}
	m, _ = update(m, press("k"))
	m, _ = update(m, press("k"))
	// A FAILED_PRECONDITION from the daemon keeps the dialog open verbatim.
	m, _ = update(m, press("k"))
	m, _ = update(m, press("m"))
	a.orchErr = status.Error(codes.FailedPrecondition, "no run awaiting review on sandbox w1")
	m, cmd = update(m, ctrlS())
	m = feed(m, cmd)
	if m.orchDetail.form == nil || !strings.Contains(m.orchDetail.status, "no run awaiting review on sandbox w1") {
		t.Error("refusal verbatim")
	}
}

func TestInboxRunKinds(t *testing.T) {
	a := &fakeDaemon{}
	a.sandboxes = []*pb.Sandbox{{Id: "w1", DisplayName: "worker-one"}, {Id: "w2", DisplayName: "worker-two"}}
	m := withSandboxes(sized(New(a, "/work").WithNotifier(&recordNotifier{})), a.sandboxes)
	m.sub = nil
	for _, k := range []pb.NotificationKind{pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED, pb.NotificationKind_NOTIFICATION_KIND_RUN_AWAITING_REVIEW, pb.NotificationKind_NOTIFICATION_KIND_RUN_NEEDS_INPUT} {
		m, _ = update(m, eventMsg{ev: notif("n-"+k.String(), "w2", k)})
	}
	m, _ = update(m, press("i"))
	view := m.viewNotifications()
	for _, want := range []string{"run failed", "run awaiting review", "worker needs input", "✗", "⚑", "?"} {
		if !strings.Contains(view, want) {
			t.Errorf("inbox missing %q", want)
		}
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList || m.list.Index() != 1 {
		t.Errorf("enter should land on the worker's row: screen %v index %d", m.screen, m.list.Index())
	}
	for _, k := range []pb.NotificationKind{pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED, pb.NotificationKind_NOTIFICATION_KIND_RUN_AWAITING_REVIEW, pb.NotificationKind_NOTIFICATION_KIND_RUN_NEEDS_INPUT} {
		if notifTitle(k) == "task complete" || notifIcon(k) == "" {
			t.Errorf("kind %v should have its own title and icon", k)
		}
	}
}

func TestEventFormAndView(t *testing.T) {
	ev := &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true, Arguments: map[string]string{"number": "1", "repo": "r"}, SkippedCount: 2}
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host", Actions: []*pb.Action{reviewAction()}, TimedEvents: []*pb.TimedEvent{ev}}}},
		orchRuns: &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "run-q", EventKind: pb.EventKind_EVENT_KIND_TIMED, Origin: "tev-1"}}}}
	m := sized(New(a, "/work"))
	m = openDetail(t, m)
	m.orchDetail.view = viewEvents
	view := m.View()
	for _, want := range []string{"[on]", "poll", "→ review-pr", "every 5m", "waiting 1", "skipped 2", "dedupe"} {
		if !strings.Contains(view, want) {
			t.Errorf("events view missing %q", want)
		}
	}
	// space toggles enabled.
	m, cmd := update(m, press(" "))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "PutTimedEvent o-a poll dedupe_disabled=false enabled=false") {
		t.Errorf("toggle calls = %q", a.orchCallsJoined())
	}
	// a opens the editor with dedupe on by default; interval below 1 refused.
	m, _ = update(m, press("a"))
	v := m.orchDetail.formVals.(*eventFormVals)
	if !v.dedupe || !v.enabled || v.action != "review-pr" {
		t.Errorf("defaults = %+v", v)
	}
	v.name, v.intervalMinutes, v.args = "nightly", "0", "number = 7"
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.orchDetail.status, "at least 1") {
		t.Errorf("interval refusal = %q", m.orchDetail.status)
	}
	v.intervalMinutes = "60"
	v.dedupe = false
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "PutTimedEvent o-a nightly dedupe_disabled=true enabled=true interval=3600") {
		t.Errorf("save calls = %q", a.orchCallsJoined())
	}
	if len(a.putEvents) == 0 || a.putEvents[len(a.putEvents)-1].GetArguments()["number"] != "7" {
		t.Errorf("saved event arguments = %+v", a.putEvents)
	}
	// e pre-fills; d is confirm-gated; disabled reason renders.
	m, _ = update(m, press("e"))
	v = m.orchDetail.formVals.(*eventFormVals)
	if v.id != "tev-1" || v.intervalMinutes != "5" || !strings.Contains(v.args, "number = 1") {
		t.Errorf("edit pre-fill = %+v", v)
	}
	v.args = "nonsense"
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.orchDetail.status, "must be name = value") {
		t.Errorf("bad kv = %q", m.orchDetail.status)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm {
		t.Fatal("d confirms")
	}
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "DeleteTimedEvent o-a tev-1") {
		t.Errorf("delete calls = %q", a.orchCallsJoined())
	}
	a.orchestrators[0].Orchestrator.TimedEvents = []*pb.TimedEvent{{Id: "tev-2", Name: "poll", Action: "review-pr", IntervalSeconds: 60, DisabledReason: "action review-pr was deleted"}}
	m = feed(m, m.orchDetailLoadCmd())
	if !strings.Contains(m.View(), "disabled: action review-pr was deleted") || !strings.Contains(m.View(), "[off]") {
		t.Error("disabled reason should render")
	}
	if nextFire(&pb.TimedEvent{}) != "—" {
		t.Error("nextFire")
	}
	// No actions: the editor refuses to open.
	a.orchestrators[0].Orchestrator.Actions = nil
	m = feed(m, m.orchDetailLoadCmd())
	m, _ = update(m, press("a"))
	if m.orchDetail.form != nil || !strings.Contains(m.orchDetail.status, "add an action first") {
		t.Error("no actions ⇒ no event form")
	}
}

// SWI-4: `n` on the Events view enqueues the selected event now.
func TestEventsViewEnqueueNow(t *testing.T) {
	ev := &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host", Actions: []*pb.Action{reviewAction()}, TimedEvents: []*pb.TimedEvent{ev}}}}}
	m := sized(New(a, "/work"))
	m = openDetail(t, m)
	m.orchDetail.view = viewEvents
	if !strings.Contains(m.View(), "n enqueue now") {
		t.Error("the Events view hints should include n enqueue now")
	}
	m, cmd := update(m, press("n"))
	if m.screen == screenConfirm {
		t.Fatal("enqueue now needs no confirmation")
	}
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "FireTimedEvent o-a tev-1") {
		t.Errorf("calls = %q", a.orchCallsJoined())
	}
	if !strings.Contains(m.status, "enqueued poll (run-9, position 2)") {
		t.Errorf("status = %q", m.status)
	}

	// The daemon's dedupe refusal is shown as is.
	a.orchErr = errors.New("run run-1 of poll is already queued")
	m, cmd = update(m, press("n"))
	m = feed2(m, cmd)
	if !strings.Contains(m.orchDetail.status, "run run-1 of poll is already queued") {
		t.Errorf("error status = %q", m.orchDetail.status)
	}
}

func TestEventsViewEnqueueNowWithNoEventsDoesNothing(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host"}}}}
	m := sized(New(a, "/work"))
	m = openDetail(t, m)
	m.orchDetail.view = viewEvents
	if _, cmd := update(m, press("n")); cmd != nil {
		t.Error("n with no events must do nothing")
	}
}
