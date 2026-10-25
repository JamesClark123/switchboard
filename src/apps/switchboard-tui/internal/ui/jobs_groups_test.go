package ui

import (
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// groupsHosts wires alpha-bot (one action, one event) on alpha and an empty
// beta-bot on beta.
func groupsHosts(t *testing.T) (Model, *fakeDaemon, *fakeDaemon) {
	t.Helper()
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", Actions: []*pb.Action{reviewAction()},
		TimedEvents: []*pb.TimedEvent{{Id: "tev-a", Name: "poll", Action: "review-pr", IntervalSeconds: 300, Enabled: true}}}}}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, _, _ := twoFakeHosts(t, a, b)
	return openJobs(m), a, b
}

// US4: actions are grouped per orchestrator with its host; an empty group still
// takes an add, which lands on that orchestrator; call/edit/delete act on the
// highlighted action's orchestrator.
func TestJobsActionsGroupedAcrossHosts(t *testing.T) {
	m, a, b := groupsHosts(t)
	m.jobs.tab = jobsActions
	v := m.View()
	ai, bi := strings.Index(v, "alpha-bot @ alpha"), strings.Index(v, "beta-bot @ beta")
	if ai < 0 || bi < ai || !strings.Contains(v, "review-pr") || !strings.Contains(v, "no actions — press a to add one") {
		t.Fatalf("groups in order with an empty-state line:\n%s", v)
	}
	if h := m.renderHelp(m.jobsHelp()); !strings.Contains(h, "call") || !strings.Contains(h, "edit") {
		t.Errorf("an action row's help = %q", h)
	}
	// Move onto beta's empty group: only add is offered; e/d/enter do nothing.
	m, _ = update(m, press("j"))
	if h := m.renderHelp(m.jobsHelp()); !strings.Contains(h, "add") || strings.Contains(h, "call") {
		t.Errorf("an empty group's help = %q", h)
	}
	for _, k := range []string{"e", "d", "enter"} {
		if mm, c := update(m, press(k)); c != nil || mm.jobs.form != nil || mm.screen != screenJobs {
			t.Errorf("%q on an empty group is a no-op", k)
		}
	}
	m, _ = update(m, press("a"))
	if m.jobs.form == nil || m.jobs.formTarget.host != "hostb" || m.jobs.formTarget.id != "o-b" {
		t.Fatalf("a should target beta-bot: %+v", m.jobs.formTarget)
	}
	v2 := m.jobs.formVals.(*actionFormVals)
	v2.name, v2.body = "triage", "Triage {issue}"
	v2.args = "issue *"
	m, cmd := update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "PutAction o-b triage") || strings.Contains(a.orchCallsJoined(), "PutAction") {
		t.Errorf("add should reach beta only: a=%q b=%q", a.orchCallsJoined(), b.orchCallsJoined())
	}
	if !strings.Contains(m.View(), "triage") || strings.Contains(m.View(), "no actions") {
		t.Errorf("beta's group shows the new action:\n%s", m.View())
	}
	// enter calls it on beta.
	m.jobs.cursors[jobsActions] = 1
	m, _ = update(m, press("enter"))
	*m.jobs.formVals.(*callFormVals).values["issue"] = "7"
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "CallAction o-b triage issue=7") {
		t.Errorf("call calls = %q", b.orchCallsJoined())
	}
	// e and d on alpha's action act on alpha.
	m.jobs.cursors[jobsActions] = 0
	m, _ = update(m, press("e"))
	if m.jobs.formTarget.id != "o-a" || m.jobs.formVals.(*actionFormVals).original != "review-pr" {
		t.Error("e edits alpha's action")
	}
	m, _ = update(m, press("esc"))
	m.jobs.form = nil
	m, _ = update(m, press("d"))
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "DeleteAction o-a review-pr") {
		t.Errorf("delete calls = %q", a.orchCallsJoined())
	}
}

