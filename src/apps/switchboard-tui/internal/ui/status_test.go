package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/editor"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// TestStatusLines (T013): wrapping, breaking a long token, the cut marker,
// sanitising before the cut.
func TestStatusLines(t *testing.T) {
	msg := "opened abc in VS Code (unknown editor 'foo' in settings) and some more words to wrap past eighty columns here"
	lines := statusLines(msg, 80, 6)
	if len(lines) != 2 || lipgloss.Width(lines[0]) > 80 || strings.HasSuffix(lines[0], " ") || strings.Join(lines, " ") != msg {
		t.Errorf("wrap at spaces: %q", lines)
	}
	// A 120-column token with no spaces is broken so no line exceeds the width.
	token := strings.Repeat("x", 120)
	lines = statusLines("error: "+token, 80, 6)
	if len(lines) < 2 || !strings.Contains(strings.Join(lines, ""), token) {
		t.Errorf("a long token is broken: %q", lines)
	}
	for _, l := range lines {
		if lipgloss.Width(l) > 80 {
			t.Errorf("line too wide: %q", l)
		}
	}
	// The cut marker lands on a kept line shorter than the width.
	lines = statusLines(strings.Repeat("word ", 60), 80, 2)
	if len(lines) != 2 || !strings.HasSuffix(lines[1], "…") || lipgloss.Width(lines[1]) > 80 {
		t.Errorf("cut: %q", lines)
	}
	// Tabs are expanded before the cut counts columns.
	tabs := strings.Repeat("ab\t", 10)
	if w := ansi.StringWidth(cutEnd(tabs, 120)); w > 120 {
		t.Errorf("cutEnd after sanitising = %d columns", w)
	}
	if w := ansi.StringWidth(strings.Join(statusLines(tabs+strings.Repeat("z", 200), 80, 6), "")); w > 6*80 {
		t.Errorf("sanitised then wrapped = %d columns", w)
	}
	if statusLines("", 80, 6) != nil || len(statusLines("x", 80, 0)) != 1 {
		t.Error("an empty status gives no lines; the limit floors at 1")
	}
	if got := statusLines("a\nb", 80, 6); len(got) != 1 || got[0] != "a b" {
		t.Errorf("newlines become spaces: %q", got)
	}
}

func TestCutMiddle(t *testing.T) {
	exe := "/Users/developer/Library/Application Support/JetBrains/Toolbox/scripts/webstorm"
	if got := cutMiddle(exe, 60); got != "/Users/developer/Lib…port/JetBrains/Toolbox/scripts/webstorm" || ansi.StringWidth(got) != 60 {
		t.Errorf("cutMiddle(60) = %q", got)
	}
	long := strings.Repeat("a", 50) + strings.Repeat("b", 50)
	if got := cutMiddle(long, 78); got != strings.Repeat("a", 38)+"…"+strings.Repeat("b", 39) {
		t.Errorf("cutMiddle(78) = %q", got)
	}
	for _, s := range []string{strings.Repeat("a", 60), strings.Repeat("a", 59), "x", ""} {
		if got := cutMiddle(s, 60); got != s {
			t.Errorf("text that fits changed: %q", got)
		}
	}
	// A wide rune on the cut: never wider than cols, at 60 and 78.
	wide := strings.Repeat("日", 70)
	for _, cols := range []int{60, 78} {
		if got := cutMiddle(wide, cols); ansi.StringWidth(got) > cols || !strings.Contains(got, "…") {
			t.Errorf("cutMiddle(wide, %d) = %q (%d columns)", cols, got, ansi.StringWidth(got))
		}
		mixed := strings.Repeat("a", 19) + "日" + strings.Repeat("b", 100)
		if got := cutMiddle(mixed, cols); ansi.StringWidth(got) > cols {
			t.Errorf("cutMiddle(mixed, %d) = %d columns", cols, ansi.StringWidth(got))
		}
	}
	// Control characters are removed first.
	if got := cutMiddle("a\x1b[2Jb", 60); got != "ab" {
		t.Errorf("sanitised = %q", got)
	}
}

// listAt80x24 is the sandbox list at 80×24 with the update banner showing.
func listAt80x24(t *testing.T, d *fakeDaemon) Model {
	t.Helper()
	m := withSandboxes(New(d, "/work").WithVersion("v0.1.0"), seeded(d, 3))
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, updateAvailableMsg{latest: "v9.9.9"})
	if m.updateBanner == "" {
		t.Fatal("the banner should show")
	}
	return m
}

