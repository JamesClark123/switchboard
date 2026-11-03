package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// The fixtures are the screens research R1 recorded, captured from Claude Code
// 2.1.312 by TestCaptureFixtures, except auto-mode-dialog, which is
// hand-authored (no start-up dialog appeared on the capture), and those of a real
// turn, captured on Linux from 2.1.316 by TestCaptureTurnFixtures.

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
		// A real turn (SWI-28): the box reads empty and ready all through it.
		{name: "turn-before", present: true, ready: true},
		{name: "turn-running", present: true, ready: true},
		{name: "turn-running-narrow", present: true, ready: true},
		{name: "reply-row", present: true, ready: true},
		{name: "effort-repeat-before", present: true, ready: true,
			confirm: &confirm{cmd: "/effort medium", prefix: "Set effort level to medium (saved", effort: "medium", notLvl: "high"}},
		{name: "effort-repeat-after", present: true, ready: true,
			confirm: &confirm{cmd: "/effort medium", prefix: "Set effort level to medium (saved", effort: "medium", notLvl: "high"}},
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
	if at := LastEcho(screen, "/model opus"); at != 3 {
		t.Fatalf("LastEcho = %d, want the second echo's row", at)
	}
	if at := LastEcho(screen, "/model haiku"); at != -1 {
		t.Fatalf("LastEcho of no echo = %d, want -1", at)
	}
}

// The glyph a real reply draws on Linux (SWI-28).
func TestLastReplyOnARealScreen(t *testing.T) {
	lines := loadFixture(t, "reply-row")
	if at := LastReply(lines); at < 0 || lines[at].Text != "● pong" {
		t.Fatalf("LastReply = %d on:\n%s", at, encodeRows(lines))
	}
	// Before the turn: no reply; the right-aligned `◐` or `●` effort row isn't one.
	if at := LastReply(loadFixture(t, "effort-set-medium")); at != -1 {
		t.Fatalf("LastReply with no reply = %d", at)
	}
}

func TestTurnRunning(t *testing.T) {
	for _, name := range []string{"turn-running", "turn-running-narrow"} {
		lines := loadFixture(t, name)
		if !TurnRunning(lines) {
			t.Errorf("%s: a running turn must read as running:\n%s", name, encodeRows(lines))
		}
		// The box alone says nothing: that is why the footer is read.
		if text, present, ready := InputBox(lines); !present || !ready || strings.TrimSpace(text) != "" {
			t.Errorf("%s: box = %q %v %v", name, text, present, ready)
		}
	}
	for _, name := range []string{"reply-row", "startup-placeholder", "empty-after-command", "effort-repeat-after", "model-picker"} {
		if TurnRunning(loadFixture(t, name)) {
			t.Errorf("%s: no turn is running", name)
		}
	}
	// Quoted in the transcript, or with no box on screen, it doesn't count.
	quoted := append(rows("❯ what does esc to interrupt do?"), loadFixture(t, "empty-after-command")[1:]...)
	if TurnRunning(quoted) {
		t.Error("a transcript quoting the hint is no turn")
	}
	if TurnRunning(rows("esc to interrupt")) {
		t.Error("no box, no footer")
	}
}

func TestTranscriptLeavesOutTheBoxAndTheUIOverIt(t *testing.T) {
	got := TranscriptTexts(loadFixture(t, "effort-repeat-after"))
	for _, r := range got {
		if strings.Contains(r, "/effort") && !strings.HasPrefix(r, "❯ /effort") {
			t.Errorf("the effort indicator is no transcript row: %q", r)
		}
		if strings.TrimSpace(r) == "" {
			t.Error("blank rows are left out")
		}
	}
	if len(got) == 0 || got[len(got)-1] == "❯" {
		t.Fatalf("transcript = %q", got)
	}
	if got := TranscriptTexts(rows("a", "", "b")); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("no box = %q", got)
	}
}

