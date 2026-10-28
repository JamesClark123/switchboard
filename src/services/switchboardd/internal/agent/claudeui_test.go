package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// The fixtures are the screens research R1 recorded, captured from Claude Code
// 2.1.312 by TestCaptureFixtures, except auto-mode-dialog, which is
// hand-authored (no start-up dialog appeared on the capture).

// confirm is a fixture's expected read-back of one command.
type confirm struct {
	cmd     string // the echoed command
	prefix  string // the confirmation line starts with this
	model   string // ModelConfirmed holds for this family ("" = not checked)
	notFam  string // ModelConfirmed fails for this family
	effort  string // EffortConfirmed holds for this level
	notLvl  string // EffortConfirmed fails for this level
	missing bool   // no confirmation line is expected
}

func TestClaudeUIFixtures(t *testing.T) {
	tests := []struct {
		name    string
		text    string // InputBox text
		present bool
		ready   bool
		modal   bool
		step    string // StepInBox(text, step) must hold ("" = not checked)
		confirm *confirm
	}{
		// The placeholder hint is faint: the box reads empty.
		{name: "startup-placeholder", present: true, ready: true},
		{name: "empty-after-command", present: true, ready: true,
			confirm: &confirm{cmd: "/effort medium", prefix: "Set effort level to medium (saved", effort: "medium", notLvl: "high"}},
		{name: "model-set-opus", present: true, ready: true,
			confirm: &confirm{cmd: "/model opus", prefix: "Set model to Opus 5.5", model: "Opus", notFam: "Haiku"}},
		{name: "model-set-fable", present: true, ready: true,
			confirm: &confirm{cmd: "/model fable", prefix: "Set model to Fable 5.1", model: "Fable", notFam: "Opus"}},
		{name: "model-set-sonnet", present: true, ready: true,
			confirm: &confirm{cmd: "/model sonnet", prefix: "Set model to Sonnet 5.5", model: "Sonnet", notFam: "Fable"}},
		{name: "model-set-haiku", present: true, ready: true,
			confirm: &confirm{cmd: "/model haiku", prefix: "Set model to Haiku 5.5", model: "Haiku", notFam: "Sonnet"}},
		// An unknown model reads back as not confirmed, with the same empty box.
		{name: "model-not-found", present: true, ready: true,
			confirm: &confirm{cmd: "/model bogus", prefix: "Model 'bogus' not found", notFam: "bogus"}},
		{name: "effort-set-low", present: true, ready: true,
			confirm: &confirm{cmd: "/effort low", prefix: "Set effort level to low (saved", effort: "low", notLvl: "medium"}},
		{name: "effort-set-medium", present: true, ready: true,
			confirm: &confirm{cmd: "/effort medium", prefix: "Set effort level to medium (saved", effort: "medium", notLvl: "low"}},
		{name: "effort-set-high", present: true, ready: true,
			confirm: &confirm{cmd: "/effort high", prefix: "Set effort level to high (saved", effort: "high", notLvl: "xhigh"}},
		{name: "effort-set-xhigh", present: true, ready: true,
			confirm: &confirm{cmd: "/effort xhigh", prefix: "Set effort level to xhigh (saved", effort: "xhigh", notLvl: "high"}},
		{name: "effort-max-session", present: true, ready: true,
			confirm: &confirm{cmd: "/effort max", prefix: "Set effort level to max (this session only)", effort: "max", notLvl: "auto"}},
		{name: "effort-auto", present: true, ready: true,
			confirm: &confirm{cmd: "/effort auto", prefix: "Effort level set to auto", effort: "auto", notLvl: "high"}},
		{name: "effort-invalid", present: true, ready: true,
			confirm: &confirm{cmd: "/effort bogus", prefix: "Invalid argument: bogus", notLvl: "bogus"}},
		// /clear redraws to an empty box; it has no result line.
		{name: "clear-banner", present: true, ready: true, confirm: &confirm{cmd: "/clear", missing: true}},
		// The bare /model picker: no box, a modal, nothing confirmed yet.
		{name: "model-picker", modal: true, confirm: &confirm{cmd: "/model", missing: true}},
		{name: "auto-mode-dialog", modal: true},
		{name: "text-left-in-box", text: fixtureTextLeft, present: true, ready: true, step: fixtureTextLeft},
		// The autocomplete above the box is not a modal.
		{name: "typing-slash-autocomplete", text: "/mod", present: true, ready: true, step: "/model opus"},
		{name: "framed-paste-in-box", text: "[Pasted text #1 +6 lines]", present: true, ready: true, step: fixtureFramedPrompt},
		{name: "long-prompt-wrapped", text: fixtureLongPrompt, present: true, ready: true, step: fixtureLongPrompt},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		seen[tt.name] = true
		t.Run(tt.name, func(t *testing.T) {
			lines := loadFixture(t, tt.name)
			text, present, ready := InputBox(lines)
			if text != tt.text || present != tt.present || ready != tt.ready {
				t.Errorf("InputBox = (%q, %v, %v), want (%q, %v, %v)", text, present, ready, tt.text, tt.present, tt.ready)
			}
			if got := ModalOpen(lines); got != tt.modal {
				t.Errorf("ModalOpen = %v, want %v", got, tt.modal)
			}
			if tt.step != "" && !StepInBox(text, tt.step) {
				t.Errorf("StepInBox(%q, %q) = false, want true", text, tt.step)
			}
			if tt.step == "" && StepInBox(text, "/model opus") {
				t.Errorf("StepInBox(%q, /model opus) = true for a box without the step", text)
			}
			if c := tt.confirm; c != nil {
				checkConfirm(t, lines, c)
			}
		})
	}
	// Every fixture on disk is covered.
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if name := strings.TrimSuffix(filepath.Base(f), ".txt"); !seen[name] {
			t.Errorf("fixture %s has no test case", name)
		}
	}
}