// setStatus sets a status through a message, so the wrapper's size check runs.
func setStatus(m Model, s string) Model {
	m, _ = update(m, editorOutcomeMsg{sandboxID: "none", text: s, failed: true})
	return m
}

// TestStatusLayoutList (T014): a 4-line status at 80×24 keeps the header on
// screen; a one-line status gives today's bodyHeight; the newline case.
func TestStatusLayoutList(t *testing.T) {
	m := listAt80x24(t, &fakeDaemon{})
	if m.bodyHeight() != 18 || m.statusLineCount() != 1 {
		t.Fatalf("one-line bodyHeight = %d (lines %d)", m.bodyHeight(), m.statusLineCount())
	}
	m = setStatus(m, "short")
	if m.bodyHeight() != 18 {
		t.Errorf("a one-line status keeps bodyHeight 18, got %d", m.bodyHeight())
	}
	four := strings.Repeat("four lines of status text ", 11) // ≈ 290 columns
	m = setStatus(m, four)
	if n := m.statusLineCount(); n != 4 {
		t.Fatalf("statusLineCount = %d, want 4", n)
	}
	if m.bodyHeight() != 15 || m.list.Height() != m.mainListHeight() {
		t.Errorf("bodyHeight %d list %d target %d", m.bodyHeight(), m.list.Height(), m.mainListHeight())
	}
	lines := assertFits(t, m, 80, 24)
	v := strings.Join(lines, "\n")
	for _, sl := range statusLines(four, 80, 6) {
		if !strings.Contains(v, sl) {
			t.Errorf("status line missing: %q\n%s", sl, v)
		}
	}
	// 40 a's, a newline, 39 b's, a newline and c: 80 columns as the terminal
	// text leaves it, 82 once the newlines are spaces, so it takes 2 lines
	// and shows whole.
	nl := strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 39) + "\nc"
	m = setStatus(m, nl)
	if m.statusLineCount() != 2 || m.statusFitsOneLine() {
		t.Errorf("newline status lines = %d", m.statusLineCount())
	}
	v = ansi.Strip(m.View())
	for _, sl := range statusLines(nl, 80, 6) {
		if !strings.Contains(v, sl) {
			t.Errorf("the newline status shows whole; missing %q:\n%s", sl, v)
		}
	}
}

// TestStatusDaemonsCut: Daemons with 8 rows can't shrink, so the message is cut
// with … and the header stays; back on the list the whole message shows.
func TestStatusDaemonsCut(t *testing.T) {
	d := &fakeDaemon{}
	m := listAt80x24(t, d)
	mgr := client.NewManager()
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		mgr.Upsert(client.HostEntry{ID: id, DisplayName: "host-" + id, Kind: "ssh", SSHTarget: "me@" + id})
	}
	m = m.WithHosts(mgr, nil, "a")
	long := strings.Repeat("a message about the editor that goes on ", 8)
	m = setStatus(m, long)
	m, _ = update(m, shiftRight())
	if m.screen != screenDaemons {
		t.Fatal("not on Daemons")
	}
	lines := assertFits(t, m, 80, 24)
	v := strings.Join(lines, "\n")
	if !strings.Contains(v, "…") || !strings.Contains(v, "host-h") {
		t.Errorf("Daemons cuts the message and keeps its rows:\n%s", v)
	}
	if m.status != long {
		t.Error("the status keeps the whole message")
	}
	m, _ = update(m, shiftLeft())
	v = strings.Join(assertFits(t, m, 80, 24), "\n")
	for _, sl := range statusLines(long, 80, 6) {
		if !strings.Contains(v, sl) {
			t.Errorf("back on the list the whole message shows; missing %q\n%s", sl, v)
		}
	}
}

