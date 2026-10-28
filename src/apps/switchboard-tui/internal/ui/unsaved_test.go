package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// noKeyGrace turns the dialog's typing grace off for a test, so a story can
// press a choice right after the dialog opens.
func noKeyGrace(t *testing.T) {
	t.Helper()
	old := unsavedKeyGrace
	unsavedKeyGrace = 0
	t.Cleanup(func() { unsavedKeyGrace = old })
}

var ctrlC = tea.KeyMsg{Type: tea.KeyCtrlC}

// editedReasoningSetup opens the reasoning setup of o-a (saved setup: one
// folder) and edits it: proj added, clone mode on.
func editedReasoningSetup(t *testing.T) (Model, string) {
	t.Helper()
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	a := reasoningFake(true, "")
	a.orchestrators[0].Orchestrator.ReasoningSetup = &pb.ReasoningSetup{Sources: []*pb.SourceRef{{Path: "/old/folder"}}}
	a.orchDetail.Summary = a.orchestrators[0]
	m := openReasoning(t, a, root)
	m, _ = update(m, press("e"))
	if m.screen != screenLaunch || m.launch.reasoning == nil {
		t.Fatal("setup did not open")
	}
	if m.guardDirty(gReasoning) {
		t.Fatal("setup is dirty on open")
	}
	m.launch.selected[proj] = true
	m.launch.order = append(m.launch.order, proj)
	m.launch.cloneMode = true
	return m, proj
}

// openDialog raises the dialog on the edited reasoning setup.
func openDialog(t *testing.T, m Model, g guardSurface) Model {
	t.Helper()
	mm, _ := m.requestExit(g)
	m = mm.(Model)
	if !m.unsaved.open || m.unsaved.surface != g {
		t.Fatalf("dialog not open for %v: %+v", g, m.unsaved)
	}
	return m
}

func TestUnsavedDialogOpensOnCancelAndEnterCancels(t *testing.T) {
	noKeyGrace(t)
	m, proj := editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	if m.unsaved.highlight != unsavedCancel || m.unsaved.saveLabel != "Rebuild" {
		t.Fatalf("dialog = %+v, want Cancel highlighted and a Rebuild label", m.unsaved)
	}
	view := m.View()
	for _, want := range []string{"Unsaved changes", "[ Rebuild ]", "[› Cancel ‹]", "[ Discard ]", "r rebuild · d discard · esc cancel · ←/→ · enter"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog missing %q", want)
		}
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.unsaved.open || m.screen != screenLaunch || !m.launch.selected[proj] || !m.launch.cloneMode {
		t.Fatalf("enter on Cancel must leave the setup untouched: open=%v screen=%v", m.unsaved.open, m.screen)
	}
}

