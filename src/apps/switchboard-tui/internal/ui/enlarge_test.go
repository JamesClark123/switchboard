package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

var (
	keyCtrlE = tea.KeyMsg{Type: tea.KeyCtrlE}
	keyEsc   = tea.KeyMsg{Type: tea.KeyEsc}
)

// textSurface opens one app form that carries Text fields, through the model.
type textSurface struct {
	name string
	g    guardSurface
	open func(t *testing.T) Model
}

// kitFormOn opens one kit form on a bare kit, as enter/a on its section does.
func kitFormOn(t *testing.T, k *store.Kit, kind kitFormKind) Model {
	t.Helper()
	m := editorOn(t, k)
	mm, cmd := m.openKitForm(kind, -1)
	m = mm.(Model)
	m, _ = update(m, runCmd(cmd))
	return m
}

// textSurfaces covers all 17 Text fields: 13 in the kit forms, MCP headers,
// action body and arguments, and event fixed arguments.
func textSurfaces() []textSurface {
	var out []textSurface
	for _, kind := range []kitFormKind{formNetwork, formEnvironment, formAgentInstructions, formInstall,
		formStartup, formInitFile, formCredential, formEscapeHatch, formService} {
		out = append(out, textSurface{
			name: fmt.Sprintf("kit form %d", kind), g: gKitItem,
			open: func(t *testing.T) Model { return kitFormOn(t, &store.Kit{Name: "bare"}, kind) },
		})
	}
	return append(out,
		textSurface{name: "mcp", g: gMcp, open: func(t *testing.T) Model {
			return pressCmd(mcpModel(t, &fakeDaemon{}), "a")
		}},
		textSurface{name: "action", g: gOpJobs, open: func(t *testing.T) Model {
			m, _ := jobsWith(t, jobsActions, nil, nil)
			m, _ = update(m, press("a"))
			return m
		}},
		textSurface{name: "event", g: gOpJobs, open: func(t *testing.T) Model {
			m, _ := jobsWith(t, jobsEvents, []*pb.Action{reviewAction()}, nil)
			m, _ = update(m, press("a"))
			return m
		}},
	)
}

// textsOf is g's registered Text fields, ordered by title so runs are stable.
func textsOf(t *testing.T, m Model, g guardSurface) []*huh.Text {
	t.Helper()
	spec := m.guardSpec(g)
	if spec == nil {
		t.Fatal("surface has no Text registry")
	}
	var out []*huh.Text
	for txt := range spec.texts {
		out = append(out, txt)
	}
	slices.SortFunc(out, func(a, b *huh.Text) int { return strings.Compare(spec.texts[a].title, spec.texts[b].title) })
	return out
}

// enlargedOn opens the agent-instructions form on content and ctrl+e on it.
func enlargedOn(t *testing.T, content string) Model {
	t.Helper()
	k := &store.Kit{Name: "bare"}
	if content != "" {
		k.AgentInstructions = &store.KitAgentInstructions{Content: content}
	}
	m := kitFormOn(t, k, formAgentInstructions)
	if _, _, ok := m.focusedText(gKitItem); !ok {
		focusField(t, m.kitEditor.form, textsOf(t, m, gKitItem)[0])
	}
	m, _ = update(m, keyCtrlE)
	if !m.enlarge.open {
		t.Fatal("ctrl+e did not open the enlarged view")
	}
	return m
}

func numbered(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%04d", i)
	}
	return strings.Join(lines, "\n")
}

// cursorVisible reports whether the view shows the cursor's line.
func cursorVisible(m Model) bool {
	row := strings.Split(m.enlarge.ta.Value(), "\n")[m.enlarge.ta.Line()]
	return strings.Contains(ansi.Strip(m.enlarge.ta.View()), row)
}

func TestEnlargeOpensOnEveryTextField(t *testing.T) {
	total := 0
	for _, s := range textSurfaces() {
		m := s.open(t)
		for _, txt := range textsOf(t, m, s.g) {
			total++
			title := m.guardSpec(s.g).texts[txt].title
			focusField(t, m.guardForm(s.g), txt)
			if !strings.Contains(m.View(), "ctrl+e enlarge") {
				t.Errorf("%s/%s: no ctrl+e hint in the footer", s.name, title)
			}
			mm, _ := update(m, keyCtrlE)
			if !mm.enlarge.open || mm.enlarge.field != txt || !strings.Contains(mm.View(), title) {
				t.Errorf("%s/%s: ctrl+e did not open the view on the field", s.name, title)
			}
			if !strings.Contains(mm.View(), enlargeHint) {
				t.Errorf("%s/%s: hint line missing", s.name, title)
			}
		}
	}
	if total != 17 {
		t.Errorf("visited %d Text fields, want 17", total)
	}
}

