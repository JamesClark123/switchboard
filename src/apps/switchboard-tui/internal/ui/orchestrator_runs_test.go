package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Feature 009's per-orchestrator Actions / Queue / Events behaviour, migrated to
// the Jobs screen (feature 010): the same operations through the target-based
// implementation, asserting the same daemon calls and outcomes (FR-027).

func reviewAction() *pb.Action {
	return &pb.Action{Name: "review-pr", Description: "Review a PR", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "Review {number} in {repo}",
		Arguments: []*pb.ActionArgument{{Name: "number", Required: true, Description: "PR number"}, {Name: "repo", Required: true, DefaultValue: "acme/web"}, {Name: "focus"}}}
}

// jobsWith opens Jobs on one daemon hosting alpha-bot with the given actions and
// runs, on the given tab.
func jobsWith(t *testing.T, tab jobsTab, actions []*pb.Action, runs *pb.ListRunsResponse) (Model, *fakeDaemon) {
	t.Helper()
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host", Actions: actions}}}, orchRuns: runs}
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = tab
	return m, a
}

func TestActionFormRoundTripAndRefusal(t *testing.T) {
	m, a := jobsWith(t, jobsActions, nil, nil)
	if !strings.Contains(m.View(), "no actions — press a to add one") || !strings.Contains(m.View(), "alpha-bot @ test-host") {
		t.Errorf("empty actions group:\n%s", m.View())
	}
	m, _ = update(m, press("a"))
	if m.jobs.form == nil || m.jobs.formKind != "action" || m.jobs.formTarget.id != "o-a" {
		t.Fatal("a should open the action editor for the group's orchestrator")
	}
	if !strings.Contains(m.View(), "alpha-bot @ test-host › action") {
		t.Error("the form names its orchestrator")
	}
	v := m.jobs.formVals.(*actionFormVals)
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
	if m.jobs.form != nil || !strings.Contains(m.status, "saved action review-pr") {
		t.Errorf("after save: form %v status %q", m.jobs.form != nil, m.status)
	}
	v2 := m.View()
	for _, want := range []string{"review-pr", "(prompt)", "Review a PR", "#ci", "{number*, repo*, focus}"} {
		if !strings.Contains(v2, want) {
			t.Errorf("actions tab missing %q", want)
		}
	}
	// e pre-fills the editor; an INVALID_ARGUMENT reply shows verbatim and keeps it open.
	m, _ = update(m, press("e"))
	v = m.jobs.formVals.(*actionFormVals)
	if v.original != "review-pr" || !strings.Contains(v.args, "number * PR number") || !strings.Contains(v.args, "repo * = acme/web") {
		t.Errorf("edit form pre-fill = %+v", v)
	}
	a.orchErr = status.Error(codes.InvalidArgument, "placeholder {nope} names no argument")
	m, cmd = update(m, ctrlS())
	m = feed(m, cmd)
	if m.jobs.form == nil || !strings.Contains(m.jobs.status, "placeholder {nope} names no argument") || !strings.Contains(m.View(), "names no argument") {
		t.Errorf("refusal should keep the form open with the text: form %v status %q", m.jobs.form != nil, m.jobs.status)
	}
	a.orchErr = nil
	// A bad argument line is refused client-side.
	v.args = "1st-arg * bad name"
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.jobs.status, "must be letters, digits and underscores") {
		t.Errorf("bad argument line status = %q", m.jobs.status)
	}
	m = escDiscard(t, m)
	if m.jobs.form != nil || m.jobs.status != "" {
		t.Error("esc, discard closes the form and its refusal")
	}
	// d is confirm-gated and shows the disabled events.
	a.disabledEvents = []string{"poll"}
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm || m.confirm.returnTo != screenJobs || !strings.Contains(m.View(), "Delete action review-pr?") {
		t.Fatalf("d should confirm over Jobs: %v", m.screen)
	}
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "DeleteAction o-a review-pr") || !strings.Contains(m.status, "disabled events: poll") {
		t.Errorf("delete: calls %q status %q", a.orchCallsJoined(), m.status)
	}
	if m.screen != screenJobs {
		t.Errorf("the dialog returns to Jobs, got %v", m.screen)
	}
}