func TestUnsavedDialogHighlightWrapsAndDiscards(t *testing.T) {
	noKeyGrace(t)
	m, _ := editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	steps := []struct {
		key  tea.KeyMsg
		want int
	}{
		{tea.KeyMsg{Type: tea.KeyRight}, unsavedDiscard},
		{press("l"), unsavedSave},
		{tea.KeyMsg{Type: tea.KeyTab}, unsavedCancel},
		{tea.KeyMsg{Type: tea.KeyLeft}, unsavedSave},
		{press("h"), unsavedDiscard},
		{tea.KeyMsg{Type: tea.KeyShiftTab}, unsavedCancel},
	}
	for i, s := range steps {
		m, _ = update(m, s.key)
		if m.unsaved.highlight != s.want {
			t.Fatalf("step %d (%s): highlight = %d, want %d", i, s.key, m.unsaved.highlight, s.want)
		}
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.unsaved.open || m.screen != screenOrchDetail {
		t.Fatalf("enter@Discard: open=%v screen=%v, want the orchestrator detail", m.unsaved.open, m.screen)
	}

	m, _ = editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	m, _ = update(m, press("d"))
	if m.unsaved.open || m.screen != screenOrchDetail {
		t.Fatalf("d: open=%v screen=%v, want the orchestrator detail", m.unsaved.open, m.screen)
	}
}

func TestUnsavedDialogCancelKeysAndIgnoredKeys(t *testing.T) {
	noKeyGrace(t)
	for _, k := range []tea.KeyMsg{press("esc"), ctrlC, press("c")} {
		m, _ := editedReasoningSetup(t)
		m = openDialog(t, m, gReasoning)
		m, _ = update(m, k)
		if m.unsaved.open || m.screen != screenLaunch {
			t.Errorf("%s should cancel back to the setup: open=%v screen=%v", k, m.unsaved.open, m.screen)
		}
	}
	m, _ := editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	for _, k := range []string{"y", "n", "q", "s", "x"} {
		m, _ = update(m, press(k))
		if !m.unsaved.open || m.screen != screenLaunch {
			t.Fatalf("%q must be ignored by the dialog (s is not Save on the reasoning setup)", k)
		}
	}
}

func TestUnsavedSaveRunsTheSurfaceSave(t *testing.T) {
	noKeyGrace(t)
	for _, how := range []string{"s", "enter@Save"} {
		m, _ := editedReasoningSetup(t)
		// The same browser as a plain launch: gLaunch's Save is startLaunch.
		m.launch.reasoning = nil
		m.launch.baseline = launchSnapshot{}
		m = openDialog(t, m, gLaunch)
		if how == "s" {
			m, _ = update(m, press("s"))
		} else {
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
		}
		if m.unsaved.open || m.screen != screenList || len(m.launching) != 1 {
			t.Fatalf("%s: open=%v screen=%v launching=%d, want the launch started from the list", how, m.unsaved.open, m.screen, len(m.launching))
		}
	}
}

func TestUnsavedRebuildOpensAConfirmOfTheEditedSetup(t *testing.T) {
	noKeyGrace(t)
	m, proj := editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	m, _ = update(m, press("r"))
	if m.unsaved.open || m.screen != screenConfirm || !m.confirm.yOnly || m.confirm.returnTo != screenLaunch {
		t.Fatalf("r: screen=%v confirm=%+v, want a y-only confirm returning to the setup", m.screen, m.confirm)
	}
	body := strings.Join(m.confirm.body, "\n")
	for _, want := range []string{"edited setup", "folder, proj (clone)", "conversation and workspace are lost"} {
		if !strings.Contains(body, want) {
			t.Errorf("confirm body missing %q:\n%s", want, body)
		}
	}
	view := m.View()
	if !strings.Contains(view, "Refused while a run is on it.") || !strings.Contains(view, "Reasoning sandbox setup") {
		t.Error("the confirm keeps the warning and floats over the setup")
	}
	// Enter never accepts a y-only confirm; n returns to the setup intact.
	for range 3 {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if m.screen != screenConfirm {
		t.Fatal("enter accepted a y-only confirm")
	}
	m, _ = update(m, press("n"))
	if m.screen != screenLaunch || !m.launch.selected[proj] || !m.launch.cloneMode {
		t.Fatalf("n: screen=%v, want the edited setup back", m.screen)
	}
	// y rebuilds with the edited setup and lands on the detail.
	m = openDialog(t, m, gReasoning)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // enter@Rebuild = r
	if m.screen != screenConfirm {
		t.Fatalf("enter@Rebuild: screen=%v, want the confirm", m.screen)
	}
	m, cmd := update(m, press("y"))
	if m.screen != screenOrchDetail || cmd == nil {
		t.Fatalf("y: screen=%v, want the orchestrator detail", m.screen)
	}
	req, ok := runCmd(cmd).(reasoningRebuildMsg)
	if !ok || req.setup == nil || len(req.setup.GetSources()) != 2 || req.setup.GetSources()[1].GetPath() != proj ||
		req.setup.GetConfig().GetSeedingMode() != pb.SeedingMode_SEEDING_MODE_CLONE {
		t.Fatalf("rebuild request = %+v, want the edited setup", req)
	}
}

func TestUnsavedDialogTypingGrace(t *testing.T) {
	now := time.Unix(1000, 0)
	old := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = old })

	m, _ := editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	now = now.Add(100 * time.Millisecond)
	// "→, enter" typed straight after a stray esc must not reach Discard.
	for _, k := range []tea.KeyMsg{{Type: tea.KeyRight}, {Type: tea.KeyEnter}, press("d"), press("r"), press("l"), {Type: tea.KeyTab}} {
		m, _ = update(m, k)
		if !m.unsaved.open || m.unsaved.highlight != unsavedCancel || m.screen != screenLaunch {
			t.Fatalf("%s acted within the grace window", k)
		}
	}
	for _, k := range []tea.KeyMsg{press("esc"), ctrlC} {
		mm := openDialog(t, m, gReasoning)
		mm, _ = update(mm, k)
		if mm.unsaved.open || mm.screen != screenLaunch {
			t.Errorf("%s must cancel at once", k)
		}
	}
	now = now.Add(200 * time.Millisecond)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.unsaved.open || m.screen != screenOrchDetail {
		t.Fatalf("after the grace, →, enter should discard: open=%v screen=%v", m.unsaved.open, m.screen)
	}
}