// TestStatusJobsLimit: Jobs at 80×14 with more than 3 rows: the limit leaves
// the window its minimum 3 rows; could-not-read and connecting lines take
// from it. Asserted on statusLimit() directly, because chrome's min(limit,
// room) can hide a wrong limit.
func TestStatusJobsLimit(t *testing.T) {
	m, _ := jobsWith(t, jobsOverview, nil, &pb.ListRunsResponse{
		Queued:   []*pb.Run{{Id: "q1", Action: "a"}, {Id: "q2", Action: "a"}, {Id: "q3", Action: "a"}},
		InFlight: []*pb.Run{{Id: "i1", Action: "a"}, {Id: "i2", Action: "a"}},
	})
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 14})
	if m.screen != screenJobs {
		t.Fatal("not on Jobs")
	}
	help := m.helpLineCount(m.jobsHelp())
	top, bottom := m.jobsFixedLines()
	want := 14 - 2 - 1 - help - (len(top) + len(bottom) + 3)
	if want > statusMaxLines {
		want = statusMaxLines
	}
	if want < 1 {
		want = 1
	}
	if got := m.statusLimit(); got != want {
		t.Errorf("statusLimit = %d, want %d (help %d, fixed %d+%d)", got, want, help, len(top), len(bottom))
	}
	m = setStatus(m, strings.Repeat("status words that wrap ", 20))
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) > 14 || !strings.Contains(lines[0], "Switchboard") {
		t.Errorf("Jobs with a long status: %d lines\n%s", len(lines), strings.Join(lines, "\n"))
	}
	// Extra fixed lines reduce the limit by the same amount.
	m.jobs.status = "refused"
	top2, bottom2 := m.jobsFixedLines()
	if got := m.statusLimit(); got != max(1, min(statusMaxLines, want-(len(top2)+len(bottom2)-len(top)-len(bottom)))) {
		t.Errorf("statusLimit with a Jobs status = %d", got)
	}
}

// TestStatusTerminalLimit: the terminal screen cuts at 2 lines and termRows is
// the value with no status.
func TestStatusTerminalLimit(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	rows := m.termRows()
	m = setStatus(m, strings.Repeat("a long status while the terminal is open ", 8))
	if m.termRows() != rows || rows != 16 {
		t.Errorf("termRows = %d, want %d regardless of the status", m.termRows(), rows)
	}
	m, cmd := update(m, press("t"))
	m, _ = update(m, runTermAttach(cmd))
	m = setStatus(m, strings.Repeat("a long status while the terminal is open ", 8))
	if m.screen != screenTerminal || m.statusLimit() != terminalStatusLines {
		t.Fatalf("screen %v limit %d", m.screen, m.statusLimit())
	}
	lines := assertFits(t, m, 80, 24)
	if v := strings.Join(lines, "\n"); !strings.Contains(v, "…") || !strings.Contains(v, "Terminal") {
		t.Errorf("terminal cut:\n%s", v)
	}
}

// TestStatusDetachedPickOverInbox: the start-up notice's picker floats over the
// inbox (SWI-25 A2), so a long status takes its lines from the inbox list and
// the frame stays on screen.
func TestStatusDetachedPickOverInbox(t *testing.T) {
	m := listAt80x24(t, &fakeDaemon{})
	out, _ := m.enterNotifications()
	m = out.(Model)
	m.detachedPick = detachedPickState{n: &pb.NotificationEvent{}, choices: []detachedOrch{{id: "o", name: "a", count: 1}}}
	m.screen = screenDetachedPick
	if got := m.activeSurface(); got != (activeSurface{kind: surfaceItemList, which: ilNotifications}) {
		t.Errorf("surface = %+v, want the inbox list", got)
	}
	m = setStatus(m, strings.Repeat("a message about the editor that goes on ", 8))
	if m.notifyList.Height() != m.bodyHeight() {
		t.Errorf("inbox list height = %d, want the body's %d", m.notifyList.Height(), m.bodyHeight())
	}
	assertFits(t, m, 80, 24)
}

// TestActiveHelpMatchesRender: every screen's activeHelp() is what its render
// passes to chrome.
func TestActiveHelpMatchesRender(t *testing.T) {
	var seen helpBindings
	chromeHook = func(hb helpBindings) { seen = hb }
	t.Cleanup(func() { chromeHook = nil })
	same := func(a, b helpBindings) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i].Help() != b[i].Help() {
				return false
			}
		}
		return true
	}
	d := &fakeDaemon{}
	st, _ := store.New(t.TempDir())
	m := twoHostModel(t, d).WithSettings(st.Settings()).WithKits(nil)
	check := func(name string, mm Model) {
		t.Helper()
		seen = nil
		_ = mm.View()
		if !same(seen, mm.activeHelp()) {
			t.Errorf("%s: activeHelp %v, rendered %v", name, helpLine(mm.activeHelp()), helpLine(seen))
		}
	}
	check("list", m)
	s, _ := update(m, press(","))
	check("settings", s)
	dm, _ := update(m, shiftRight())
	check("daemons", dm)
	jm, _ := update(dm, shiftRight())
	check("jobs", jm)
	c, _ := m.enterConfirm(confirmState{title: "x", onConfirm: func() tea.Msg { return nil }})
	check("confirm", c.(Model))
	pick := m
	pick.detachedPick = detachedPickState{n: &pb.NotificationEvent{}, choices: []detachedOrch{{id: "o", name: "a", count: 1}}}
	pick.screen = screenDetachedPick
	check("detached pick", pick)
	tm, cmd := update(runningSbx(d), press("t"))
	tm, _ = update(tm, runCmd(cmd))
	check("terminal", tm)
	r, _ := m.enterRename(d.sandboxes[0], "")
	check("rename", r.(Model))
	g, cmd := update(m, press("g"))
	g, _ = update(g, runCmd(cmd))
	check("groups", g)
	// The enlarge hint is part of the map: a form with a focused Text.
	e, cmd := update(sized(New(&fakeDaemon{manifest: testManifest()}, "/work").WithConfigs(newConfigStore(t))), press("c"))
	e, _ = update(e, runCmd(cmd))
	check("config editor", e)
}