func TestCallFormBuiltFromArguments(t *testing.T) {
	m, a := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
	m, _ = update(m, press("enter"))
	if m.jobs.form == nil || !strings.HasPrefix(m.jobs.formKind, "call ") {
		t.Fatal("enter should open the call form")
	}
	v := m.jobs.formVals.(*callFormVals)
	if *v.values["repo"] != "acme/web" || *v.values["number"] != "" {
		t.Errorf("defaults should be pre-filled: %+v", v.values)
	}
	view := m.View()
	if !strings.Contains(view, "number *") || !strings.Contains(view, "PR number") {
		t.Error("the form shows names, descriptions and the required marker")
	}
	// Submission is refused while a required value is empty; nothing is sent.
	m, _ = update(m, ctrlS())
	if m.jobs.form == nil || !strings.Contains(m.jobs.status, "argument number is required") || strings.Contains(a.orchCallsJoined(), "CallAction") {
		t.Fatalf("empty required: status %q calls %q", m.jobs.status, a.orchCallsJoined())
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
	v = m.jobs.formVals.(*callFormVals)
	*v.values["number"] = "9"
	a.orchErr = status.Error(codes.InvalidArgument, "argument number is larger than 16384 bytes")
	m, cmd = update(m, ctrlS())
	m = feed(m, cmd)
	if m.jobs.form == nil || !strings.Contains(m.jobs.status, "larger than 16384") {
		t.Error("daemon refusal should keep the call form open verbatim")
	}
	// A call form with no arguments says so and still calls.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	a.orchErr = nil
	mm, _ := m.openCallForm(surfaceJobs, m.jobs.orchs[0].target, &pb.Action{Name: "ping"})
	m = mm.(Model)
	if !strings.Contains(m.View(), "No arguments") {
		t.Error("an argument-less call form says so")
	}
}

func TestOverviewRunsPromptCancelAndMarkFree(t *testing.T) {
	runs := &pb.ListRunsResponse{
		Queued:   []*pb.Run{{Id: "run-q", Action: "review-pr", State: pb.RunState_RUN_STATE_QUEUED, Position: 1, EventKind: pb.EventKind_EVENT_KIND_TIMED, OriginLabel: "poll", Prompt: "Review 412 in acme/web\n\nfocus: tests"}},
		InFlight: []*pb.Run{{Id: "run-r", Action: "review-pr", State: pb.RunState_RUN_STATE_AWAITING_REVIEW, SandboxId: "sb-review", EventKind: pb.EventKind_EVENT_KIND_TRIGGERED, OriginLabel: "worker-two"}},
		History:  []*pb.Run{{Id: "run-h", Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, Summary: "closed well", ClosedByDeveloper: true}, {Id: "run-f", Action: "triage", State: pb.RunState_RUN_STATE_FAILED, Reason: "agent exited"}},
	}
	m, a := jobsWith(t, jobsOverview, []*pb.Action{reviewAction()}, runs)
	view := m.View()
	for _, want := range []string{"Upcoming", "Queued", "In progress", "History (latest 15)", "#1", "timed poll", "awaiting review", "on sb-revie", "closed by developer: clos", "agent exited", "failed", "alpha-bot @ test-host"} {
		if !strings.Contains(view, want) {
			t.Errorf("overview missing %q:\n%s", want, view)
		}
	}
	// enter opens the prompt overlay for the highlighted run; esc closes it.
	m, cmd := update(m, press("enter"))
	m = feed(m, cmd)
	if m.jobs.prompt == nil || !strings.Contains(m.View(), "Review 412 in acme/web") || !strings.Contains(m.View(), "Run run-q") {
		t.Fatalf("enter should show the prompt: %+v", m.jobs.prompt)
	}
	if !strings.Contains(m.renderHelp(m.jobsHelp()), "close") {
		t.Error("the overlay's help offers close")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.jobs.prompt != nil {
		t.Error("esc closes the overlay")
	}
	// x cancels only a queued row (confirm-gated, on the run's own orchestrator).
	if !strings.Contains(m.renderHelp(m.jobsHelp()), "cancel") {
		t.Error("a queued row's help offers x cancel")
	}
	m, _ = update(m, press("x"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Cancel run run-q?") {
		t.Fatal("x on a queued run should confirm")
	}
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "CancelRun o-a run-q") || !strings.Contains(m.status, "cancelled run-q") {
		t.Errorf("cancel calls = %q status %q", a.orchCallsJoined(), m.status)
	}
	m, _ = update(m, press("j")) // the in-flight row
	if !strings.Contains(m.renderHelp(m.jobsHelp()), "mark free") {
		t.Error("an awaiting-review row's help offers m mark free")
	}
	m, _ = update(m, press("x"))
	if m.screen != screenJobs || !strings.Contains(m.jobs.status, "only a queued run") {
		t.Errorf("x on a running row: %v %q", m.screen, m.jobs.status)
	}
	// m on the awaiting-review row opens the dialog; failed + note reach the daemon.
	m, _ = update(m, press("m"))
	if m.jobs.form == nil || m.jobs.formKind != "mark free" {
		t.Fatal("m should open the mark-free dialog")
	}
	v := m.jobs.formVals.(*markFreeVals)
	v.outcome, v.note = "failed", "did not finish"
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), `MarkWorkerFree o-a sb-review true "did not finish"`) || !strings.Contains(m.status, "closed run-reviewed as failed") {
		t.Errorf("mark free: calls %q status %q", a.orchCallsJoined(), m.status)
	}
	if m.jobs.form != nil || m.screen != screenJobs {
		t.Error("a successful mark free closes the dialog and stays on Jobs")
	}
	// m on a history row is refused locally; o is offered there instead.
	m.jobs.cursors[jobsOverview] = 2
	if h := m.renderHelp(m.jobsHelp()); !strings.Contains(h, "open results") || strings.Contains(h, "mark free") {
		t.Errorf("a finished row's help: %q", h)
	}
	m, _ = update(m, press("m"))
	if m.jobs.form != nil || !strings.Contains(m.jobs.status, "only a run awaiting review") {
		t.Error("m on a finished run is refused")
	}
	// A GetRun failure lands on the Jobs line.
	a.orchErr = errBoom{}
	m, cmd = update(m, press("enter"))
	m = feed(m, cmd)
	if m.jobs.prompt != nil || m.jobs.status != "boom" {
		t.Errorf("a failed prompt fetch: prompt %v status %q", m.jobs.prompt != nil, m.jobs.status)
	}
}