func TestAdded(t *testing.T) {
	const e, r = "❯ /effort medium", "  ⎿  Set"
	tests := []struct {
		name        string
		before, now []string
		added       []string
		notAligned  bool
	}{
		{name: "nothing before", now: []string{"a"}, added: []string{"a"}},
		{name: "unchanged", before: []string{"a", e, r}, now: []string{"a", e, r}},
		{name: "appended", before: []string{"a"}, now: []string{"a", e, r}, added: []string{e, r}},
		{name: "appended as the top moved off", before: []string{"a", "b", e, r}, now: []string{"b", e, r, e, r}, added: []string{e, r}},
		{name: "only moved up", before: []string{"a", e, r}, now: []string{e, r}},
		{name: "a repeat of the rows before", before: []string{e, r, e, r}, now: []string{r, e, r, e, r}, added: []string{e, r}},
		{name: "one row shared", before: []string{"a", "b"}, now: []string{"b", "c", "d"}, added: []string{"c", "d"}},
		{name: "nothing shared", before: []string{"a", "b"}, now: []string{"c", "d"}, added: []string{"c", "d"}, notAligned: true},
		{name: "cleared", before: []string{"a", "b"}, notAligned: true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			added, aligned := Added(c.before, rows(c.now...))
			if aligned == c.notAligned {
				t.Errorf("aligned = %v", aligned)
			}
			got := []string{}
			for _, l := range added {
				got = append(got, l.Text)
			}
			if !slices.Equal(got, append([]string{}, c.added...)) {
				t.Errorf("added = %q, want %q", got, c.added)
			}
		})
	}
	// The top rows changing on their own (a notice hiding) doesn't lose the match.
	long := make([]string, alignRows+2)
	for i := range long {
		long[i] = fmt.Sprintf("row %d", i)
	}
	now := append(append([]string{"notice gone"}, long[1:]...), "new")
	if added, aligned := Added(long, rows(now...)); !aligned || len(added) != 1 || added[0].Text != "new" {
		t.Errorf("top row changed = %v, %v", added, aligned)
	}
}

// On real screens the echo typed lands on the row of the one before it, which is
// why positions can't tell them apart.
func TestAddedOnRealFrames(t *testing.T) {
	before, after := loadFixture(t, "effort-repeat-before"), loadFixture(t, "effort-repeat-after")
	if b, a := LastEcho(before, "/effort medium"), LastEcho(after, "/effort medium"); a != b {
		t.Fatalf("the frames no longer show the case: last echo on row %d, then %d", b, a)
	}
	added, aligned := Added(TranscriptTexts(before), Transcript(after))
	if !aligned || LastEcho(added, "/effort medium") < 0 {
		t.Errorf("added = %v, aligned = %v", added, aligned)
	}
	added, aligned = Added(TranscriptTexts(after), Transcript(after))
	if !aligned || len(added) != 0 {
		t.Errorf("same frame: added = %v, aligned = %v", added, aligned)
	}
}