func TestConfirmOnAcceptRunsOnAcceptOnly(t *testing.T) {
	for _, k := range []string{"y", "enter", "n", "esc"} {
		m := sized(New(&fakeDaemon{}, t.TempDir()))
		m.confirm = confirmState{
			title:     "Go?",
			returnTo:  screenJobs,
			onConfirm: func() tea.Msg { return nil },
			onAccept:  func(m *Model) { m.status = "accepted on " + screenName(m.screen) },
		}
		m.screen = screenConfirm
		msg := press(k)
		if k == "enter" {
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		}
		m, _ = update(m, msg)
		accepted := m.status == "accepted on jobs"
		if want := k == "y" || k == "enter"; accepted != want || m.screen != screenJobs {
			t.Errorf("%s: status=%q screen=%v", k, m.status, m.screen)
		}
	}
	// No hook: still lands on returnTo.
	m := sized(New(&fakeDaemon{}, t.TempDir()))
	m.confirm = confirmState{title: "Go?", returnTo: screenSources, onConfirm: func() tea.Msg { return nil }}
	m.screen = screenConfirm
	m, _ = update(m, press("y"))
	if m.screen != screenSources {
		t.Errorf("nil hook: screen = %v", m.screen)
	}
}

// screenName labels the screens the onAccept test lands on.
func screenName(s screen) string {
	if s == screenJobs {
		return "jobs"
	}
	return "other"
}

func TestConfirmYOnly(t *testing.T) {
	open := func(yOnly bool) Model {
		m := sized(New(&fakeDaemon{}, t.TempDir()))
		m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
		m.confirm = confirmState{title: "Go?", returnTo: screenJobs, onConfirm: func() tea.Msg { return "go" }, yOnly: yOnly}
		m.screen = screenConfirm
		return m
	}
	m := open(true)
	for range 5 {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if m.screen != screenConfirm {
		t.Fatal("enter accepted a y-only confirm")
	}
	view := m.View()
	if !strings.Contains(m.confirmModal(), "y confirm · n/esc cancel") || !strings.Contains(m.renderHelp(m.confirmHelp()), "y confirm") ||
		strings.Contains(view, "y/enter") {
		t.Errorf("y-only hints wrong:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line %d cells wide at 80 columns: %q", w, line)
		}
	}
	m, cmd := update(m, press("y"))
	if m.screen != screenJobs || cmd == nil {
		t.Fatal("y should accept")
	}
	for _, k := range []string{"n", "esc"} {
		m = open(true)
		m, cmd = update(m, press(k))
		if m.screen != screenJobs || cmd != nil {
			t.Errorf("%s should decline", k)
		}
	}
	m = open(false)
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenJobs || cmd == nil {
		t.Fatal("enter should still accept an ordinary confirm")
	}
	if !strings.Contains(open(false).confirmModal(), "y/enter confirm") {
		t.Error("ordinary confirm hint changed")
	}
}