// Feature 009's mark free from the sandbox list (over the detail, back to the
// list) and from the Workers view are unchanged.
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
	// The detail's form is forwarded non-key messages, and closes after a success.
	a.orchErr = nil
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.orchDetail.form == nil {
		t.Error("tab must not close the dialog")
	}
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if m.orchDetail.form != nil || m.screen != screenOrchDetail || !strings.Contains(m.status, "closed run-reviewed") {
		t.Errorf("a detail mark free closes the dialog on the detail: form %v screen %v status %q", m.orchDetail.form != nil, m.screen, m.status)
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
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsEvents
	view := m.View()
	for _, want := range []string{"alpha-bot @ test-host", "[on]", "poll", "→ review-pr", "every 5m", "waiting 1", "skipped 2", "dedupe"} {
		if !strings.Contains(view, want) {
			t.Errorf("events tab missing %q", want)
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
	v := m.jobs.formVals.(*eventFormVals)
	if !v.dedupe || !v.enabled || v.action != "review-pr" {
		t.Errorf("defaults = %+v", v)
	}
	v.name, v.intervalMinutes, v.args = "nightly", "0", "number = 7"
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.jobs.status, "at least 1") {
		t.Errorf("interval refusal = %q", m.jobs.status)
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
	m.jobs.cursors[jobsEvents] = 0
	m, _ = update(m, press("e"))
	v = m.jobs.formVals.(*eventFormVals)
	if v.id != "tev-1" || v.intervalMinutes != "5" || !strings.Contains(v.args, "number = 1") {
		t.Errorf("edit pre-fill = %+v", v)
	}
	v.args = "nonsense"
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.jobs.status, "must be name = value") {
		t.Errorf("bad kv = %q", m.jobs.status)
	}
	m = escDiscard(t, m)
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
	m = reloadJobs(m)
	if !strings.Contains(m.View(), "disabled: action review-pr was deleted") || !strings.Contains(m.View(), "[off]") {
		t.Error("disabled reason should render")
	}
	if nextFire(&pb.TimedEvent{}) != "—" {
		t.Error("nextFire")
	}
	// No actions: the editor refuses to open.
	a.orchestrators[0].Orchestrator.Actions = nil
	m = reloadJobs(m)
	m, _ = update(m, press("a"))
	if m.jobs.form != nil || !strings.Contains(m.jobs.status, "add an action to alpha-bot @ test-host first") {
		t.Errorf("no actions ⇒ no event form; status %q", m.jobs.status)
	}
}

// --- Enqueue now (SWI-4), from the Events tab ---

// eventsModel opens Jobs › Events on one daemon hosting alpha-bot with ev.
func eventsModel(t *testing.T, ev *pb.TimedEvent, a *fakeDaemon) Model {
	t.Helper()
	a.orchestrators = []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host", Actions: []*pb.Action{reviewAction()}, TimedEvents: []*pb.TimedEvent{ev}}}}
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsEvents
	return m
}

// aKey is alpha-bot's target key in eventsModel (the single daemon).
func aKey(m Model) string { return m.jobs.orchs[0].target.key() }

func TestEventsViewEnqueueNow(t *testing.T) {
	ev := &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}
	a := &fakeDaemon{}
	m := eventsModel(t, ev, a)
	if !strings.Contains(m.View(), "n enqueue now") {
		t.Error("the Events tab hints should include n enqueue now")
	}
	m, cmd := update(m, press("n"))
	if m.screen == screenConfirm {
		t.Fatal("enqueue now needs no confirmation")
	}
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "FireTimedEvent o-a tev-1") {
		t.Errorf("calls = %q", a.orchCallsJoined())
	}
	if !strings.Contains(m.status, "enqueued poll on alpha-bot @ test-host (run-9, queued at position 2)") {
		t.Errorf("status = %q", m.status)
	}

	// The daemon's dedupe refusal is shown as is, naming the orchestrator.
	a.orchErr = errors.New("run run-1 of poll is already queued")
	m.jobs.lastFireAt = time.Time{} // past the repeat window
	m, cmd = update(m, press("n"))
	m = feed2(m, cmd)
	if !strings.Contains(m.jobs.status, "enqueue on alpha-bot @ test-host: run run-1 of poll is already queued") {
		t.Errorf("error status = %q", m.jobs.status)
	}
}