func checkConfirm(t *testing.T, lines []termview.Line, c *confirm) {
	t.Helper()
	line, ok := ConfirmationAfter(lines, c.cmd)
	if c.missing {
		if ok {
			t.Errorf("ConfirmationAfter(%q) = %q, want none", c.cmd, line)
		}
		return
	}
	if !ok || !strings.HasPrefix(line, c.prefix) {
		t.Fatalf("ConfirmationAfter(%q) = %q, %v; want a line starting %q", c.cmd, line, ok, c.prefix)
	}
	if c.model != "" && !ModelConfirmed(line, c.model) {
		t.Errorf("ModelConfirmed(%q, %q) = false", line, c.model)
	}
	if c.notFam != "" && ModelConfirmed(line, c.notFam) {
		t.Errorf("ModelConfirmed(%q, %q) = true", line, c.notFam)
	}
	if c.effort != "" && !EffortConfirmed(line, c.effort) {
		t.Errorf("EffortConfirmed(%q, %q) = false", line, c.effort)
	}
	if c.notLvl != "" && EffortConfirmed(line, c.notLvl) {
		t.Errorf("EffortConfirmed(%q, %q) = true", line, c.notLvl)
	}
}

// A wrapped confirmation line is joined, and the last echo wins.
func TestConfirmationAfterJoinsWrapsAndTakesTheLastEcho(t *testing.T) {
	lines := loadFixture(t, "effort-set-high")
	line, ok := ConfirmationAfter(lines, "/effort high")
	if !ok || !strings.HasSuffix(line, "extensive testing and documentation") {
		t.Fatalf("ConfirmationAfter = %q, %v; want the wrapped row joined", line, ok)
	}
	screen := rows("❯ /model opus", "  ⎿  Model 'opus' not found", "", "❯ /model opus", "  ⎿  Set model to Opus 5.5")
	if line, _ := ConfirmationAfter(screen, "/model opus"); line != "Set model to Opus 5.5" {
		t.Fatalf("ConfirmationAfter = %q, want the last echo's line", line)
	}
	if n := Echoes(screen, "/model opus"); n != 2 {
		t.Fatalf("Echoes = %d, want 2", n)
	}
	if n := Echoes(loadFixture(t, "model-set-opus"), "/model opus"); n != 1 {
		t.Fatalf("Echoes on model-set-opus = %d, want 1", n)
	}
}

// rows builds a screen from plain rows (nothing faint).
func rows(texts ...string) []termview.Line {
	out := make([]termview.Line, len(texts))
	for i, s := range texts {
		out[i] = termview.Line{Text: s, Faint: make([]bool, len([]rune(s)))}
	}
	return out
}

func TestConfirmationAfterEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		screen []termview.Line
	}{
		{"no echo", rows("❯ /effort low", "  ⎿  Set effort level to low")},
		{"echo on the last row", rows("❯ /model opus")},
		{"no result under the echo", rows("❯ /model opus", "", "  ⎿  Set model to Opus")},
		{"the box's prompt is not an echo", rows("────", "❯ /model opus", "────")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if line, ok := ConfirmationAfter(tt.screen, "/model opus"); ok {
				t.Fatalf("ConfirmationAfter = %q, want none", line)
			}
		})
	}
}

func TestInputBoxEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		screen  []termview.Line
		text    string
		present bool
		ready   bool
	}{
		{"no rules", rows("❯ hello"), "", false, false},
		{"one rule", rows("────", "❯ hi"), "", false, false},
		{"adjacent rules", rows("────", "────"), "", false, false},
		{"no prompt between the rules", rows("────", "hello", "────"), "", false, false},
		{"a bare prompt", rows("────", "❯", "────"), "", true, true},
		{"plain space prompt", rows("────", "❯ typed", "────"), "typed", true, true},
		{"prompt glyph without a space", rows("────", "❯x", "────"), "", false, false},
		{"a short rule is not a rule", rows("──", "❯ x", "──"), "", false, false},
		{"modal under the box", rows("────", "❯ x", "────", "  Esc to cancel"), "x", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, present, ready := InputBox(tt.screen)
			if text != tt.text || present != tt.present || ready != tt.ready {
				t.Fatalf("InputBox = (%q, %v, %v), want (%q, %v, %v)", text, present, ready, tt.text, tt.present, tt.ready)
			}
		})
	}
}

func TestModalOpenIgnoresTheTranscript(t *testing.T) {
	// A marker in an older command's result, or a submitted "1. …" prompt, is
	// not a modal.
	screen := rows("❯ /help", "  ⎿  Enter to select · Esc to cancel", "❯ 1. fix the build", "  ⎿  ok", "────", "❯ ", "────")
	if ModalOpen(screen) {
		t.Fatal("markers above the transcript's last entry should not count")
	}
	if !ModalOpen(rows("   Select model", "   ❯ 2.  Opus")) {
		t.Fatal("a numbered option row with no transcript is a modal")
	}
}

func TestStepInBox(t *testing.T) {
	tests := []struct {
		name string
		box  string
		step string
		want bool
	}{
		{"exact", "/model opus", "/model opus", true},
		{"wrapping is ignored", "fix the  build now", "fix the build now", true},
		{"a truncated display", "fix the bu…", "fix the build now", true},
		{"a paste placeholder", "[Pasted text #3 +12 lines]", "any\nmulti-line\nstep", true},
		{"an empty box", "", "/model opus", false},
		{"the developer's text", "something else", "/model opus", false},
		{"the step plus more typing", "/model opus please", "/model opus", false},
		{"a placeholder among other text", "x [Pasted text #1 +2 lines]", "a\nb\nc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StepInBox(tt.box, tt.step); got != tt.want {
				t.Fatalf("StepInBox(%q, %q) = %v, want %v", tt.box, tt.step, got, tt.want)
			}
		})
	}
}

func TestConfirmedMatchers(t *testing.T) {
	tests := []struct {
		name string
		got  bool
		want bool
	}{
		{"model exact", ModelConfirmed("Set model to Opus", "Opus"), true},
		{"model with version", ModelConfirmed("Set model to Opus 5.5 and saved as your default for new sessions", "Opus"), true},
		{"model empty family", ModelConfirmed("Set model to Opus", ""), false},
		{"model other family", ModelConfirmed("Set model to Opus 5.5", "Haiku"), false},
		{"model prefix of a longer name", ModelConfirmed("Set model to Opusx 5", "Opus"), false},
		{"model kept by a picker", ModelConfirmed("Kept model as Opus 5.5", "Opus"), false},
		{"effort exact", EffortConfirmed("Set effort level to low", "low"), true},
		{"effort empty level", EffortConfirmed("Set effort level to low", ""), false},
		{"effort high is not xhigh", EffortConfirmed("Set effort level to xhigh (saved …)", "high"), false},
		{"effort auto", EffortConfirmed(" Effort level set to auto ", "auto"), true},
		{"effort auto is not resolved level", EffortConfirmed("Set effort level to auto (saved)", "auto"), false},
		{"effort invalid", EffortConfirmed("Invalid argument: bogus. Valid options are: low", "low"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

// The faint sidecar matters: without it the placeholder hint reads as text.
func TestPlaceholderNeedsTheFaintSidecar(t *testing.T) {
	text, err := os.ReadFile(filepath.Join(fixtureDir, "startup-placeholder.txt"))
	if err != nil {
		t.Fatal(err)
	}
	unstyled := decodeFixture(string(text), "")
	if got, _, _ := InputBox(unstyled); !strings.HasPrefix(got, "Try ") {
		t.Fatalf("InputBox without faint = %q, want the hint text", got)
	}
}
