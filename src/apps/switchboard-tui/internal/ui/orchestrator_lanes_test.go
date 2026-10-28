package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SWI-18 lanes in the TUI (contracts/lanes-ui.md).

// lanesDaemon is a daemon whose orchestrator has a lane "frontend" (worker-one
// in it, build-ui exclusive to it), an un-laned worker-two, a reasoning
// sandbox and one dropped worker tag.
func lanesDaemon() *fakeDaemon {
	rec := &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host",
		Lanes: []*pb.Lane{{Id: "ln-fe", Name: "frontend", Description: "UI work"}},
		Actions: []*pb.Action{
			{Name: "build-ui", Type: pb.ActionType_ACTION_TYPE_PROMPT, LaneIds: []string{"ln-fe"}, LaneMode: pb.LaneMode_LANE_MODE_EXCLUSIVE},
			{Name: "deploy", Type: pb.ActionType_ACTION_TYPE_PROMPT},
		},
		DroppedWorkerTags: []*pb.DroppedWorkerTag{{Action: "old", Tag: "gpu"}},
	}
	sum := &pb.OrchestratorSummary{Orchestrator: rec}
	return &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{sum}, orchDetail: &pb.OrchestratorDetail{Summary: sum, Workers: []*pb.WorkerView{
		{SandboxId: "w1", HostId: "h", DisplayName: "worker-one", LaneIds: []string{"ln-fe"}, HostReachable: true},
		{SandboxId: "w2", HostId: "h", DisplayName: "worker-two", HostReachable: true},
		{SandboxId: "r1", HostId: "h", DisplayName: "reasoning-abc", Reasoning: true, HostReachable: true},
	}}}
}

// lanesView opens the detail's Lanes view.
func lanesView(t *testing.T, a *fakeDaemon) Model {
	t.Helper()
	m := openDetail(t, sized(New(a, "/work")))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.orchDetail.view != viewLanes {
		t.Fatalf("left from Workers should reach Lanes, got %v", m.orchDetail.view)
	}
	return m
}

// keys presses each key and feeds what it returns.
func keys(m Model, ks ...tea.KeyMsg) Model {
	for _, k := range ks {
		var cmd tea.Cmd
		m, cmd = update(m, k)
		m = feed2(m, cmd)
	}
	return m
}

var (
	enterKey = tea.KeyMsg{Type: tea.KeyEnter}
	escKey   = tea.KeyMsg{Type: tea.KeyEsc}
	spaceKey = tea.KeyMsg{Type: tea.KeySpace}
)