func TestEventsViewEnqueueNowWithNoEventsDoesNothing(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host"}}}}
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsEvents
	for _, k := range []string{"n", "e", "d", " "} {
		if mm, cmd := update(m, press(k)); cmd != nil || mm.screen != screenJobs || mm.jobs.form != nil {
			t.Errorf("%q with no events must do nothing", k)
		}
	}
	if !strings.Contains(m.View(), "no events — press a to add one") {
		t.Error("an empty events group says how to add one")
	}
}

// With dedupe off AND a run of the event already open, n asks first; with no open
// run it enqueues directly (nothing would double up).
func TestEnqueueNowWithDedupeOffConfirmsOnlyWhenARunIsOpen(t *testing.T) {
	ev := &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true, DedupeDisabled: true}

	// No open run: straight through, no dialog.
	a := &fakeDaemon{}
	m := eventsModel(t, ev, a)
	m, cmd := update(m, press("n"))
	if m.screen == screenConfirm || cmd == nil {
		t.Fatalf("dedupe off with no open run should enqueue directly; screen %v", m.screen)
	}
	m = feed2(m, cmd)
	if got := strings.Count(a.orchCallsJoined(), "FireTimedEvent o-a tev-1"); got != 1 {
		t.Errorf("calls %q", a.orchCallsJoined())
	}

	// An open run of this event (queued or in flight): confirm first.
	for name, runs := range map[string]*pb.ListRunsResponse{
		"queued":    {Queued: []*pb.Run{{Id: "run-q", EventKind: pb.EventKind_EVENT_KIND_TIMED, Origin: "tev-1"}}},
		"in flight": {InFlight: []*pb.Run{{Id: "run-q", EventKind: pb.EventKind_EVENT_KIND_TIMED, Origin: "tev-1"}}},
	} {
		a := &fakeDaemon{orchRuns: runs}
		m := eventsModel(t, ev, a)
		m, cmd := update(m, press("n"))
		if m.screen != screenConfirm || cmd != nil {
			t.Fatalf("%s: n with dedupe off and an open run should open a confirm; screen %v", name, m.screen)
		}
		if v := m.View(); !strings.Contains(v, "Enqueue poll now?") || !strings.Contains(v, "run-q") || !strings.Contains(v, "alongside") {
			t.Errorf("%s: confirm text: %s", name, v)
		}
		m, _ = update(m, press("x")) // an unrelated key is ignored by the dialog
		if m.screen != screenConfirm || strings.Contains(a.orchCallsJoined(), "FireTimedEvent") {
			t.Fatalf("%s: the dialog must ignore unrelated keys and fire nothing", name)
		}
		m, _ = update(m, press("n")) // n cancels
		if m.screen != screenJobs || strings.Contains(a.orchCallsJoined(), "FireTimedEvent") {
			t.Fatalf("%s: cancel must not fire and returns to Jobs: %q", name, a.orchCallsJoined())
		}
		m.jobs.lastFireAt = time.Time{}
		m, _ = update(m, press("n"))
		m, cmd = update(m, press("y"))
		m = feed2(m, cmd)
		if got := strings.Count(a.orchCallsJoined(), "FireTimedEvent o-a tev-1"); got != 1 {
			t.Errorf("%s: confirmed fire should call once; calls %q", name, a.orchCallsJoined())
		}
	}
}

