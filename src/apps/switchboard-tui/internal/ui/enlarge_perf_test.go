package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// SC-006 responsiveness benchmarks (research R9): one keystroke on a 200×50
// terminal with a 5,000-line text of ~85-char lines, measured as Model.Update
// plus Model.View through the real key path. Reference numbers from R9 (24-core
// dev container): enlarged type 34.8 ms, cursor 34.7 ms, page 36.7 ms; inline
// focused type 54.6 ms, cursor 53.3 ms; another field of that form 52.9 ms.
// The budget is 100 ms. These are not a CI timing gate (shared runners make
// wall-clock assertions flaky); quickstart Scenario 7 runs them:
//
//	go test ./internal/ui/ -run '^$' -bench SC006 -benchtime 20x

const sc006Lines = 5000

// sc006Text is the 5,000-line text, each line ~85 chars.
func sc006Text() string {
	line := strings.Repeat("lorem ipsum dolor sit amet ", 3) + "consectetur"
	lines := make([]string, sc006Lines)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// sc006Form is the MCP add form at 200×50 with the big text in its Headers
// field, focused on headers (focusText) or on the Name input.
func sc006Form(b *testing.B, focusText bool) Model {
	b.Helper()
	m := pressCmd(mcpModel(b, &fakeDaemon{}), "a")
	m, _ = update(m, tea.WindowSizeMsg{Width: 200, Height: 50})
	m.mcpView.vals.headers = sc006Text()
	m.reopenForm(gMcp)
	if focusText {
		for txt := range m.mcpView.spec.texts {
			focusField(b, m.mcpView.form, txt)
		}
	}
	_ = m.View()
	return m
}

// sc006Enlarged is sc006Form enlarged, the cursor mid-text.
func sc006Enlarged(b *testing.B) Model {
	b.Helper()
	m := sc006Form(b, true)
	m, _ = update(m, keyCtrlE)
	if !m.enlarge.open {
		b.Fatal("ctrl+e did not open the view")
	}
	for range sc006Lines / 2 {
		m.enlarge.ta.CursorDown()
	}
	m.enlarge.ta, _ = m.enlarge.ta.Update(nil)
	_ = m.View()
	return m
}

// sc006Run times keys[i%len(keys)] through Update and View b.N times.
func sc006Run(b *testing.B, m Model, keys ...tea.KeyMsg) {
	b.Helper()
	b.ResetTimer()
	for i := range b.N {
		m, _ = update(m, keys[i%len(keys)])
		_ = m.View()
	}
}

var (
	sc006Type = []tea.KeyMsg{press("x"), {Type: tea.KeyBackspace}}
	sc006Move = []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyUp}}
)

func BenchmarkSC006EnlargedType(b *testing.B) { sc006Run(b, sc006Enlarged(b), sc006Type...) }

func BenchmarkSC006EnlargedCursor(b *testing.B) { sc006Run(b, sc006Enlarged(b), sc006Move...) }

func BenchmarkSC006EnlargedPage(b *testing.B) {
	sc006Run(b, sc006Enlarged(b), tea.KeyMsg{Type: tea.KeyPgDown}, tea.KeyMsg{Type: tea.KeyPgUp})
}

func BenchmarkSC006InlineFocusedType(b *testing.B) { sc006Run(b, sc006Form(b, true), sc006Type...) }

func BenchmarkSC006InlineFocusedCursor(b *testing.B) { sc006Run(b, sc006Form(b, true), sc006Move...) }

func BenchmarkSC006OtherField(b *testing.B) {
	m := sc006Form(b, false)
	if _, ok := m.mcpView.form.GetFocusedField().(*huh.Input); !ok {
		b.Fatalf("expected the Name input, got %T", m.mcpView.form.GetFocusedField())
	}
	sc006Run(b, m, sc006Type...)
}