// US4: events are grouped the same way; toggle and Enqueue now act on the
// highlighted event's orchestrator, add on the cursor's group.
func TestJobsEventsGroupedAcrossHosts(t *testing.T) {
	m, a, b := groupsHosts(t)
	m.jobs.tab = jobsEvents
	v := m.View()
	if !strings.Contains(v, "alpha-bot @ alpha") || !strings.Contains(v, "[on]") || !strings.Contains(v, "no events — press a to add one") {
		t.Fatalf("events groups:\n%s", v)
	}
	if h := m.renderHelp(m.jobsHelp()); !strings.Contains(h, "on/off") || !strings.Contains(h, "enqueue now") {
		t.Errorf("an event row's help = %q", h)
	}
	m, cmd := update(m, press(" "))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "PutTimedEvent o-a poll dedupe_disabled=false enabled=false") {
		t.Errorf("toggle calls = %q", a.orchCallsJoined())
	}
	m, cmd = update(m, press("n"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "FireTimedEvent o-a tev-a") {
		t.Errorf("enqueue now calls = %q", a.orchCallsJoined())
	}
	// Beta's empty group: a is refused until beta has an action (today's rule),
	// and the other keys do nothing.
	m, _ = update(m, press("j"))
	for _, k := range []string{"e", "d", " ", "n"} {
		if mm, c := update(m, press(k)); c != nil || mm.jobs.form != nil || mm.screen != screenJobs {
			t.Errorf("%q on an empty group is a no-op", k)
		}
	}
	m, _ = update(m, press("a"))
	if m.jobs.form != nil || !strings.Contains(m.jobs.status, "add an action to beta-bot @ beta first") {
		t.Errorf("no actions on beta ⇒ no event form; %q", m.jobs.status)
	}
	if !strings.Contains(m.View(), "add an action to beta-bot @ beta first") {
		t.Error("the refusal shows on the Jobs line")
	}
	// After beta gains an action, a adds an event to beta.
	b.orchestrators[0].Orchestrator.Actions = []*pb.Action{reviewAction()}
	m = feed(m, m.jobsLoadCmd())
	m.jobs.cursors[jobsEvents] = 1
	m, _ = update(m, press("a"))
	if m.jobs.form == nil || m.jobs.formTarget.id != "o-b" {
		t.Fatalf("a on beta's group targets beta: %+v", m.jobs.formTarget)
	}
	ev := m.jobs.formVals.(*eventFormVals)
	ev.name = "nightly"
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "PutTimedEvent o-b nightly") || strings.Contains(a.orchCallsJoined(), "nightly") {
		t.Errorf("add event should reach beta only: a=%q b=%q", a.orchCallsJoined(), b.orchCallsJoined())
	}
	// e / d on alpha's event.
	m.jobs.cursors[jobsEvents] = 0
	m, _ = update(m, press("e"))
	if m.jobs.formTarget.id != "o-a" || m.jobs.formVals.(*eventFormVals).id != "tev-a" {
		t.Error("e edits alpha's event")
	}
	m.jobs.form = nil
	m, _ = update(m, press("d"))
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "DeleteTimedEvent o-a tev-a") {
		t.Errorf("delete calls = %q", a.orchCallsJoined())
	}
}

// A refusal while a form is open on Jobs keeps it open with the text, and a
// refusal after leaving Jobs goes to the footer instead.
func TestJobsFormRefusalRouting(t *testing.T) {
	m, a, _ := groupsHosts(t)
	m.jobs.tab = jobsActions
	m, _ = update(m, press("a"))
	m.jobs.formVals.(*actionFormVals).name = "x"
	a.orchErr = errBoom{}
	m, cmd := update(m, ctrlS())
	msg := runCmd(cmd)
	m.screen = screenList
	m, _ = update(m, msg)
	if strings.Contains(m.jobs.status, "boom") || !strings.HasPrefix(m.status, "error: ") || !strings.Contains(m.status, "boom") {
		t.Errorf("off-screen refusal goes to the footer: jobs %q footer %q", m.jobs.status, m.status)
	}
	// Non-key messages reach an open Jobs form.
	m.screen = screenJobs
	if mm, _ := update(m, struct{}{}); mm.jobs.form == nil {
		t.Error("forwarding a message keeps the form")
	}
	// A failed toggle on a Jobs row shows on the Jobs line.
	m.jobs.form = nil
	m.jobs.tab = jobsEvents
	m, cmd = update(m, press(" "))
	m, _ = update(m, runCmd(cmd))
	if !strings.Contains(m.jobs.status, "boom") {
		t.Errorf("a failed toggle: %q", m.jobs.status)
	}
}