// While one enqueue is in flight a second n is refused; the guard clears when the
// fire's own reply lands, on success and on error.
func TestEnqueueNowRefusesASecondPressWhileInFlight(t *testing.T) {
	a := &fakeDaemon{}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}, a)
	m, first := update(m, press("n"))
	if first == nil || m.orchFires[aKey(m)] == 0 {
		t.Fatal("first n should dispatch and record the fire")
	}
	m, second := update(m, press("n"))
	if second != nil || !strings.Contains(m.jobs.status, "already in progress") {
		t.Errorf("second n: cmd %v status %q", second != nil, m.jobs.status)
	}
	m = feed2(m, first)
	if len(m.orchFires) != 0 {
		t.Error("the guard must clear when the fire's reply arrives")
	}
	if got := strings.Count(a.orchCallsJoined(), "FireTimedEvent"); got != 1 {
		t.Errorf("exactly one call expected; %q", a.orchCallsJoined())
	}
	// ...and a failure clears it too.
	m.jobs.lastFireAt = time.Time{}
	a.orchErr = errors.New("boom")
	m, c := update(m, press("n"))
	m = feed2(m, c)
	if len(m.orchFires) != 0 {
		t.Error("the guard must clear after an error")
	}
}

// An unrelated orchestrator reply (a toggle, a form save…) finishing while an
// enqueue is in flight must NOT release its guard.
func TestEnqueueNowGuardIgnoresUnrelatedReplies(t *testing.T) {
	a := &fakeDaemon{}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}, a)
	m, fire := update(m, press("n"))
	if m.orchFires[aKey(m)] == 0 {
		t.Fatal("precondition: firing")
	}
	m, _ = update(m, orchDoneMsg{status: "disabled poll", sink: surfaceJobs}) // an unrelated mutation finished
	if m.orchFires[aKey(m)] == 0 {
		t.Fatal("an unrelated reply must not clear the in-flight guard")
	}
	m.jobs.lastFireAt = time.Time{}
	if m2, c := update(m, press("n")); c != nil || !strings.Contains(m2.jobs.status, "already in progress") {
		t.Error("a second n must still be refused")
	}
	m = feed2(m, fire)
	if len(m.orchFires) != 0 {
		t.Error("the fire's own reply releases the guard")
	}
}

