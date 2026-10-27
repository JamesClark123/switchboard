package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Untrusted text (feature 010, FR-029, research R10). Run summaries, reasons,
// composed prompts, notification messages and the daemon's error strings are
// written by agents and remote daemons, and an agent works on untrusted input (PR
// bodies, issue text): drawn raw, such text could clear the screen, move the
// cursor, retitle the terminal, write the clipboard (OSC 52) or reorder what is
// shown with bidi controls. Everything of that kind is passed through one of the
// two functions below where it is rendered — never on stored data.

// tabWidth is the column multiple a tab expands to. Raw tabs cannot be kept: the
// width functions (ansi.StringWidth, lipgloss.Width, ansi.Wrap) count a tab as no
// columns while a terminal draws up to eight, so wrapped text would overflow.
const tabWidth = 4

// invisibleFormat reports the characters that can reorder, hide or displace
// text: every Unicode format character (Cf: the bidi controls, zero-width
// characters, the BOM, tag characters and the rest) and the line and paragraph
// separators, which some terminals break lines on.
func invisibleFormat(r rune) bool {
	return r == 0x2028 || r == 0x2029 || unicode.Is(unicode.Cf, r)
}

// sanitizeTerminalText makes untrusted text safe to draw in full: escape
// sequences (CSI, OSC, DCS, SOS/PM/APC and the rest) are removed, carriage
// returns become line breaks, every other control character (C0 but newline and
// tab, DEL, C1) and every bidi/invisible formatting character is dropped, invalid
// UTF-8 becomes U+FFFD, and tabs are expanded to spaces. Line breaks are kept.
func sanitizeTerminalText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = ansi.Strip(s)
	var b strings.Builder
	b.Grow(len(s))
	col := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\n':
			b.WriteByte('\n')
			col = 0
		case r == '\t':
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case unicode.IsControl(r), invisibleFormat(r):
			// dropped
		default:
			b.WriteRune(r)
			col += ansi.StringWidth(string(r))
		}
	}
	return b.String()
}

// sanitizeLine is untrusted text for a single-line row: sanitised, line breaks
// shown as spaces, then cut to width display columns with an ellipsis. It never
// splits a character or leaves part of a sequence; width <= 0 means no limit.
func sanitizeLine(s string, width int) string {
	s = strings.ReplaceAll(sanitizeTerminalText(s), "\n", " ")
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// fitCol is a fixed-width row column: untrusted text cut to width display
// columns and padded to it, so long names cannot push the columns out of line.
func fitCol(s string, width int) string {
	s = sanitizeLine(s, width)
	if w := ansi.StringWidth(s); w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}
