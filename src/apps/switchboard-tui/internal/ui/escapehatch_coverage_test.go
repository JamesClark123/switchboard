package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// A valid escape-hatch command saves and reloads from the store.
func TestSaveKitWithEscapeHatchPersists(t *testing.T) {
	kit := &store.Kit{Name: "ok", EscapeHatch: []store.KitEscapeHatchCommand{
		{Name: "install-deps", Command: "pnpm install", WhenToUse: "deps"},
	}}
	out := editorOn(t, kit)
	out2, _ := out.saveKit()
	m := asModel(out2)
	stored, err := m.kits.Get("ok")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.EscapeHatch) != 1 || stored.EscapeHatch[0].Name != "install-deps" {
		t.Errorf("escape-hatch command should persist on save, got %+v", stored.EscapeHatch)
	}
}

// sandboxLabelAndHost falls back to the id + active host for an unknown sandbox.
func TestSandboxLabelAndHostFallback(t *testing.T) {
	m := runningSbx(&fakeDaemon{})
	name, _ := m.sandboxLabelAndHost("no-such-sandbox")
	if name != "no-such-sandbox" {
		t.Errorf("unknown sandbox should fall back to its id, got %q", name)
	}
}

// A failed decision surfaces an error rather than a status.
func TestDecideRunCmdError(t *testing.T) {
	d := &fakeDaemon{decideErr: errors.New("boom")}
	cmd := decideRunCmd(d, "ehr-1", true)
	if _, ok := cmd().(errMsg); !ok {
		t.Error("a decide failure should produce an errMsg")
	}
}

// Navigating the run list moves the selection.
func TestRunsScreenNavigation(t *testing.T) {
	d := &fakeDaemon{runs: []*pb.EscapeHatchRun{
		{Id: "ehr-1", SandboxId: "sb1", CommandName: "a", Command: "true", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED},
		{Id: "ehr-2", SandboxId: "sb1", CommandName: "b", Command: "true", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED},
	}}
	m := runningSbx(d)
	out, cmd := update(m, press("E"))
	out, _ = update(out, runCmd(cmd))
	// down then enter shows the second run's detail.
	out, _ = update(out, press("down"))
	out, _ = update(out, press("enter"))
	if !strings.Contains(out.viewRuns(), "failed") {
		t.Errorf("navigation + enter should show the highlighted run's detail; got:\n%s", out.viewRuns())
	}
}

// Rendering the full view for the approval and runs screens exercises their help
// footers and chrome (approvalHelp / runsHelp).
func TestApprovalAndRunsScreensRenderFully(t *testing.T) {
	d := &fakeDaemon{runs: []*pb.EscapeHatchRun{
		{Id: "ehr-1", SandboxId: "sb1", CommandName: "x", Command: "true", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED},
	}}
	m := runningSbx(d)

	// Approval screen full render.
	tm, _ := m.handleEscapeHatchRun(pendingRun())
	if v := asModel(tm).View(); !strings.Contains(v, "approve") {
		t.Errorf("approval view should render its help footer; got:\n%s", v)
	}

	// Runs screen full render (chrome + help footer).
	out, cmd := update(m, press("E"))
	out, _ = update(out, runCmd(cmd))
	if v := out.View(); !strings.Contains(v, "output") {
		t.Errorf("runs view should render its help footer; got:\n%s", v)
	}
}

// notifTitle/notifIcon must render every notification kind, including the two new
// escape-hatch kinds.
func TestNotifTitleAndIconAllKinds(t *testing.T) {
	kinds := []pb.NotificationKind{
		pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE,
		pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING,
		pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_NEEDS_APPROVAL,
		pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_RUN_COMPLETE,
	}
	for _, k := range kinds {
		if notifTitle(k) == "" {
			t.Errorf("notifTitle(%v) empty", k)
		}
		if notifIcon(k) == "" {
			t.Errorf("notifIcon(%v) empty", k)
		}
	}
}

// An escape-hatch notification event flows through handleEvent into the inbox.
func TestEscapeHatchNotificationEntersInbox(t *testing.T) {
	m := runningSbx(&fakeDaemon{})
	ev := &pb.Event{Event: &pb.Event_Notification{Notification: &pb.NotificationEvent{
		Id: "n1", SandboxId: "sb1", Kind: pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_RUN_COMPLETE, Message: "run finished",
	}}}
	tm, _ := m.handleEvent(ev)
	out := asModel(tm)
	if len(out.inbox) != 1 || out.unread != 1 {
		t.Errorf("escape-hatch notification should enter the inbox, inbox=%d unread=%d", len(out.inbox), out.unread)
	}
	// Render the inbox so its item titles/icons for the new kind are exercised.
	tm2, _ := out.enterNotifications()
	if v := asModel(tm2).viewNotifications(); !strings.Contains(v, "escape-hatch") {
		t.Errorf("inbox should show the escape-hatch notification; got:\n%s", v)
	}
}

// runStatusText/Icon must render every status.
func TestRunStatusRendering(t *testing.T) {
	all := []pb.EscapeHatchRunStatus{
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_TIMED_OUT,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_UNSPECIFIED,
	}
	for _, st := range all {
		if runStatusText(st) == "" {
			t.Errorf("runStatusText(%v) empty", st)
		}
		if runStatusIcon(st) == "" {
			t.Errorf("runStatusIcon(%v) empty", st)
		}
	}
}