// Terminal auto-repeat (or a double tap) right after a fire is dropped silently, so
// the success message is not replaced by a dedupe refusal.
func TestEnqueueNowDropsAnAutoRepeatWithinTheWindow(t *testing.T) {
	a := &fakeDaemon{}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}, a)
	m, cmd := update(m, press("n"))
	m = feed2(m, cmd) // the reply lands; the guard is clear again
	if len(m.orchFires) != 0 || !strings.Contains(m.status, "enqueued poll") {
		t.Fatalf("precondition: success shown, guard clear; status %q", m.status)
	}
	good := m.status
	// A held key: the first dropped press schedules the hint's expiry, the rest
	// schedule nothing (and none calls the daemon; counted below).
	m, expire := update(m, press("n"))
	if expire == nil {
		t.Fatal("the first dropped repeat should schedule the hint's expiry")
	}
	for i := 0; i < 4; i++ {
		var c tea.Cmd
		if m, c = update(m, press("n")); c != nil {
			t.Fatal("later repeats must not schedule another timer")
		}
	}
	if m.status != good {
		t.Errorf("the success message must stay in the footer; status %q", m.status)
	}
	if !strings.Contains(m.jobs.status, "repeat ignored") {
		t.Errorf("the dropped repeat should say so on the Jobs line; %q", m.jobs.status)
	}
	if got := strings.Count(a.orchCallsJoined(), "FireTimedEvent"); got != 1 {
		t.Errorf("one call expected; %q", a.orchCallsJoined())
	}
	// The hint lasts only as long as its window. The expiry message is built
	// directly rather than waiting out the real tick.
	if m, _ = update(m, orchHintExpiredMsg{sink: surfaceJobs, orch: aKey(m), text: enqueueRepeatText}); m.jobs.status != "" {
		t.Errorf("the repeat hint must clear when its window ends; %q", m.jobs.status)
	}
	// A deliberate second press after the window goes through.
	m.jobs.lastFireAt = time.Now().Add(-2 * enqueueRepeatWindow)
	if _, c := update(m, press("n")); c == nil {
		t.Error("a press after the window should dispatch")
	}
}

func TestEnqueueNowStatusWording(t *testing.T) {
	a := &fakeDaemon{orchRun: &pb.Run{Id: "run-9", Position: 3}}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: false}, a)
	m, cmd := update(m, press("n"))
	m = feed2(m, cmd)
	for _, want := range []string{"enqueued poll on alpha-bot @ test-host (run-9, queued at position 3)", "the event is disabled"} {
		if !strings.Contains(m.status, want) {
			t.Errorf("status %q missing %q", m.status, want)
		}
	}
	// No position known (e.g. the daemon could not read it): just the run id.
	a2 := &fakeDaemon{orchRun: &pb.Run{Id: "run-9"}}
	m2 := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}, a2)
	m2, cmd2 := update(m2, press("n"))
	m2 = feed2(m2, cmd2)
	if !strings.Contains(m2.status, "enqueued poll on alpha-bot @ test-host (run-9)") || strings.Contains(m2.status, "position 0") {
		t.Errorf("status = %q", m2.status)
	}
}