func TestLanesViewListsAndManagesLanes(t *testing.T) {
	a := lanesDaemon()
	m := lanesView(t, a)
	v := m.View()
	for _, want := range []string{"Lanes", "frontend", "UI work", "members: worker-one", "actions: build-ui (exclusive)", "worker tags dropped on upgrade: old (#gpu) — D dismiss"} {
		if !strings.Contains(v, want) {
			t.Errorf("lanes view missing %q:\n%s", want, v)
		}
	}
	if h := helpText(m.orchDetailHelp()); !strings.Contains(h, "members") || !strings.Contains(h, "dismiss tags") {
		t.Errorf("help = %q", h)
	}

	// a: create; a refusal keeps the form open with every value.
	m = keys(m, press("a"))
	if m.orchDetail.lanes.form == nil || !strings.Contains(m.View(), "New lane") {
		t.Fatal("a should open the lane form")
	}
	m.orchDetail.lanes.formVals.name = "frontend"
	m = keys(m, ctrlS())
	if m.orchDetail.lanes.form == nil || !strings.Contains(m.View(), "already exists") || m.orchDetail.lanes.formVals.name != "frontend" {
		t.Fatalf("a duplicate name keeps the form with the daemon's text:\n%s", m.View())
	}
	m.orchDetail.lanes.formVals.name, m.orchDetail.lanes.formVals.description = "reviewers", "PR review"
	m = keys(m, ctrlS())
	if m.orchDetail.lanes.form != nil || !strings.Contains(a.orchCallsJoined(), `PutLane o-a  reviewers "PR review"`) || !strings.Contains(m.View(), "reviewers") {
		t.Fatalf("create: calls %q\n%s", a.orchCallsJoined(), m.View())
	}
	// esc closes the form.
	m = keys(m, press("a"), escKey)
	if m.orchDetail.lanes.form != nil {
		t.Error("esc closes the lane form")
	}

	// e: pre-filled rename.
	m = keys(m, press("e"))
	if m.orchDetail.lanes.formVals.name != "frontend" || m.orchDetail.lanes.formLane != "ln-fe" || !strings.Contains(m.View(), "Edit lane") {
		t.Fatalf("e pre-fill = %+v", m.orchDetail.lanes.formVals)
	}
	m.orchDetail.lanes.formVals.name = "web"
	m = keys(m, ctrlS())
	if !strings.Contains(a.orchCallsJoined(), `PutLane o-a ln-fe web "UI work"`) {
		t.Errorf("rename calls %q", a.orchCallsJoined())
	}

	// w: members picker — non-reasoning workers, current members ticked.
	m = keys(m, press("w"))
	pk := m.orchDetail.lanes.picker
	if pk == nil || pk.mode != pickLaneMembers || len(pk.rows) != 2 || !pk.ticked["w1"] || pk.ticked["w2"] {
		t.Fatalf("members picker = %+v", pk)
	}
	if v := m.View(); !strings.Contains(v, "Members of web") || !strings.Contains(v, "[x] worker-one @ h") || strings.Contains(v, "reasoning-abc") {
		t.Errorf("members picker view:\n%s", v)
	}
	m = keys(m, press("j"), spaceKey, enterKey)
	if !strings.Contains(a.orchCallsJoined(), "CheckLaneChange o-a SetLaneMembers") || !strings.Contains(a.orchCallsJoined(), "SetLaneMembers o-a ln-fe members=w1,w2 expected=w1") {
		t.Fatalf("members save calls %q", a.orchCallsJoined())
	}
	if m.orchDetail.lanes.picker != nil || !strings.Contains(m.status, "saved members of web") {
		t.Errorf("after save: picker %v status %q", m.orchDetail.lanes.picker != nil, m.status)
	}

	// D dismisses the dropped-tag notice.
	m = keys(m, press("D"))
	if !strings.Contains(a.orchCallsJoined(), "DismissDroppedWorkerTags o-a") || strings.Contains(m.View(), "worker tags dropped") {
		t.Errorf("dismiss: calls %q", a.orchCallsJoined())
	}

	// d: CheckLaneChange, then a confirm listing the impact; y deletes with
	// the shown guard list.
	a.laneCheck = &pb.LaneChangeImpact{Workers: []string{"worker-one"}, Actions: []*pb.LaneActionUse{{Action: "build-ui", Mode: pb.LaneMode_LANE_MODE_EXCLUSIVE}},
		ActionsLeftUnlaned: []string{"build-ui"}, UnlanedActions: []string{"build-ui"}, ReroutedRuns: 2}
	m = keys(m, press("d"))
	if m.screen != screenConfirm {
		t.Fatalf("d should confirm: %v", m.screen)
	}
	body := strings.Join(m.confirm.body, "\n")
	for _, want := range []string{"Workers leave it: worker-one", "Actions using it: build-ui (exclusive)", "Left un-laned: build-ui", "No open-lane worker will be left for: build-ui", "2 queued runs will be re-routed"} {
		if !strings.Contains(body, want) {
			t.Errorf("delete confirm missing %q:\n%s", want, body)
		}
	}
	m = keys(m, press("y"))
	if !strings.Contains(a.orchCallsJoined(), "DeleteLane o-a ln-fe guard=build-ui") || m.screen != screenOrchDetail {
		t.Errorf("delete calls %q screen %v", a.orchCallsJoined(), m.screen)
	}
	// An unused lane gets a plain confirm; a refusal shows on the panel.
	a.laneCheck = nil
	a.queueLaneErr("DeleteLane", status.Error(codes.NotFound, `lane "ln-2" no longer exists`))
	m = keys(m, press("d"))
	if !strings.Contains(strings.Join(m.confirm.body, "\n"), "No worker or action uses it.") {
		t.Errorf("unused lane confirm = %v screen %v view %v status %q lanes %v cursor %d", m.confirm.body, m.screen, m.orchDetail.view, m.orchDetail.lanes.status, m.orchRec().GetLanes(), m.orchDetail.lanes.cursor)
	}
	m = keys(m, press("y"))
	if !strings.Contains(m.View(), "no longer exists") {
		t.Errorf("delete refusal should show:\n%s", m.View())
	}
}