// saveKit blocks on an invalid escape-hatch command and does not persist it.
func TestSaveKitRejectsInvalidEscapeHatch(t *testing.T) {
	kit := &store.Kit{Name: "k", EscapeHatch: []store.KitEscapeHatchCommand{
		{Name: "Bad Name", Command: "true", WhenToUse: "x"}, // non-kebab name
	}}
	out := editorOn(t, kit)
	out2, _ := out.saveKit()
	m := asModel(out2)
	if !strings.Contains(m.kitEditor.status, "kebab") {
		t.Errorf("save should report the invalid escape-hatch name, got %q", m.kitEditor.status)
	}
}

// Editing an existing approval-gated command with a max duration round-trips the
// value, and an invalid max duration is rejected.
func TestEscapeHatchMaxDurationParsing(t *testing.T) {
	kit := &store.Kit{Name: "k", EscapeHatch: []store.KitEscapeHatchCommand{
		{Name: "e2e", Command: "pnpm test:e2e", WhenToUse: "x", RequiresApproval: true, MaxDurationSecs: 600},
	}}
	out := editorOn(t, kit)
	out = navToEscapeHatch(t, out)
	out = openEHForm(t, out, 0) // edit existing
	if out.kitEditor.vals.ehMaxDuration != "600" {
		t.Errorf("edit form should show the existing max duration, got %q", out.kitEditor.vals.ehMaxDuration)
	}

	// A non-numeric duration is rejected.
	out.kitEditor.vals.ehName = "e2e"
	out.kitEditor.vals.itemCommand = "pnpm test:e2e"
	out.kitEditor.vals.ehMaxDuration = "soon"
	out2, _ := out.applyKitForm()
	if !strings.Contains(asModel(out2).kitEditor.status, "duration") {
		t.Errorf("invalid duration should be rejected, status = %q", asModel(out2).kitEditor.status)
	}
}

// The runs and approval help footers implement help.KeyMap.
func TestRunsAndApprovalHelpBindings(t *testing.T) {
	m := runningSbx(&fakeDaemon{})
	for _, hb := range []helpBindings{m.runsHelp(), m.approvalHelp()} {
		if len(hb.ShortHelp()) == 0 {
			t.Error("help bindings should expose ShortHelp")
		}
		if len(hb.FullHelp()) == 0 {
			t.Error("help bindings should expose FullHelp")
		}
	}
}

// The badge returns empty for a sandbox with no pending run.
func TestEscapeHatchBadgeEmptyWhenNoPending(t *testing.T) {
	m := runningSbx(&fakeDaemon{})
	if b := m.escapeHatchBadge("sb1"); b != "" {
		t.Errorf("no pending run should yield no badge, got %q", b)
	}
}

// listRunsCmd surfaces a daemon error as an errMsg.
func TestListRunsCmdError(t *testing.T) {
	d := &fakeDaemon{listRunsErr: errors.New("nope")}
	m := runningSbx(d)
	cmd := m.listRunsCmd(d, "sb1")
	if _, ok := cmd().(errMsg); !ok {
		t.Error("a list-runs failure should produce an errMsg")
	}
}

// applyRuns is a no-op when the screen or sandbox no longer matches.
func TestApplyRunsIgnoresStaleResult(t *testing.T) {
	m := runningSbx(&fakeDaemon{})
	m.screen = screenList // not screenRuns
	if _, cmd := m.applyRuns(runsLoadedMsg{sandboxID: "sb1"}); cmd != nil {
		t.Error("applyRuns should ignore a result when not on the runs screen")
	}
}

// updateRunsKey routes navigation to the embedded list and q returns to the list.
func TestRunsScreenQuits(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)
	out, cmd := update(m, press("E"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("q"))
	if out.screen != screenList {
		t.Errorf("q should return to the list, screen = %v", out.screen)
	}
}

// The editor's section list shows the escape-hatch section with its command count.
func TestEditorViewShowsEscapeHatchCount(t *testing.T) {
	kit := &store.Kit{Name: "k", EscapeHatch: []store.KitEscapeHatchCommand{
		{Name: "a", Command: "true", WhenToUse: "x"},
		{Name: "b", Command: "true", WhenToUse: "y"},
	}}
	out := editorOn(t, kit)
	if v := out.View(); !strings.Contains(v, "Escape-hatch commands") || !strings.Contains(v, "2") {
		t.Errorf("editor section list should show the escape-hatch count; got:\n%s", v)
	}
}

// The escape-hatch section view renders each command's item label.
func TestEscapeHatchItemLabelRendered(t *testing.T) {
	kit := &store.Kit{Name: "k", EscapeHatch: []store.KitEscapeHatchCommand{
		{Name: "install-deps", Command: "pnpm install", WhenToUse: "x", RequiresApproval: false},
		{Name: "deploy", Command: "./deploy.sh", WhenToUse: "y", RequiresApproval: true},
	}}
	out := editorOn(t, kit)
	out = navToEscapeHatch(t, out)
	v := out.View()
	for _, want := range []string{"install-deps", "auto", "deploy", "approval"} {
		if !strings.Contains(v, want) {
			t.Errorf("escape-hatch section view missing %q; got:\n%s", want, v)
		}
	}
}