func TestLastReply(t *testing.T) {
	if at := LastReply(rows("❯ hi", "⏺ first", "", "⏺ second", "  ⎿  done", "")); at != 3 {
		t.Fatalf("LastReply = %d, want the last reply's row", at)
	}
	if at := LastReply(rows("❯ hi", "● on linux", "")); at != 1 {
		t.Fatalf("LastReply of the non-macOS glyph = %d, want its row", at)
	}
	// A row below the input box is no reply.
	boxed := append(loadFixture(t, "empty-after-command"), rows("● a status row")...)
	if at := LastReply(boxed); at != -1 {
		t.Fatalf("LastReply below the box = %d, want -1", at)
	}
	// A prompt quoting a reply is indented under its echo: not a reply.
	if at := LastReply(rows("❯ quoting:", "  ⏺ not a reply")); at != -1 {
		t.Fatalf("LastReply of a quoted glyph = %d, want -1", at)
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

func TestModalOpenIgnoresAnAssistantMessageAboveTheBox(t *testing.T) {
	// The agent's last message quotes the markers (and a numbered option);
	// the box under it is empty and ready, not a dialog.
	screen := rows("❯ review the PR", "",
		"⏺ The dialog footer reads \"Enter to confirm · Esc to cancel\", and",
		"  the picker marks its current option:", "   ❯ 2. Opus", "",
		"────", "❯\u00a0", "────", "  ⏵⏵ bypass permissions on (shift+tab to cycle)")
	if ModalOpen(screen) {
		t.Fatal("markers in an assistant message above the box should not count")
	}
	if _, present, ready := InputBox(screen); !present || !ready {
		t.Fatalf("InputBox present=%v ready=%v, want an empty ready box", present, ready)
	}
	// The same message with no box under it (the dialog replaced it) is read
	// as before.
	if !ModalOpen(rows("⏺ done", "▔▔▔▔", "   ❯ 1. Yes", "   Enter to confirm · Esc to cancel")) {
		t.Fatal("a dialog in place of the box is a modal")
	}
}

// SWI-28: what Claude Code 2.1.317 reported for bracketed pastes it was typed
// (captured through a UserPromptSubmit hook), and prompts that aren't the step.
func TestStepPrompt(t *testing.T) {
	for _, c := range []struct {
		name, step, prompt string
		want               bool
	}{
		{"as typed", "/model opus", "/model opus", true},
		{"csi colours", "\x1b[1;31mTestFoo\x1b[0m bar", "TestFoo bar", true},
		{"csi 256 colours", "a \x1b[38;5;196mred\x1b[0m z", "a red z", true},
		{"csi private mode", "a \x1b[?25lhidden\x1b[?25h z", "a hidden z", true},
		{"csi erase and move", "a \x1b[2Kline\x1b[1A z", "a line z", true},
		{"osc link, bel", "see \x1b]8;;http://x.test/a1\x07link\x1b]8;;\x07 end", "see link end", true},
		{"osc link, st", "see \x1b]8;;http://x.test/b2\x1b\\link\x1b]8;;\x1b\\ end", "see link end", true},
		{"osc title", "a \x1b]0;mytitle\x07body z", "a body z", true},
		{"esc and a letter", "abc\x1bxyz end", "abcyz end", true},
		{"esc and a digit", "abc\x1b7xyz\x1b8 end", "abcxyz end", true},
		{"esc, space and a letter", "abc\x1b xyz end", "abcyz end", true},
		{"esc charset", "abc\x1b(Bxyz end", "abcxyz end", true},
		{"dcs", "abc\x1bPpayload9\x1b\\xyz end", "abcxyz end", true},
		{"esc at the end", "abc xyz\x1b", "abc xyz", true},
		{"c1 csi", "abc\u009b1mxyz end", "abc1mxyz end", true},
		{"closing tag", "quote </pasted_content> end", "quote <\\/pasted_content> end", true},
		{"opening tag", "quote <pasted_content> end", "quote <\\pasted_content> end", true},
		{"zero-width", "ab\u200bcd\u200ce\u200df\u2060g\ufeffh\u00adi end", "abcdefghi end", true},
		{"control characters", "ab\x01cd\x08ef\x1fgh\x7fij end", "abcdefghij end", true},
		{"emoji and no-break space", "warn ⚠️ fam 👨‍👩‍👧 nb\u00a0sp end", "warn ⚠️ fam 👨‍👩‍👧 nb\u00a0sp end", true},
		{"composed accents", "cafe\u0301 ñandú 日本語 end", "caf\u00e9 ñandú 日本語 end", true},
		{"composed hangul", "\u1112\u1161\u11ab end", "\ud55c end", true},
		{"composed marks", "o\u0308 a\u030a e\u0301\u0323 end", "\u00f6 \u00e5 \u1eb9\u0301 end", true},
		{"a long paste keeps its accents", "cafe\u0301\nrow", "\n\n<pasted_content id=\"205a\">\ncafe\u0301\nrow\n</pasted_content id=\"205a\">\n", true},
		{"a long paste loses its escapes", "\x1b[32mline 0\x1b[0m\tcol 0\n\x1b[32mline 1\x1b[0m\tcol 1", "\n\n<pasted_content id=\"205a\">\nline 0    col 0\nline 1    col 1\n</pasted_content id=\"205a\">\n", true},
		{"a long paste quotes a tag", "first </pasted_content> x\nrow 0", "\n\n<pasted_content id=\"205a\">\nfirst <\\/pasted_content> x\nrow 0\n</pasted_content id=\"205a\">\n", true},
		{"another prompt", "/prepare T-1", "what is going on?", false},
		{"different letters", "café", "cafe", false},
		{"different digits", "/prepare T-1", "/prepare T-2", false},
		{"no letters or digits, the same", "? ?", "??", true},
		{"no letters or digits, another prompt", "???", "what is going on?", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := StepPrompt(c.prompt, c.step); got != c.want {
				t.Errorf("StepPrompt(%q, %q) = %v, want %v", c.prompt, c.step, got, c.want)
			}
		})
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

func encodeRows(lines []termview.Line) string {
	text, _ := encodeFixture(lines)
	return text
}