func TestUnsavedModalFits80x24(t *testing.T) {
	noKeyGrace(t)
	for _, g := range []guardSurface{gReasoning, gLaunch} {
		m, _ := editedReasoningSetup(t)
		m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
		if g == gLaunch {
			m.launch.reasoning = nil
		}
		m = openDialog(t, m, g)
		modal := m.unsavedModal()
		if rows := strings.Count(modal, "\n") + 1; rows > 11 {
			t.Errorf("%v: modal is %d rows, want ≤ 11:\n%s", g, rows, modal)
		}
		for _, line := range strings.Split(m.View(), "\n") {
			if w := lipgloss.Width(line); w > 80 {
				t.Errorf("%v: line %d cells wide: %q", g, w, line)
			}
		}
	}
	for g := gNone; g <= gGroupAdd; g++ {
		if w := lipgloss.Width(unsavedBody(g)); w > 49 {
			t.Errorf("body of %v is %d cells, wraps in the 49-cell modal", g, w)
		}
	}
}

func TestPasswordPromptDrawsOverEveryLayer(t *testing.T) {
	noKeyGrace(t)
	m, _ := editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	prompt := func(m Model) Model {
		m.hosts.connecting = true
		m.hosts.pwMode = pwSignIn
		m.hosts.pwTarget = "far-host"
		return m
	}
	if v := prompt(m).View(); !strings.Contains(v, "Sign in to far-host") {
		t.Errorf("password prompt hidden under the dialog:\n%s", v)
	}
	m.unsaved = unsavedState{}
	if v := prompt(m).View(); !strings.Contains(v, "Sign in to far-host") {
		t.Errorf("password prompt hidden under the launch screen:\n%s", v)
	}
	m.screen = screenSources
	m.sourcesView = sourcesState{sandbox: "alpha"}
	if v := prompt(m).View(); !strings.Contains(v, "Sign in to far-host") {
		t.Errorf("password prompt hidden under the sources overlay:\n%s", v)
	}
}

func TestRequestExitOnACleanSurfaceLeaves(t *testing.T) {
	m, _ := editedReasoningSetup(t)
	m.launch.order = m.launch.order[:1]
	m.launch.cloneMode = false
	mm, _ := m.requestExit(gReasoning)
	m = mm.(Model)
	if m.unsaved.open || m.screen != screenOrchDetail {
		t.Fatalf("clean exit: open=%v screen=%v, want the detail with no dialog", m.unsaved.open, m.screen)
	}
}

func TestReconcileClosesADialogWhoseSurfaceIsGone(t *testing.T) {
	m, _ := editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	m.screen = screenOrchDetail // e.g. a background update closed the setup
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.unsaved.open {
		t.Fatal("dialog outlived its surface")
	}
}

func TestLaunchSnapshot(t *testing.T) {
	l := launchState{name: newNameInput(), order: []string{"/a", "/b"}, kitOrder: []string{"k"}}
	l.name.SetValue("  box  ")
	base := snapshotLaunch(l)
	if base.name != "box" {
		t.Errorf("name = %q, want trimmed", base.name)
	}
	l.order[0], l.order[1] = "/b", "/a"
	if snapshotLaunch(l).equal(base) {
		t.Error("re-ordered sources must change the snapshot")
	}
	if base.sources[0] != "/a" {
		t.Error("the snapshot aliases the live order slice")
	}
	l.order[0], l.order[1] = "/a", "/b"
	l.name.SetValue("box")
	if !snapshotLaunch(l).equal(base) {
		t.Error("same choices, trimmed name: want equal")
	}
}

func TestKitFingerprint(t *testing.T) {
	a := store.Kit{Name: "k", Permissions: &store.KitPermissions{Network: &store.KitNetworkPerms{}}}
	b := store.Kit{Name: "k", Permissions: &store.KitPermissions{Network: &store.KitNetworkPerms{Allow: []string{}}}}
	if kitFingerprint(a) != kitFingerprint(b) {
		t.Error("nil vs empty slice changed the fingerprint")
	}
	if a.Permissions.Network == nil {
		t.Error("fingerprinting normalised the live kit")
	}
	unnamed := store.Kit{}
	before := kitFingerprint(unnamed)
	unnamed.Services = []store.KitService{{Name: "web"}}
	if kitFingerprint(unnamed) == before {
		t.Error("an unnamed kit with an added item must fingerprint differently")
	}
}
