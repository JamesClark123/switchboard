package agent

import (
	"regexp"
	"strings"
	"unicode"

	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// claudeui reads Claude Code's screen (SWI-17, research R2): pure functions over
// a termview snapshot (terminal.Broadcaster.Screen) that tell whether the input
// box is empty and ready, whether a step's text is still in it, and which
// confirmation line follows an echoed command. They are pinned by the screens
// captured in research R1 (testdata/claudeui). A screen they do not recognise
// reads as "no input box" / "not ready" / "no confirmation": the safe direction,
// since that halts a step the developer can see rather than typing into
// something unexpected.
//
// What the fixtures show (Claude Code 2.1.312):
//   - the input box is a `❯` row (followed by a no-break space) between two
//     horizontal rules (`────`), with wrapped rows indented two spaces and a
//     footer below the bottom rule; its placeholder hint is drawn faint;
//   - a submitted command is echoed in the transcript as `❯ <text>` (a plain
//     space), with its result on the next row as `  ⎿  <line>`, wrapped rows
//     indented further;
//   - a picker or dialog replaces the box's top rule with a `▔` border and lists
//     numbered options, the current one marked `❯ n.`, over a footer such as
//     `Enter to set as default · s to use this session only · Esc to cancel`;
//   - the `/` command autocomplete lists commands above the box (the selected
//     one marked `❯`, unnumbered) and is not a modal.

const (
	promptGlyph  = "❯"
	resultGlyph  = "⎿"
	resultIndent = "     " // a wrapped result row's indent (the width of "  ⎿  ")
)

// modalMarkers are footer texts only a picker or dialog shows.
var modalMarkers = []string{
	"Esc to cancel",
	"Enter to select",
	"Enter to set as default",
	"Enter to confirm",
}

var (
	// numberedOption is a picker's or dialog's current option row: indented,
	// `❯`, then `n.`. An echoed prompt starts at column 0, so a submitted
	// "1. …" is never mistaken for one.
	numberedOption = regexp.MustCompile(`^\s+❯\s*\d+\.`)
	// pastePlaceholder is how the box shows a multi-line paste.
	pastePlaceholder = regexp.MustCompile(`^\[Pasted text #\d+[^\]]*\]$`)
)

// InputBox reads the input box: every row between the last two horizontal rules,
// starting at the `❯` row, with wrapped rows joined. text is the typed text with
// faint runes (the placeholder hint) left out, so a box showing only its hint
// reads as empty. present is false when no such box is on screen; ready is
// present with no modal on screen (ModalOpen).
func InputBox(lines []termview.Line) (text string, present, ready bool) {
	rows, ok := boxRows(lines)
	if !ok {
		return "", false, false
	}
	parts := make([]string, 0, len(rows))
	for i, l := range rows {
		s := plain(l)
		if i == 0 {
			s = strings.TrimPrefix(s, promptGlyph)
		}
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " "), true, !ModalOpen(lines)
}

// boxRows returns the input box's rows: those between the last two horizontal
// rules, the first being the `❯` row.
func boxRows(lines []termview.Line) ([]termview.Line, bool) {
	top, bottom, ok := boxBounds(lines)
	if !ok {
		return nil, false
	}
	return lines[top+1 : bottom], true
}

// boxBounds returns the indices of the input box's top and bottom rules.
func boxBounds(lines []termview.Line) (top, bottom int, ok bool) {
	bottom, top = -1, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !isRule(lines[i].Text) {
			continue
		}
		if bottom < 0 {
			bottom = i
			continue
		}
		top = i
		break
	}
	if top < 0 || bottom-top < 2 || !isPromptRow(lines[top+1].Text) {
		return 0, 0, false
	}
	return top, bottom, true
}

// isRule reports whether a row is a horizontal rule.
func isRule(s string) bool {
	if len(s) == 0 {
		return false
	}
	n := 0
	for _, r := range s {
		if r != '─' {
			return false
		}
		n++
	}
	return n >= 3
}

// isPromptRow reports whether a row starts with the prompt glyph, alone or
// followed by a space (plain or no-break).
func isPromptRow(s string) bool {
	rest, ok := strings.CutPrefix(s, promptGlyph)
	if !ok {
		return false
	}
	return rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, " ")
}