func TestEnlargeIgnoresOtherFields(t *testing.T) {
	m := pressCmd(mcpModel(t, &fakeDaemon{}), "a")
	f := m.guardForm(gMcp)
	focusField(t, f, f.GetFocusedField()) // the Name input
	if strings.Contains(m.View(), "ctrl+e enlarge") {
		t.Error("an Input shows the ctrl+e hint")
	}
	m = typeIn(m, "abc")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyHome})
	m, _ = update(m, keyCtrlE)
	m = typeIn(m, "z")
	if m.enlarge.open || m.mcpView.vals.name != "abcz" {
		t.Errorf("ctrl+e on an Input should move to the end: open=%v name=%q", m.enlarge.open, m.mcpView.vals.name)
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyTab}) // the Kind select
	m = settle(m, cmd)
	if _, ok := m.guardForm(gMcp).GetFocusedField().(*huh.Select[string]); !ok {
		t.Fatalf("expected the Kind select, got %T", m.guardForm(gMcp).GetFocusedField())
	}
	kind := m.mcpView.vals.kind
	m, _ = update(m, keyCtrlE)
	if m.enlarge.open || m.mcpView.vals.kind != kind {
		t.Error("ctrl+e on a Select should do nothing")
	}
}

func TestEnlargeEditWritesBackOnClose(t *testing.T) {
	for _, closeKey := range []tea.KeyMsg{keyEsc, keyCtrlE} {
		m := enlargedOn(t, "hello")
		field := m.enlarge.field
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlEnd})
		m = typeIn(m, " world")
		m, _ = update(m, closeKey)
		if m.enlarge.open || m.unsaved.open {
			t.Fatalf("%s: view should close with no dialog", closeKey)
		}
		if m.kitEditor.vals.agentInstructions != "hello world" || field.GetValue() != "hello world" {
			t.Errorf("%s: write-back: ptr %q field %q", closeKey, m.kitEditor.vals.agentInstructions, field.GetValue())
		}
		if m.kitEditor.form.GetFocusedField() != field || !m.guardDirty(gKitItem) {
			t.Errorf("%s: focus should stay on the field and the form be changed", closeKey)
		}
	}
}

func TestEnlargeCtrlSSaves(t *testing.T) {
	m := enlargedOn(t, "")
	m = typeIn(m, "be terse")
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	m = settle(m, cmd)
	if m.enlarge.open || m.kitEditor.form != nil {
		t.Fatal("ctrl+s should close the view and the form")
	}
	if ai := m.kitEditor.kit.AgentInstructions; ai == nil || ai.Content != "be terse" {
		t.Errorf("kit not saved: %+v", ai)
	}

	// An op form submits to the daemon.
	m, d := jobsWith(t, jobsActions, nil, nil)
	m, _ = update(m, press("a"))
	v := m.jobs.formVals.(*actionFormVals)
	v.name = "triage"
	m.reopenForm(gOpJobs)
	var body *huh.Text
	for txt, e := range m.jobs.formSpec.texts {
		if e.title == "Body" {
			body = txt
		}
	}
	focusField(t, m.jobs.form, body)
	m, _ = update(m, keyCtrlE)
	m = typeIn(m, "Look")
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	m = settle(m, cmd)
	if m.enlarge.open || !strings.Contains(d.orchCallsJoined(), "PutAction") {
		t.Errorf("ctrl+s should submit the action: calls %q", d.orchCallsJoined())
	}
}

func TestEnlargeCtrlCAsksOverTheViewAndCancelReturns(t *testing.T) {
	noKeyGrace(t)
	m := enlargedOn(t, numbered(60))
	for range 30 {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m = typeIn(m, "X")
	row, col, view := m.enlarge.ta.Line(), m.enlarge.ta.LineInfo().ColumnOffset, m.enlarge.ta.View()

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.unsaved.open || !m.unsaved.fromEnlarge || !m.enlarge.open {
		t.Fatal("ctrl+c on a changed form should ask over the view")
	}
	if !strings.Contains(m.View(), "Unsaved changes") {
		t.Error("dialog not drawn over the view")
	}
	m, _ = update(m, press("c"))
	if m.unsaved.open || !m.enlarge.open {
		t.Fatal("Cancel should return to the view")
	}
	if m.enlarge.ta.Line() != row || m.enlarge.ta.LineInfo().ColumnOffset != col || m.enlarge.ta.View() != view {
		t.Error("Cancel lost the cursor or scroll")
	}

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlR})
	if m.enlarge.ta.Value() != numbered(60) {
		t.Error("ctrl+r should restore the text at ctrl+e")
	}

	// Discard from the dialog closes the view and leaves the form.
	m = typeIn(m, "Y")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	m, _ = update(m, press("d"))
	if m.enlarge.open || m.unsaved.open || m.kitEditor.form != nil {
		t.Error("Discard should close the view and the form")
	}
}

func TestEnlargeCtrlCOnAnUnchangedFormLeaves(t *testing.T) {
	m := enlargedOn(t, "same")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.enlarge.open || m.unsaved.open || m.kitEditor.form != nil {
		t.Error("ctrl+c on an unchanged form should close the view and the form")
	}
}