// Only a SUCCESSFUL fire starts the repeat window, so a deliberate retry right
// after a refusal goes through; a different event is never held back by another
// event's window.
func TestEnqueueNowRetryAfterAFailedFireIsNotDebounced(t *testing.T) {
	a := &fakeDaemon{}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}, a)
	a.orchErr = errors.New("run run-1 of poll is already queued") // set after the tab loaded: every call fails
	m, cmd := update(m, press("n"))
	m = feed2(m, cmd)
	if !m.jobs.lastFireAt.IsZero() {
		t.Fatal("a refused fire must not start the repeat window")
	}
	a.orchErr = nil
	m, retry := update(m, press("n"))
	if retry == nil {
		t.Fatal("an immediate retry after a refusal must dispatch")
	}
	m = feed2(m, retry)
	if m.jobs.lastFireAt.IsZero() || !strings.Contains(m.status, "enqueued poll") {
		t.Errorf("a successful fire should start the window; status %q", m.status)
	}
}

func TestEnqueueNowWindowIsPerEvent(t *testing.T) {
	a := &fakeDaemon{}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}, a)
	m.jobs.lastFireAt, m.jobs.lastFireKey = time.Now(), aKey(m)+"/tev-other" // another event fired just now
	if _, cmd := update(m, press("n")); cmd == nil {
		t.Error("another event's window must not block this event")
	}
}

// The double tap's "already in progress" clears when the fire succeeds, and an
// expiring hint never clears text that replaced it.
func TestEnqueueNowSuccessClearsTheInProgressHint(t *testing.T) {
	a := &fakeDaemon{}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}, a)
	m, first := update(m, press("n"))
	m, _ = update(m, press("n"))
	if m.jobs.status != enqueueInProgressText {
		t.Fatalf("precondition: in-progress hint; %q", m.jobs.status)
	}
	m = feed2(m, first)
	if m.jobs.status != "" {
		t.Errorf("a successful fire must clear the stale in-progress hint; %q", m.jobs.status)
	}
	m.jobs.status, m.jobs.hintKey = "something newer", aKey(m)
	if m, _ = update(m, orchHintExpiredMsg{sink: surfaceJobs, orch: aKey(m), text: enqueueRepeatText}); m.jobs.status != "something newer" {
		t.Errorf("an expiring hint must not clear other text; %q", m.jobs.status)
	}
}