// TestStatusCost: with an empty or one-line status bodyHeight renders the help
// zero times and a View renders chrome once.
func TestStatusCost(t *testing.T) {
	m := listAt80x24(t, &fakeDaemon{})
	for _, s := range []string{"", "one line"} {
		m.status = s
		before := helpRenders
		_ = m.bodyHeight()
		if helpRenders != before {
			t.Errorf("bodyHeight rendered the help %d times with status %q", helpRenders-before, s)
		}
		cb := chromeRenders
		_ = m.View()
		if chromeRenders != cb+1 {
			t.Errorf("View rendered chrome %d times", chromeRenders-cb)
		}
	}
}

func BenchmarkViewList(b *testing.B) {
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work").WithVersion("v0.1.0"), seeded(d, 8))
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, s := range []string{"", "one line"} {
		m.status = s
		b.Run("status="+s, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
	}
}

// TestSizeCheck (T015): each key field changed in turn resizes the active
// surface; an unchanged key computes no target; a form shrunk then restored
// is resized back with a no-op Update.
func TestSizeCheck(t *testing.T) {
	noKeyGrace(t)
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work").WithVersion("v0.1.0"), seeded(d, 3))
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	target := func(mm Model) int { return mm.mainListHeight() }
	if m.list.Height() != target(m) {
		t.Fatalf("start: list %d target %d", m.list.Height(), target(m))
	}
	// Status grows.
	long := strings.Repeat("a long status that wraps to several lines ", 6)
	m = setStatus(m, long)
	if m.list.Height() != target(m) || m.list.Height() >= 18-2 {
		t.Errorf("status: list %d target %d", m.list.Height(), target(m))
	}
	// An unchanged key computes nothing.
	before := sizeChecks
	m, _ = update(m, spinnerTick())
	if sizeChecks != before {
		t.Error("an unchanged key must not recompute the target")
	}
	// The banner arrives.
	h := m.list.Height()
	m, _ = update(m, updateAvailableMsg{latest: "v9.9.9"})
	if m.list.Height() != h-1 || m.list.Height() != target(m) {
		t.Errorf("banner: list %d target %d", m.list.Height(), target(m))
	}
	// The help footer changes height via MarkFree: a worker awaiting review.
	d.sandboxes[0].Controller = &pb.SandboxController{OrchestratorId: "o", Name: "bot", Run: &pb.WorkerRun{Id: "r", NeedsReview: true}}
	m, _ = update(m, sandboxesMsg(d.sandboxes))
	if m.list.Height() != target(m) {
		t.Errorf("markFree: list %d target %d", m.list.Height(), target(m))
	}
	// Tabs change (a group tab adds ←/→ group to the help).
	gs := newGroupStore(t)
	if _, err := gs.Save(store.Group{Name: "team"}); err != nil {
		t.Fatal(err)
	}
	m = m.WithGroups(gs)
	m, _ = update(m, listDataMsg{groups: mustGroups(t, gs)})
	if len(m.tabs) < 2 || m.list.Height() != target(m) {
		t.Errorf("tabs: %d list %d target %d", len(m.tabs), m.list.Height(), target(m))
	}
	// Returning to the list from Settings during a long message.
	m, _ = update(m, press(","))
	m = setStatus(m, strings.Repeat("another long message set on the settings screen ", 6))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList || m.list.Height() != target(m) {
		t.Errorf("back to the list: list %d target %d", m.list.Height(), target(m))
	}
	// An item list: groups.
	m, cmd := update(m, press("g"))
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenGroups || m.groups.list.Height() != m.bodyHeight() {
		t.Errorf("groups list %d body %d", m.groups.list.Height(), m.bodyHeight())
	}
	m = setStatus(m, "")
	if m.groups.list.Height() != m.bodyHeight() {
		t.Errorf("groups list after the status cleared: %d body %d", m.groups.list.Height(), m.bodyHeight())
	}
	// A huh form shrunk then restored.
	e, cmd := update(sized(New(&fakeDaemon{manifest: testManifest()}, "/work").WithConfigs(newConfigStore(t))), press("c"))
	e, _ = update(e, runCmd(cmd))
	e, _ = update(e, tea.WindowSizeMsg{Width: 80, Height: 24})
	if e.editor.form == nil || e.formHeights[e.editor.form] != e.bodyHeight() {
		t.Fatalf("form height recorded %d, body %d", e.formHeights[e.editor.form], e.bodyHeight())
	}
	e = typeIn(e, "zz")
	e = setStatus(e, strings.Repeat("a long status over the form ", 8))
	if e.formHeights[e.editor.form] != e.bodyHeight() || e.bodyHeight() >= 18 {
		t.Errorf("form shrunk to %d, body %d", e.formHeights[e.editor.form], e.bodyHeight())
	}
	e = setStatus(e, "")
	if e.formHeights[e.editor.form] != 18 || !strings.Contains(ansi.Strip(e.View()), "zz") {
		t.Errorf("form restored to %d; typed text drawn: %v", e.formHeights[e.editor.form], strings.Contains(ansi.Strip(e.View()), "zz"))
	}
	e, _ = update(e, tea.KeyMsg{Type: tea.KeyEsc})
	e, _ = update(e, press("d"))
	if e.formHeights != nil {
		t.Error("a closed form's height is forgotten")
	}
}

// TestSizeCheckPauseKey: moving between a reasoning row and a worker row adds or
// removes `P pause/resume worker`, which can wrap the footer; the list follows.
func TestSizeCheckPauseKey(t *testing.T) {
	noKeyGrace(t)
	d := &fakeDaemon{}
	sbs := seeded(d, 2)
	sbs[0].Controller = &pb.SandboxController{OrchestratorId: "o", Name: "bot", Reasoning: true}
	sbs[1].Controller = &pb.SandboxController{OrchestratorId: "o", Name: "bot"}
	m := withSandboxes(New(d, "/work").WithVersion("v0.1.0"), sbs)
	m, _ = update(m, press("O")) // workers are listed on the Orchestrated tab
	wrapped := 0
	for w := 60; w <= 200; w++ {
		m, _ = update(m, tea.WindowSizeMsg{Width: w, Height: 24})
		before := m.helpLineCount(m.activeHelp())
		beforeRow := m.listHelpEntries()
		key := "j"
		if m.current().GetId() == sbs[1].GetId() {
			key = "k"
		}
		m, _ = update(m, press(key))
		if m.listHelpEntries() == beforeRow {
			t.Fatalf("width %d: %s didn't change the row's help entries", w, key)
		}
		if m.helpLineCount(m.activeHelp()) != before {
			wrapped++
		}
		if m.list.Height() != m.mainListHeight() {
			t.Errorf("width %d: list %d target %d", w, m.list.Height(), m.mainListHeight())
		}
	}
	if wrapped == 0 {
		t.Fatal("no width changed the footer's height, so this tested nothing")
	}
}

func spinnerTick() tea.Msg { return termUpdateMsg{} }

func mustGroups(t *testing.T, gs *store.GroupStore) []store.Group {
	t.Helper()
	groups, err := gs.List()
	if err != nil {
		t.Fatal(err)
	}
	return groups
}

// --- SC-004 (T040) ---

// TestSC004MessagesFitAt80x24: every `v` message with outside text at its
// limits fits in full on the sandbox list at 80×24 with the update banner.
func TestSC004MessagesFitAt80x24(t *testing.T) {
	d := &fakeDaemon{}
	m := listAt80x24(t, d)
	unknown := []string{"unknown editor '" + strings.Repeat("u", 60) + "' in settings"}
	unreadable := []string{"settings couldn't be read at startup, changes aren't saved"}
	ci, _ := editor.Lookup("code-insiders")
	custom, _ := editor.Lookup("custom")
	exe60 := strings.Repeat("e", 60)
	daemonErr := errors.New(strings.Repeat("d", 120))
	cases := map[string]string{
		"daemon error + unknown":     withNotices("can't open sb1aaaaa: "+cutEnd(daemonErr.Error(), 120), unknown),
		"daemon error with tabs":     withNotices("can't open sb1aaaaa: "+cutEnd(strings.Repeat("abc\t", 30), 120), unknown),
		"not-found longest":          withNotices(startFailedText(ci, exe60, errors.New(strings.Repeat("r", 40))), unknown),
		"host-lookup no spaces":      withNotices(hostLookupText("sb1aaaaa", strings.Repeat("h", 120)), unknown),
		"host-lookup space after 40": withNotices(hostLookupText("sb1aaaaa", strings.Repeat("h", 41)+" "+strings.Repeat("i", 36)), unknown),
		"host-lookup + unreadable":   withNotices(hostLookupText("sb1aaaaa", strings.Repeat("h", 120)), unreadable),
		"exit report":                exitReportText("sb1aaaaa", exe60, "SWITCHBOARD_CODE_INSIDERS_BIN", editor.ExitReport{Status: 127}, unknown),
		"path-limited built-in":      withNotices(refusalText("sb1aaaaa", ci, &editor.Refusal{Rule: editor.RulePathLimited, Detail: "U+10FFFF", Where: "SWITCHBOARD_CODE_INSIDERS_BIN"}), unknown),
		"path-limited custom":        withNotices(refusalText("sb1aaaaa", custom, &editor.Refusal{Rule: editor.RulePathLimited, Detail: "U+10FFFF"}), unreadable),
		"shell-syntax":               withNotices(refusalText("sb1aaaaa", custom, &editor.Refusal{Rule: editor.RuleShellSyntax}), unreadable),
		"no-remote":                  withNotices(refusalText("sb1aaaaa", ci, &editor.Refusal{Rule: editor.RuleNoRemote}), unknown),
		"three notices":              withNotices("opened sb1aaaaa in Custom", append(append([]string{}, unknown...), unreadable[0], "the path is on the remote host")),
	}
	for name, text := range cases {
		mm := setStatus(m, text)
		if n := len(statusLines(text, 80, 6)); n > 4 {
			t.Errorf("%s: %d lines at 80 columns:\n%s", name, n, strings.Join(statusLines(text, 80, 6), "\n"))
		}
		lines := assertFits(t, mm, 80, 24)
		v := strings.Join(lines, "\n")
		for _, sl := range statusLines(text, 80, 6) {
			if !strings.Contains(v, sl) {
				t.Errorf("%s: status line missing: %q\n%s", name, sl, v)
			}
		}
		if mm.list.Height() != mm.mainListHeight() {
			t.Errorf("%s: list %d target %d", name, mm.list.Height(), mm.mainListHeight())
		}
	}
	// The list opened after a 3-line status was set on the settings screen.
	three := strings.Repeat("a message set while settings was open ", 5)
	s, _ := update(m, press(","))
	s = setStatus(s, three)
	s, _ = update(s, tea.KeyMsg{Type: tea.KeyEsc})
	v := strings.Join(assertFits(t, s, 80, 24), "\n")
	for _, sl := range statusLines(three, 80, 6) {
		if !strings.Contains(v, sl) {
			t.Errorf("after settings: missing %q\n%s", sl, v)
		}
	}
	// A banner arriving during a 4-line message.
	nb := withSandboxes(New(d, "/work").WithVersion("v0.1.0"), d.sandboxes)
	nb, _ = update(nb, tea.WindowSizeMsg{Width: 80, Height: 24})
	four := cases["not-found longest"]
	nb = setStatus(nb, four)
	nb, _ = update(nb, updateAvailableMsg{latest: "v9.9.9"})
	v = strings.Join(assertFits(t, nb, 80, 24), "\n")
	for _, sl := range statusLines(four, 80, 6) {
		if !strings.Contains(v, sl) {
			t.Errorf("banner during a message: missing %q\n%s", sl, v)
		}
	}
	// The joined case (SC-008): cut with …, the header and the whole error on
	// screen, the status keeps the full text.
	termErr := "terminal: " + strings.Repeat("t", 110)
	joined := termErr + "; " + four
	jm := setStatus(m, joined)
	lines := assertFits(t, jm, 80, 24)
	v = strings.Join(lines, "\n")
	whole := true
	for _, sl := range statusLines(joined, 80, 6) {
		whole = whole && strings.Contains(v, sl)
	}
	if whole || !strings.Contains(v, strings.Repeat("t", 30)) || jm.status != joined {
		t.Errorf("joined message is cut, the error on screen, the status whole (cut %v):\n%s", !whole, v)
	}
}