func TestWorkerLanePickerGuardAndStale(t *testing.T) {
	a := lanesDaemon()
	m := openDetail(t, sized(New(a, "/work")))
	v := m.View()
	for _, want := range []string{"lanes: frontend", "open lane"} {
		if !strings.Contains(v, want) {
			t.Errorf("workers view missing lane cell %q:\n%s", want, v)
		}
	}
	if !strings.Contains(helpText(m.orchDetailHelp()), "lanes") {
		t.Error("help offers l lanes")
	}
	// l on worker-two: the worker picker, none ticked.
	m = keys(m, press("j"), press("l"))
	pk := m.orchDetail.lanes.picker
	if pk == nil || pk.mode != pickWorkerLanes || len(pk.selected()) != 0 || !strings.Contains(m.View(), "none ticked = open lane") {
		t.Fatalf("worker picker = %+v\n%s", pk, m.View())
	}
	// The guard fires: confirm, then y saves with the list; the daemon's newer
	// list asks again.
	a.laneCheck = &pb.LaneChangeImpact{UnlanedActions: []string{"deploy"}, UnlanedRuns: []string{"run-1"}}
	a.queueLaneErr("SetWorkerLanes", guardRefusal("deploy", "lint"))
	m = keys(m, spaceKey, enterKey)
	if m.screen != screenConfirm || !strings.Contains(strings.Join(m.confirm.body, " "), "No open-lane worker will be left to run: deploy (and 1 queued run). Save anyway?") {
		t.Fatalf("guard confirm: %v %v", m.screen, m.confirm.body)
	}
	m = keys(m, press("y"))
	if m.screen != screenConfirm || !strings.Contains(strings.Join(m.confirm.body, " "), "deploy, lint") {
		t.Fatalf("a second refusal reopens the confirm: %v %v", m.screen, m.confirm.body)
	}
	m = keys(m, press("y"))
	if !strings.Contains(a.orchCallsJoined(), "SetWorkerLanes o-a w2 lanes=ln-fe expected= guard=deploy,lint") || m.orchDetail.lanes.picker != nil {
		t.Fatalf("calls %q", a.orchCallsJoined())
	}

	// Stale (Aborted): the picker reloads from the fresh detail, cursor kept,
	// with the daemon's message.
	a.laneCheck = nil
	m = keys(m, press("l"))
	a.queueLaneErr("SetWorkerLanes", status.Error(codes.Aborted, "worker-two's lanes changed since the picker opened — reloaded"))
	m = keys(m, spaceKey, enterKey)
	pk = m.orchDetail.lanes.picker
	if pk == nil || !strings.Contains(pk.status, "changed since the picker opened") || !pk.ticked["ln-fe"] || pk.busy {
		t.Fatalf("stale reload = %+v", pk)
	}
	m = keys(m, escKey)
	if m.orchDetail.lanes.picker != nil {
		t.Error("esc closes the picker")
	}
	// A plain refusal stays on the picker.
	m = keys(m, press("l"))
	a.queueLaneErr("CheckLaneChange", errBoom{})
	m = keys(m, enterKey)
	if pk := m.orchDetail.lanes.picker; pk == nil || !strings.Contains(pk.status, "boom") {
		t.Errorf("refusal on the picker = %+v", pk)
	}
	m = keys(m, escKey)

	// Reasoning rows have no lanes; an old daemon has no picker.
	m = keys(m, press("j"), press("l"))
	if !strings.Contains(m.orchDetail.status, "reasoning sandbox — no lanes") {
		t.Errorf("reasoning l: %q", m.orchDetail.status)
	}
	a.noLanes = true
	m = keys(m, press("l"))
	if !strings.Contains(m.orchDetail.status, "predates lanes — update with u") {
		t.Errorf("old daemon l: %q", m.orchDetail.status)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.orchDetail.view == viewLanes || strings.Contains(m.orchViewTabs(), "Lanes") {
		t.Error("without the capability the Lanes view is not offered")
	}
}

// With no lanes, the worker picker offers a: the lane form, then the picker
// again with the new lane ticked (FR-306).
func TestWorkerPickerAddsALane(t *testing.T) {
	a := lanesDaemon()
	a.orchDetail.Summary.Orchestrator.Lanes = nil
	for _, w := range a.orchDetail.Workers {
		w.LaneIds = nil
	}
	m := openDetail(t, sized(New(a, "/work")))
	m = keys(m, press("l"))
	if !strings.Contains(m.View(), "no lanes yet — a to add one") || !strings.Contains(m.View(), "a add a lane") {
		t.Fatalf("empty worker picker:\n%s", m.View())
	}
	m = keys(m, press("a"))
	if m.orchDetail.lanes.form == nil {
		t.Fatal("a opens the lane form")
	}
	m.orchDetail.lanes.formVals.name = "frontend"
	m = keys(m, ctrlS())
	pk := m.orchDetail.lanes.picker
	if pk == nil || len(pk.rows) != 1 || !pk.ticked["ln-1"] {
		t.Fatalf("picker after the lane form = %+v", pk)
	}
}

// A detail reload drops a vanished lane's tick and closes a picker whose
// worker went away.
func TestLanePickerFollowsReloads(t *testing.T) {
	a := lanesDaemon()
	m := openDetail(t, sized(New(a, "/work")))
	m = keys(m, press("l"))
	a.orchDetail.Summary.Orchestrator.Lanes = append(a.orchDetail.Summary.Orchestrator.Lanes, &pb.Lane{Id: "ln-2", Name: "backend"})
	m = feed(m, m.orchDetailLoadCmd())
	if pk := m.orchDetail.lanes.picker; pk == nil || len(pk.rows) != 2 || !pk.ticked["ln-fe"] {
		t.Fatalf("picker after reload = %+v", pk)
	}
	a.orchDetail.Workers = a.orchDetail.Workers[1:]
	m = feed(m, m.orchDetailLoadCmd())
	if m.orchDetail.lanes.picker != nil || !strings.Contains(m.orchDetail.lanes.status, "no longer on alpha-bot") {
		t.Errorf("picker of a detached worker: %+v %q", m.orchDetail.lanes.picker, m.orchDetail.lanes.status)
	}
}

// x on the Workers view folds the guard into the detach confirm.
func TestWorkersViewDetachGuard(t *testing.T) {
	a := lanesDaemon()
	m := openDetail(t, sized(New(a, "/work")))
	a.laneCheck = &pb.LaneChangeImpact{UnlanedActions: []string{"deploy"}}
	m = keys(m, press("j"), press("x"))
	if m.screen != screenConfirm || !strings.Contains(strings.Join(m.confirm.body, " "), "No open-lane worker will be left to run: deploy") {
		t.Fatalf("detach confirm = %v", m.confirm.body)
	}
	m = keys(m, press("y"))
	if !strings.Contains(a.orchCallsJoined(), "DetachWorker o-a w2 guard=deploy") {
		t.Errorf("calls %q", a.orchCallsJoined())
	}
	// Against a daemon without lanes the detach goes ahead as before.
	a.noLanes = true
	m = keys(m, press("x"))
	if m.screen != screenConfirm || strings.Contains(a.orchCallsJoined(), "CheckLaneChange o-a DetachWorker\nDetachWorker o-a w2\n") {
		t.Fatalf("old daemon detach: %v", m.screen)
	}
	m = keys(m, press("y"))
	if !strings.HasSuffix(strings.TrimSpace(a.orchCallsJoined()), "DetachWorker o-a w2") && !strings.Contains(a.orchCallsJoined(), "DetachWorker o-a w2\n") {
		t.Errorf("calls %q", a.orchCallsJoined())
	}
}

func TestActionFormLanes(t *testing.T) {
	a := lanesDaemon()
	a.orchDetail = nil
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsActions
	v := m.View()
	for _, want := range []string{"⇢ frontend · exclusive", "worker tags dropped on upgrade: old (#gpu) — D dismiss"} {
		if !strings.Contains(v, want) {
			t.Errorf("actions tab missing %q:\n%s", want, v)
		}
	}
	if !strings.Contains(helpText(m.jobsHelp()), "dismiss tags") {
		t.Error("help offers D")
	}
	// e on build-ui: the lanes are pre-ticked; a laned save needs no check.
	m = keys(m, press("e"))
	fv := m.jobs.formVals.(*actionFormVals)
	if len(fv.lanes) != 1 || fv.mode != "exclusive" || !strings.Contains(m.View(), "Lanes (prompt only)") || !strings.Contains(helpText(m.jobsHelp()), "manage lanes") {
		t.Fatalf("form = %+v\n%s", fv, m.View())
	}
	fv.mode = "inclusive"
	m = keys(m, ctrlS())
	got := a.putActions[len(a.putActions)-1]
	if got.GetLaneMode() != pb.LaneMode_LANE_MODE_INCLUSIVE || strings.Contains(a.orchCallsJoined(), "CheckLaneChange") {
		t.Errorf("saved %+v, calls %q", got, a.orchCallsJoined())
	}
	// An un-laned save checks first and confirms when the guard fires.
	m = keys(m, press("a"))
	fv = m.jobs.formVals.(*actionFormVals)
	fv.name, fv.body = "lint", "lint it"
	a.laneCheck = &pb.LaneChangeImpact{UnlanedActions: []string{"lint"}}
	m = keys(m, ctrlS())
	if m.screen != screenConfirm {
		t.Fatalf("guard should confirm: %v", m.screen)
	}
	m = keys(m, press("y"))
	if !strings.Contains(a.orchCallsJoined(), "CheckLaneChange o-a PutAction") || !strings.Contains(a.orchCallsJoined(), "PutAction o-a lint guard=lint") || m.jobs.form != nil {
		t.Errorf("calls %q", a.orchCallsJoined())
	}
	a.laneCheck = nil

	// Switching to reason hides the lanes and saves none; switching back
	// keeps the ticks.
	m = keys(m, press("e"))
	fv = m.jobs.formVals.(*actionFormVals)
	fv.lanes = []string{"ln-fe"}
	fv.kind = "reason"
	m, _ = update(m, struct{}{})
	if len(fv.lanes) != 1 || m.jobs.status != reasonLanesText {
		t.Errorf("reason: lanes %v status %q", fv.lanes, m.jobs.status)
	}
	if a := fv.action(nil); a.GetType() != pb.ActionType_ACTION_TYPE_REASON || len(a.GetLaneIds()) != 0 {
		t.Errorf("a reason action saves no lanes: %+v", a)
	}
	fv.kind = "prompt"
	m, _ = update(m, struct{}{})
	if len(fv.lanes) != 1 || m.jobs.status != "" {
		t.Errorf("back to prompt: lanes %v status %q", fv.lanes, m.jobs.status)
	}

	// A lane gone meanwhile: the form stays open, its options reloaded.
	fv.lanes = []string{"ln-fe"}
	a.queueLaneErr("PutAction", status.Error(codes.NotFound, `lane "ln-fe" no longer exists`))
	a.orchestrators[0].Orchestrator.Lanes = nil
	m = keys(m, ctrlS())
	if m.jobs.form == nil || len(fv.lanes) != 0 || len(fv.options) != 0 || !strings.Contains(m.View(), "no longer exists") {
		t.Fatalf("NotFound: form %v lanes %v options %v\n%s", m.jobs.form != nil, fv.lanes, fv.options, m.View())
	}

	// Against an old daemon the save is refused locally.
	a.noLanes = true
	m = keys(m, ctrlS())
	if !strings.Contains(m.jobs.status, "the daemon on test-host predates lanes — update with u") {
		t.Errorf("old daemon save: %q", m.jobs.status)
	}
	m = keys(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.jobs.manage.open {
		t.Error("ctrl+l needs the capability")
	}
}

// ctrl+l: Manage lanes over the action form; esc returns with every value and
// the options rebuilt (FR-317).
func TestManageLanesFromTheActionForm(t *testing.T) {
	a := lanesDaemon()
	a.orchDetail = nil
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsActions
	m = keys(m, press("e"))
	fv := m.jobs.formVals.(*actionFormVals)
	m = keys(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if !m.jobs.manage.open || !strings.Contains(m.View(), "Manage lanes") || !strings.Contains(m.View(), "frontend") {
		t.Fatalf("manage dialog:\n%s", m.View())
	}
	if !strings.Contains(helpText(m.jobsHelp()), "back to the form") {
		t.Errorf("help = %q", helpText(m.jobsHelp()))
	}
	// Rename frontend and add a lane, both saved as made.
	m = keys(m, press("e"))
	m.jobs.manage.panel.formVals.name = "web"
	m = keys(m, ctrlS())
	m = keys(m, press("a"))
	m.jobs.manage.panel.formVals.name = "backend"
	m = keys(m, ctrlS())
	if !strings.Contains(a.orchCallsJoined(), "PutLane o-a ln-fe web") || !strings.Contains(a.orchCallsJoined(), "PutLane o-a  backend") {
		t.Fatalf("calls %q", a.orchCallsJoined())
	}
	m = keys(m, press("r"))
	m = keys(m, escKey)
	if m.jobs.manage.open || m.jobs.form == nil || m.jobs.formVals.(*actionFormVals) != fv || len(fv.options) != 2 || fv.options[0].GetName() != "web" {
		t.Fatalf("back on the form: %+v", fv)
	}
	// A deleted lane's tick is dropped.
	m = keys(m, tea.KeyMsg{Type: tea.KeyCtrlL}, press("d"), press("y"), escKey)
	fv = m.jobs.formVals.(*actionFormVals)
	if len(fv.lanes) != 0 || len(fv.options) != 1 {
		t.Errorf("after deleting the ticked lane: %+v", fv)
	}
}

func TestAttachLaneStep(t *testing.T) {
	m, a, _ := attachEnv(t)
	a.orchestrators[0].Orchestrator.Lanes = []*pb.Lane{{Id: "ln-1", Name: "frontend"}, {Id: "ln-2", Name: "backend"}}
	m = feed(m, m.orchLoadCmd())
	m = selectSandbox(m, "sa")
	m = keys(m, press("o"), enterKey)
	if m.attach.lanes == nil || m.screen != screenAttachPick || !strings.Contains(m.View(), "frontend") {
		t.Fatalf("lane step:\n%s", m.View())
	}
	if !strings.Contains(helpText(m.attachPickHelp()), "toggle") {
		t.Error("lane step help")
	}
	// A lane deleted meanwhile: the step reopens reloaded with the message.
	a.queueLaneErr("AttachWorker", status.Error(codes.NotFound, `lane "ln-2" no longer exists`))
	a.orchestrators[0].Orchestrator.Lanes = a.orchestrators[0].Orchestrator.Lanes[:1]
	m = keys(m, press("j"), spaceKey, enterKey)
	if m.attach.lanes == nil || len(m.attach.lanes.rows) != 1 || !strings.Contains(m.attach.lanes.status, "no longer exists") {
		t.Fatalf("after NotFound: %+v", m.attach.lanes)
	}
	m = keys(m, spaceKey, enterKey)
	if !strings.Contains(a.orchCallsJoined(), "AttachWorker o-a hosta sa host= lanes=ln-1") {
		t.Errorf("calls %q", a.orchCallsJoined())
	}
	// esc on the step cancels.
	m = keys(m, press("o"), enterKey, escKey)
	if m.screen != screenList || m.attach.lanes != nil {
		t.Errorf("esc: screen %v", m.screen)
	}
}

// The sandbox's daemon predates lanes: confirm, y attaches to the open lane.
func TestAttachPredatesLanes(t *testing.T) {
	a := &fakeDaemon{}
	b := &fakeDaemon{noLanes: true, sandboxes: []*pb.Sandbox{{Id: "sb", DisplayName: "sand-b", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}}
	a.orchestrators = []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "review-bot", HostId: "hosta", Hosts: []*pb.OrchestratorHost{{HostId: "hostb"}}, Lanes: []*pb.Lane{{Id: "ln-1", Name: "frontend"}}}}}
	m, _, _ := twoFakeHosts(t, a, b)
	m = feed2(m, m.listDataCmd())
	m = feed(m, m.orchLoadCmd())
	m.refreshListItems()
	m = selectSandbox(m, "sb")
	m = keys(m, press("o"), enterKey)
	if m.screen != screenConfirm || !strings.Contains(m.confirm.title, "predates lanes: attach to the open lane?") {
		t.Fatalf("predates confirm: %v %q", m.screen, m.confirm.title)
	}
	m = keys(m, press("y"))
	if !strings.Contains(a.orchCallsJoined(), "AttachWorker o-a hostb sb") || strings.Contains(a.orchCallsJoined(), "sb host=") {
		t.Errorf("calls %q", a.orchCallsJoined())
	}
}

func TestLaunchLaneStep(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{hostID: "hosta", orchestrators: []*pb.OrchestratorSummary{
		{Orchestrator: &pb.Orchestrator{Id: "o-here", Name: "here-bot", HostId: "hosta", Lanes: []*pb.Lane{{Id: "ln-1", Name: "frontend"}, {Id: "ln-2", Name: "backend"}}}},
	}}
	m := sized(New(d, root))
	m = feed(m, m.orchLoadCmd())
	m = keys(m, press("n"), press("O"), press("j"), enterKey)
	if m.launch.lanePick == nil || !strings.Contains(m.View(), "Lanes for the new worker of here-bot") {
		t.Fatalf("lane step:\n%s", m.View())
	}
	m = keys(m, spaceKey, enterKey)
	if len(m.launch.orchLanes) != 1 || !strings.Contains(strings.Join(strings.Fields(m.View()), " "), "(lanes: frontend)") {
		t.Fatalf("title should name the lanes:\n%s", m.View())
	}
	// A refused bundle keeps the wizard open on the lane step, reloaded.
	d.queueLaneErr("GetWorkerBundle", status.Error(codes.NotFound, `lane "ln-1" no longer exists`))
	d.orchestrators[0].Orchestrator.Lanes = d.orchestrators[0].Orchestrator.Lanes[1:]
	m = keys(m, spaceKey, enterKey)
	if m.screen != screenLaunch || m.launch.lanePick == nil || len(m.launch.lanePick.rows) != 1 || !strings.Contains(m.launch.lanePick.status, "no longer exists") {
		t.Fatalf("after the refusal: screen %v pick %+v", m.screen, m.launch.lanePick)
	}
	// Pick backend; the bundle is fetched before the wizard closes, for the
	// target's host id.
	m = keys(m, spaceKey, enterKey, enterKey)
	if !strings.Contains(d.orchCallsJoined(), "GetWorkerBundle o-here host=hosta lanes=ln-2") {
		t.Fatalf("calls %q", d.orchCallsJoined())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		got := d.lastController
		d.mu.Unlock()
		if got != nil {
			if m.screen != screenList {
				t.Errorf("the wizard closes once the bundle is in: %v", m.screen)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the launch never carried the controller")
}

// esc on the lane step drops the orchestrator; a predates-lanes target offers
// the open lane.
func TestLaunchLaneStepEscAndOldDaemon(t *testing.T) {
	root := t.TempDir()
	d := &fakeDaemon{hostID: "hosta", orchestrators: []*pb.OrchestratorSummary{
		{Orchestrator: &pb.Orchestrator{Id: "o-here", Name: "here-bot", HostId: "hosta", Lanes: []*pb.Lane{{Id: "ln-1", Name: "frontend"}}}},
	}}
	m := sized(New(d, root))
	m = feed(m, m.orchLoadCmd())
	m = keys(m, press("n"), press("O"), press("j"), enterKey, escKey)
	if m.launch.lanePick != nil || m.launch.orchestrator != nil || m.screen != screenLaunch {
		t.Fatalf("esc on the lane step: %+v", m.launch.orchestrator)
	}
	d.noLanes = true
	m = keys(m, press("O"), press("j"), enterKey)
	if m.screen != screenConfirm || !strings.Contains(m.confirm.title, "predates lanes") {
		t.Fatalf("predates confirm: %v pick %v orch %v progress %q lanepick %v has %v lanes %v", m.screen, m.launch.orchPick, m.launch.orchestrator, m.launch.progress, m.launch.lanePick, m.hostHasLanes(m.launch.orchestrator.host), m.launch.orchestrator.summary.GetOrchestrator().GetLanes())
	}
	m = keys(m, press("y"))
	if m.screen != screenLaunch || m.launch.orchestrator == nil || len(m.launch.orchLanes) != 0 {
		t.Errorf("y: screen %v orch %v", m.screen, m.launch.orchestrator)
	}
}

func TestThirdLineLanes(t *testing.T) {
	c := &pb.SandboxController{Name: "pdev-orch"}
	sb := &pb.Sandbox{Controller: c}
	if got := thirdLine(sb); got != "orchestrator: pdev-orch" {
		t.Errorf("lanes unknown = %q", got)
	}
	c.LanesKnown = true
	if got := thirdLine(sb); got != "orchestrator: pdev-orch · open lane" {
		t.Errorf("open lane = %q", got)
	}
	c.LaneNames = []string{"frontend", "re\x1b[31mviewers"}
	if got := thirdLine(sb); got != "orchestrator: pdev-orch · lanes: frontend, reviewers" && !strings.HasPrefix(got, "orchestrator: pdev-orch · lanes: frontend, re") {
		t.Errorf("lanes = %q", got)
	} else if strings.Contains(got, "\x1b") {
		t.Errorf("lane names are sanitised: %q", got)
	}
	c.LanesPending = true
	if got := thirdLine(sb); got != "orchestrator: pdev-orch · lanes pending" {
		t.Errorf("pending = %q", got)
	}
	c.Reasoning = true
	if got := thirdLine(sb); got != "orchestrator: pdev-orch (reasoning)" {
		t.Errorf("reasoning = %q", got)
	}
}

func TestQueuedRunShowsWaitingReason(t *testing.T) {
	m, _ := update(New(&fakeDaemon{}, "/work"), tea.WindowSizeMsg{Width: 180, Height: 40})
	run := &pb.Run{Id: "run-1", Action: "build-ui", State: pb.RunState_RUN_STATE_QUEUED, Position: 3, WaitingReason: "waiting for a worker in frontend"}
	row := jobsRunRow{run: run, target: orchTarget{name: "alpha", hostName: "h"}}
	if got := m.jobsRunLine(row); !strings.Contains(got, "· waiting for a worker in frontend") {
		t.Errorf("row = %q", got)
	}
	run.WaitingReason = strings.Repeat("waiting for X, ", 20)
	if got := m.jobsRunLine(row); !strings.Contains(got, "+") {
		t.Errorf("a cut reason ends +: %q", got)
	}
	if got := m.promptView(run); !strings.Contains(got, "waiting for X") {
		t.Errorf("the prompt overlay shows the reason: %q", got)
	}
}

func TestEventsDroppedReloadsTheList(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.listLoading = false
	// Jobs knows an orchestrator on this host (off screen: a reload marks it
	// stale) and one on another host (untouched).
	m.jobs.started = true
	hk := m.subHostKey("")
	m.jobs.metaOf(hk, "o-a")
	m.jobs.metaOf("elsewhere", "o-b")
	out, cmd := m.handleEvent("", &pb.Event{Event: &pb.Event_EventsDropped{EventsDropped: &pb.EventsDropped{}}})
	mm := out.(Model)
	if !mm.listLoading || cmd == nil {
		t.Error("events_dropped should reload the list")
	}
	if !mm.jobs.meta[hk+"/o-a"].stale || mm.jobs.meta["elsewhere/o-b"].stale {
		t.Errorf("events_dropped should reload this host's Jobs orchestrators only: %+v", mm.jobs.meta)
	}
}

func TestLanePickerUnit(t *testing.T) {
	var rows []pickRow
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		rows = append(rows, pickRow{id: "ln-" + n, label: n})
	}
	p := newLanePicker(pickLaneMembers, "Members", "x", rows, []string{"ln-b"})
	for range 7 {
		p.update(press("j"))
	}
	p.update(press("j")) // stops at the end
	if p.cursor != 7 {
		t.Fatalf("cursor = %d", p.cursor)
	}
	v := p.view(60, 10)
	if !strings.Contains(v, "↑ more") || !strings.Contains(v, "[ ] h") {
		t.Errorf("scrolled view:\n%s", v)
	}
	p.update(press("k"))
	p.update(spaceKey)
	if got := p.selected(); len(got) != 2 || got[0] != "ln-b" || got[1] != "ln-g" {
		t.Errorf("selected = %v", got)
	}
	// reload keeps the cursor on the same id.
	p.reload(rows[3:], []string{"ln-e"}, false)
	if p.rows[p.cursor].id != "ln-g" || !p.ticked["ln-e"] || p.ticked["ln-b"] {
		t.Errorf("reload: cursor %d ticked %v", p.cursor, p.ticked)
	}
	for range 8 {
		p.update(press("k"))
	}
	if v := p.view(60, 10); !strings.Contains(v, "↓ more") {
		t.Errorf("view at the top:\n%s", v)
	}
	// busy takes only esc.
	p.busy = true
	if p.update(spaceKey) != pickNone || p.update(escKey) != pickClose || !strings.Contains(p.view(60, 40), "saving…") {
		t.Error("busy picker")
	}
	for _, mode := range []pickMode{pickAttach, pickLaunch, pickLaneMembers} {
		e := newLanePicker(mode, "t", "", nil, nil)
		if !strings.Contains(e.view(60, 20), "(no") {
			t.Errorf("empty %v view", mode)
		}
	}
	if got := capNames([]string{"a", "b", "c", "d", "e", "f", "g"}); got != "a, b, c, d, e, and 2 more" {
		t.Errorf("capNames = %q", got)
	}
	if got := guardText(&pb.LaneChangeImpact{UnlanedRuns: []string{"r1", "r2"}}); got != "2 queued runs" {
		t.Errorf("guardText runs only = %q", got)
	}
	if modeWord(pb.LaneMode_LANE_MODE_INCLUSIVE) != "inclusive" {
		t.Error("modeWord")
	}
}

// A guarded write's result opens a confirm only where it started; elsewhere it
// lands on the footer, and a late result never touches another orchestrator's
// picker (PR #24 review).
func TestLaneResultsStayWithTheirOrigin(t *testing.T) {
	a := lanesDaemon()
	m := openDetail(t, sized(New(a, "/work")))
	a.laneCheck = &pb.LaneChangeImpact{UnlanedActions: []string{"deploy"}}
	m, _ = update(m, press("j"))
	m, cmd := update(m, press("x"))
	if cmd == nil || m.screen == screenConfirm {
		t.Fatalf("x should start the check: %v", m.screen)
	}
	msg := cmd()
	m.screen = screenList // the user moved on before the check came back
	m, _ = update(m, msg)
	if m.screen != screenList || !strings.Contains(m.status, "was interrupted — not applied") {
		t.Fatalf("late check: screen %v status %q", m.screen, m.status)
	}
	if strings.Contains(a.orchCallsJoined(), "DetachWorker o-a w2") {
		t.Errorf("nothing should be detached: %q", a.orchCallsJoined())
	}

	// A late success for another orchestrator leaves this picker open.
	m.screen = screenOrchDetail
	m = keys(m, press("l"))
	if m.orchDetail.lanes.picker == nil {
		t.Fatal("l should open the worker's lane picker")
	}
	other := laneWrite{sink: surfaceDetail, target: orchTarget{host: "elsewhere", id: "o-z", name: "zeta"}, closePicker: true, done: "saved"}
	m, _ = update(m, laneResultMsg{w: other})
	if m.orchDetail.lanes.picker == nil {
		t.Error("another orchestrator's result closed this picker")
	}
	m, _ = update(m, laneResultMsg{w: other, err: errors.New("boom")})
	if p := m.orchDetail.lanes.picker; p == nil || p.status != "" || m.orchDetail.lanes.stale != "" {
		t.Errorf("another orchestrator's refusal landed on this picker: %+v", p)
	}
	// So does a late success of an earlier picker on this same orchestrator.
	earlier := laneWrite{sink: surfaceDetail, target: m.detailTarget(), closePicker: true, done: "saved", owner: &lanePicker{}}
	m, _ = update(m, laneResultMsg{w: earlier})
	if m.orchDetail.lanes.picker == nil {
		t.Error("an earlier picker's result closed this one")
	}
}

// Declining the guard on an action form completed by tab returns to an
// editable form, not one that re-submits on every key.
func TestDeclinedGuardKeepsTheFormEditable(t *testing.T) {
	a := lanesDaemon()
	a.orchDetail = nil
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsActions
	m = keys(m, press("j"), press("e")) // deploy: un-laned
	fv, ok := m.jobs.formVals.(*actionFormVals)
	if !ok || fv.name != "deploy" {
		t.Fatalf("form = %+v", m.jobs.formVals)
	}
	a.laneCheck = &pb.LaneChangeImpact{UnlanedActions: []string{"deploy"}}
	// Tabbed past the last field: huh completes the form, which submits.
	m.jobs.form.State = huh.StateCompleted
	m, cmd := update(m, struct{}{})
	m = feed2(m, cmd)
	if m.screen != screenConfirm {
		t.Fatalf("guard should confirm: %v", m.screen)
	}
	m = keys(m, press("n"))
	if m.screen != screenJobs || m.jobs.form == nil || m.jobs.form.State != huh.StateNormal {
		t.Fatalf("declined: screen %v form %v", m.screen, m.jobs.form)
	}
	m = keys(m, press("x"))
	if m.screen == screenConfirm {
		t.Error("a key on the form re-submitted it")
	}
}

// Lane form saves are numbered model-wide: a reply to a save made before the
// panel was rebuilt never closes a newer form (PR #24 re-review).
func TestLaneFormSaveNumbersOutliveThePanel(t *testing.T) {
	a := lanesDaemon()
	m := lanesView(t, a)
	m = keys(m, press("a"))
	m.orchDetail.lanes.formVals.name = "x"
	m, cmd := m.submitLaneForm(surfaceDetail)
	old := cmd().(laneFormMsg)
	m.orchDetail.lanes = lanesPanel{} // the detail was re-entered
	m = keys(m, press("a"))
	m.orchDetail.lanes.formVals.name = "y"
	m, _ = m.submitLaneForm(surfaceDetail)
	out, _ := m.applyLaneForm(old)
	if p := out.(Model).orchDetail.lanes; p.form == nil || !p.saving || p.formVals.name != "y" {
		t.Errorf("an old reply closed the newer form: %+v", p)
	}
}

// The reason-lanes note never covers a refusal on the form's line.
func TestReasonNoteKeepsARefusal(t *testing.T) {
	a := lanesDaemon()
	a.orchDetail = nil
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsActions
	m = keys(m, press("e"))
	fv := m.jobs.formVals.(*actionFormVals)
	fv.kind = "reason"
	m.jobs.status = "body: unknown placeholder {x}"
	m, _ = update(m, struct{}{})
	if m.jobs.status != "body: unknown placeholder {x}" {
		t.Errorf("status = %q", m.jobs.status)
	}
	m.jobs.status = ""
	m, _ = update(m, struct{}{})
	if m.jobs.status != reasonLanesText {
		t.Errorf("status = %q, want the note", m.jobs.status)
	}
	// Its ticks gone (a lane deleted elsewhere), the note goes too.
	fv.lanes = nil
	m, _ = update(m, struct{}{})
	if m.jobs.status != "" {
		t.Errorf("status = %q after the ticks went", m.jobs.status)
	}
}

// The Lanes window marks hidden lanes by position, so lanes that render
// alike still get their marks.
func TestLanesWindowMarksHiddenLanes(t *testing.T) {
	a := lanesDaemon()
	rec := a.orchDetail.GetSummary().GetOrchestrator()
	rec.Lanes = nil
	for i := range 12 {
		rec.Lanes = append(rec.Lanes, &pb.Lane{Id: fmt.Sprintf("ln-%d", i), Name: fmt.Sprintf("frontend-review-%c", 'a'+i)})
	}
	m := lanesView(t, a)
	m.height = 20
	m.orchDetail.lanes.cursor = 6
	v := strings.Join(m.lanesView(surfaceDetail), "\n")
	if !strings.Contains(v, "↑ more") || !strings.Contains(v, "↓ more") {
		t.Errorf("window marks missing:\n%s", v)
	}
	m.orchDetail.lanes.cursor = 0
	if v := strings.Join(m.lanesView(surfaceDetail), "\n"); strings.Contains(v, "↑ more") || !strings.Contains(v, "↓ more") {
		t.Errorf("top window:\n%s", v)
	}
}