// plain returns a row's text without its faint runes.
func plain(l termview.Line) string {
	var sb strings.Builder
	for i, r := range []rune(l.Text) {
		if i < len(l.Faint) && l.Faint[i] {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// StepInBox reports whether the box text (InputBox) still holds a step: the box
// shows the beginning of the step's text, compared without whitespace so that
// wrapping does not matter (and never when the box is empty or holds more than
// the step, which is the developer's typing), or the box holds only a paste
// placeholder (a multi-line step such as the framed action prompt).
func StepInBox(text, stepText string) bool {
	text = strings.TrimSpace(text)
	if pastePlaceholder.MatchString(text) {
		return true
	}
	box := strings.TrimSuffix(squash(text), "…")
	return box != "" && strings.HasPrefix(squash(stepText), box)
}

// squash removes every whitespace rune (including the no-break space).
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// ModalOpen reports whether a picker or dialog is on screen: a numbered `❯ n.`
// option row or a modal footer (Esc to cancel, Enter to select / to set as
// default / to confirm). A picker or dialog replaces the input box's top rule,
// so with the box on screen only the rows below it are read: a marker quoted
// in the transcript above it (a command's output, an assistant message) does
// not count. With no box, only rows below the transcript's last entry (its
// last echoed command or result line) are read.
func ModalOpen(lines []termview.Line) bool {
	from := transcriptEnd(lines)
	if _, bottom, ok := boxBounds(lines); ok {
		from = bottom + 1
	}
	for _, l := range lines[from:] {
		if numberedOption.MatchString(l.Text) {
			return true
		}
		for _, m := range modalMarkers {
			if strings.Contains(l.Text, m) {
				return true
			}
		}
	}
	return false
}

// transcriptEnd is the index of the first row after the transcript's last
// echoed command or result line (0 when there is none).
func transcriptEnd(lines []termview.Line) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if isEcho(lines[i].Text) || isResult(lines[i].Text) {
			return i + 1
		}
	}
	return 0
}

// isEcho reports whether a row is an echoed command: `❯` and a plain space at
// column 0 (the input box's prompt uses a no-break space).
func isEcho(s string) bool { return strings.HasPrefix(s, promptGlyph+" ") }

// isResult reports whether a row is a command's result line (`  ⎿  …`).
func isResult(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), resultGlyph) }

// Echoes counts the transcript's echoes of stepText (`❯ <stepText>` rows). The
// submit check counts them before typing, so that only an echo added after the
// step was sent is read back (contracts/worker-step.md §3).
func Echoes(lines []termview.Line, stepText string) int {
	n := 0
	for _, l := range lines {
		if isEchoOf(l.Text, stepText) {
			n++
		}
	}
	return n
}

func isEchoOf(row, stepText string) bool {
	return isEcho(row) && strings.TrimSpace(strings.TrimPrefix(row, promptGlyph+" ")) == strings.TrimSpace(stepText)
}

// ConfirmationAfter returns the result line under the transcript's last echo of
// stepText (`❯ <stepText>`): the text after `⎿` on the next row, with its
// wrapped rows joined. ok is false when there is no such echo, or no result row
// directly under it.
func ConfirmationAfter(lines []termview.Line, stepText string) (string, bool) {
	at := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isEchoOf(lines[i].Text, stepText) {
			at = i
			break
		}
	}
	if at < 0 || at+1 >= len(lines) || !isResult(lines[at+1].Text) {
		return "", false
	}
	parts := []string{strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[at+1].Text), resultGlyph))}
	for _, l := range lines[at+2:] {
		// A wrapped result row is aligned with the text after `  ⎿  `
		// (column 5); anything else (a blank row, the next echo, the
		// right-aligned effort indicator, the box's rules) ends the result.
		if !strings.HasPrefix(l.Text, resultIndent) || strings.HasPrefix(l.Text, resultIndent+" ") {
			break
		}
		parts = append(parts, strings.TrimSpace(l.Text))
	}
	return strings.Join(parts, " "), true
}

// ModelConfirmed reports whether a confirmation line confirms `/model` set the
// given family ("Opus", "Fable", "Sonnet", "Haiku"): `Set model to <Family> …`.
// `Model 'x' not found` and a picker's `Kept model as …` are not.
func ModelConfirmed(line, family string) bool {
	if family == "" {
		return false
	}
	want := "Set model to " + family
	return line == want || strings.HasPrefix(line, want+" ")
}

// EffortConfirmed reports whether a confirmation line confirms `/effort` set the
// given level: `Set effort level to <level> …` for low…max, or exactly `Effort
// level set to auto` for auto. `Invalid argument: …` is not.
func EffortConfirmed(line, level string) bool {
	switch level {
	case "":
		return false
	case "auto":
		return strings.TrimSpace(line) == "Effort level set to auto"
	}
	want := "Set effort level to " + level
	return line == want || strings.HasPrefix(line, want+" ")
}

// assistantGlyph starts an assistant message or tool call in the transcript.
const assistantGlyph = "⏺"

// PostClear reports whether the screen shows a session just cleared (research
// R13, fixture clear-banner): the input box empty and ready, and nothing of a
// conversation above it — no echoed prompt other than `/clear` itself, no
// command result and no assistant message. Clear's read-back and a clear's
// hold release need it as the daemon's own corroboration of the hook.
func PostClear(lines []termview.Line) bool {
	text, present, ready := InputBox(lines)
	if !present || !ready || strings.TrimSpace(text) != "" {
		return false
	}
	top, _, _ := boxBounds(lines)
	for _, l := range lines[:top] {
		switch s := l.Text; {
		case isEcho(s) && !isEchoOf(s, "/clear"), isResult(s), strings.HasPrefix(strings.TrimSpace(s), assistantGlyph):
			return false
		}
	}
	return true
}