// twoOrchEvents opens Jobs › Events on one daemon hosting alpha-bot and beta-bot,
// each with a timed event.
func twoOrchEvents(t *testing.T, a *fakeDaemon) Model {
	t.Helper()
	a.orchestrators = []*pb.OrchestratorSummary{
		{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", Actions: []*pb.Action{reviewAction()}, TimedEvents: []*pb.TimedEvent{{Id: "tev-1", Name: "poll", Action: "review-pr", Enabled: true}}}},
		{Orchestrator: &pb.Orchestrator{Id: "o-b", Name: "beta-bot", Actions: []*pb.Action{reviewAction()}, TimedEvents: []*pb.TimedEvent{{Id: "tev-1", Name: "poll", Action: "review-pr", Enabled: true}}}},
	}
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsEvents
	return m
}

// The in-flight guard is per orchestrator: B can be enqueued while A's fire is
// out, and A's reply releases A's record only (it is keyed by the target).
func TestEnqueueNowGuardIsPerOrchestrator(t *testing.T) {
	a := &fakeDaemon{}
	m := twoOrchEvents(t, a)
	m, fireA := update(m, press("n"))
	m, _ = update(m, press("j")) // beta-bot's event
	m, fireB := update(m, press("n"))
	if fireA == nil || fireB == nil || len(m.orchFires) != 2 {
		t.Fatalf("both orchestrators should fire; in flight %v", m.orchFires)
	}
	replyA, _ := runCmd(fireA).(orchDoneMsg)
	m, _ = update(m, replyA)
	if _, still := m.orchFires[replyA.fireKey]; still || len(m.orchFires) != 1 {
		t.Errorf("A's reply releases A only; in flight %v", m.orchFires)
	}
	if m2, c := update(m, press("n")); c != nil || m2.jobs.status != enqueueInProgressText {
		t.Error("B is still in flight")
	}
	// A hint timer for A does not clear B's identical hint.
	m.jobs.status, m.jobs.hintKey = enqueueRepeatText, m.jobs.orchs[1].target.key()
	if m, _ = update(m, orchHintExpiredMsg{sink: surfaceJobs, orch: replyA.fireKey, text: enqueueRepeatText}); m.jobs.status != enqueueRepeatText {
		t.Errorf("another orchestrator's timer must not clear this hint; %q", m.jobs.status)
	}
}

// Leaving Jobs and coming back while a fire is in flight must not allow a second
// one: the record lives on the model; the first reply releases it.
func TestEnqueueNowSurvivesLeavingAndReturning(t *testing.T) {
	a := &fakeDaemon{}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true, DedupeDisabled: true}, a)
	m, fire := update(m, press("n"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Fatalf("esc leaves Jobs; screen %v", m.screen)
	}
	m = openJobs(m)
	if m.jobs.tab != jobsEvents {
		t.Error("the last tab is remembered")
	}
	if m2, c := update(m, press("n")); c != nil || m2.jobs.status != enqueueInProgressText {
		t.Fatalf("a second fire must be refused while the first is in flight; status %q", m2.jobs.status)
	}
	reply, _ := runCmd(fire).(orchDoneMsg)
	m, _ = update(m, reply)
	if len(m.orchFires) != 0 || m.jobs.lastFireAt.IsZero() {
		t.Errorf("the first reply releases the record and starts the window; %v", m.orchFires)
	}
	// A refusal arriving after a return lands on the Jobs line, named.
	a.orchErr = errors.New("run run-1 of poll is already queued")
	m.jobs.lastFireAt = time.Time{}
	m, fire = update(m, press("n"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = openJobs(m)
	reply, _ = runCmd(fire).(orchDoneMsg)
	m, _ = update(m, reply)
	if !strings.Contains(m.jobs.status, "enqueue on alpha-bot @ test-host: run run-1 of poll is already queued") || len(m.orchFires) != 0 {
		t.Errorf("status %q in flight %v", m.jobs.status, m.orchFires)
	}
	if !strings.Contains(m.status, "error: enqueue on alpha-bot @ test-host") {
		t.Errorf("footer = %q", m.status)
	}
}

// The confirm path (dedupe off + a known open run) goes through a message hop; it must
// take a token and its reply must release the guard like a direct fire.
func TestEnqueueNowConfirmPathTakesAndReleasesAToken(t *testing.T) {
	a := &fakeDaemon{orchRuns: &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "run-q", EventKind: pb.EventKind_EVENT_KIND_TIMED, Origin: "tev-1"}}}}
	m := eventsModel(t, &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true, DedupeDisabled: true}, a)
	m, cmd := update(m, press("n"))
	if m.screen != screenConfirm || cmd != nil {
		t.Fatalf("expected the confirm dialog; screen %v", m.screen)
	}
	m, cmd = update(m, press("y"))
	m, fire := update(m, runCmd(cmd)) // orchFireStartMsg -> startOrchFire
	if fire == nil || m.orchFires[aKey(m)] == 0 {
		t.Fatalf("the confirmed fire must take a token; in flight %v", m.orchFires)
	}
	// A second accepted start for the same orchestrator is refused.
	m2, c := m.startOrchFire(orchFireStartMsg{sink: surfaceJobs, target: m.jobs.orchs[0].target, evID: "tev-1", name: "poll"})
	if c != nil || m2.(Model).jobs.status != enqueueInProgressText {
		t.Error("a second start while one is in flight is refused")
	}
	m = feed2(m, fire)
	if len(m.orchFires) != 0 || m.jobs.lastFireAt.IsZero() {
		t.Errorf("the reply must release the guard and the record; in flight %v", m.orchFires)
	}
}