func TestEnlargeTabIsANoOp(t *testing.T) {
	m := enlargedOn(t, "a\nb")
	field, val := m.enlarge.field, m.enlarge.ta.Value()
	for _, k := range []tea.KeyType{tea.KeyTab, tea.KeyShiftTab} {
		m, _ = update(m, tea.KeyMsg{Type: k})
	}
	if !m.enlarge.open || m.enlarge.ta.Value() != val || m.kitEditor.form.GetFocusedField() != field {
		t.Error("tab/shift+tab should change nothing")
	}
}

func TestEnlargeResizeKeepsTextAndCursor(t *testing.T) {
	m := enlargedOn(t, numbered(200))
	for range 150 {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
		_ = m.View() // the runtime renders between keys; the textarea's scroll bounds come from its last render
	}
	row := m.enlarge.ta.Line()
	m, _ = update(m, tea.WindowSizeMsg{Width: 60, Height: 12})
	if m.enlarge.ta.Value() != numbered(200) || m.enlarge.ta.Line() != row {
		t.Error("resize lost text or cursor")
	}
	if !cursorVisible(m) {
		t.Error("cursor not visible after a shrinking resize")
	}
}

func TestEnlargeFitsTheTerminal(t *testing.T) {
	for _, sz := range []struct{ w, h, minLines int }{{80, 24, 16}, {200, 50, 40}, {60, 12, 1}} {
		m := enlargedOn(t, numbered(300))
		m, _ = update(m, tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		lines := strings.Split(m.View(), "\n")
		if len(lines) > sz.h {
			t.Errorf("%dx%d: %d rows", sz.w, sz.h, len(lines))
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > sz.w {
				t.Errorf("%dx%d: a %d-cell row", sz.w, sz.h, w)
			}
		}
		if got := m.enlarge.ta.Height(); got < sz.minLines {
			t.Errorf("%dx%d: %d text lines, want ≥ %d", sz.w, sz.h, got, sz.minLines)
		}
		if sz.w == 80 && !strings.Contains(m.View(), enlargeHint) {
			t.Error("80x24: hint line missing")
		}
	}
}

func TestEnlargeCursorStaysVisible(t *testing.T) {
	m := enlargedOn(t, numbered(500))
	if m.enlarge.ta.Line() != 0 || !cursorVisible(m) {
		t.Error("open: cursor should be at the top and visible")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlEnd})
	m = typeIn(m, "z")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlR})
	if !cursorVisible(m) {
		t.Error("ctrl+r at the end of a long text: cursor not visible")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlHome})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.enlarge.ta.Line() == 0 || !cursorVisible(m) {
		t.Error("pgdown should page with the cursor visible")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgUp})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.enlarge.ta.Line() != 0 || !cursorVisible(m) {
		t.Error("pgup should page back to the top")
	}
}

func TestEnlargeCRLFAndTabRoundTrip(t *testing.T) {
	m := enlargedOn(t, "a\r\nb\r\nc")
	m, _ = update(m, keyEsc)
	if got := strings.Count(m.kitEditor.vals.agentInstructions, "\n") + 1; got != 3 {
		t.Errorf("CRLF text came back with %d lines", got)
	}
	if m.guardDirty(gKitItem) {
		t.Error("open and close with no edit should leave the form clean (CRLF)")
	}

	m = enlargedOn(t, "a\tb")
	m, _ = update(m, keyEsc)
	if m.guardDirty(gKitItem) {
		t.Errorf("open and close with no edit should leave the form clean (tab): %q", m.kitEditor.vals.agentInstructions)
	}
}

func TestEnlargeUnderAPasswordPrompt(t *testing.T) {
	m := enlargedOn(t, "x")
	m.hosts.connecting, m.hosts.pwMode, m.hosts.pwTarget = true, pwSignIn, "far-host"
	if !strings.Contains(m.View(), "Sign in to far-host") {
		t.Error("password prompt hidden under the enlarged view")
	}
}

func TestEnlargeBeyondTheInlineLineCap(t *testing.T) {
	m := enlargedOn(t, numbered(150))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlEnd})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = typeIn(m, "x")
	if n := m.enlarge.ta.LineCount(); n != 151 {
		t.Fatalf("enlarged view has %d lines, want 151", n)
	}
	m, _ = update(m, keyEsc)

	// Inline, the field is past huh's 99-line cap: enter adds no line.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if n := strings.Count(m.kitEditor.vals.agentInstructions, "\n") + 1; n != 151 {
		t.Errorf("inline enter at line 151 gave %d lines", n)
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	m = settle(m, cmd)
	if ai := m.kitEditor.kit.AgentInstructions; ai == nil || strings.Count(ai.Content, "\n")+1 != 151 {
		t.Error("the kit should save all 151 lines")
	}
}

func TestReconcileClosesAnEnlargedViewWhoseSurfaceIsGone(t *testing.T) {
	m := enlargedOn(t, "x")
	m.kitEditor.form = nil
	m.reconcileGuardLayers()
	if m.enlarge.open {
		t.Error("the view outlived its form")
	}
}
