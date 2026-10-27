package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Untrusted text cannot alter the terminal or spill into another column, keeps
// its line breaks and has its tabs expanded (FR-029, SC-008).
func TestSanitizeTerminalText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "Reviewed PR 412", "Reviewed PR 412"},
		{"osc52 clipboard (BEL)", "a\x1b]52;c;aGVsbG8=\x07b", "ab"},
		{"osc title (ST)", "a\x1b]0;pwned\x1b\\b", "ab"},
		{"osc8 hyperlink", "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"clear screen + cursor", "\x1b[2J\x1b[H\x1b[31mred\x1b[0m", "red"},
		{"dcs", "a\x1bP1$r0m\x1b\\b", "ab"},
		{"crlf and cr", "one\r\ntwo\rthree", "one\ntwo\nthree"},
		{"backspace, bell, del", "a\bb\x07c\x7fd", "abcd"},
		{"c1 controls", "a\u009b2Jb\u0085c", "a2Jbc"},
		{"bidi override", "safe \u202Eexe.txt", "safe exe.txt"},
		{"isolates and zero width", "a\u2066b\u2069c\u200Bd\uFEFFe\u061Cf", "abcdef"},
		{"invalid utf8", "a\xffb", "a�b"},
		{"truncated sequence at the end", "done\x1b[3", "done"},
		{"tab expansion", "a\tb\n\tc\nabcd\te", "a   b\n    c\nabcd    e"},
		{"newlines kept", "line 1\n\nline 3", "line 1\n\nline 3"},
		{"wide runes", "日本\tx", "日本    x"},
	}
	for _, c := range cases {
		if got := sanitizeTerminalText(c.in); got != c.want {
			t.Errorf("%s: sanitizeTerminalText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	// No escape byte or control character survives a hostile mix.
	got := sanitizeTerminalText("x\x1b]52;c;Zm9v\x07\x1b[2J\x1b7\x1b8\x9b31m\x00y")
	for _, r := range got {
		if r == 0x1b || (r < 0x20 && r != '\n') || (r >= 0x7f && r < 0xa0) {
			t.Fatalf("control rune %U survived in %q", r, got)
		}
	}
}

// A tab-expanded line measures what the terminal draws, so wrapping is right.
func TestSanitizeTabsMeasureCorrectly(t *testing.T) {
	s := sanitizeTerminalText("aaaa\tbbbb\tcccc")
	if strings.Contains(s, "\t") || ansi.StringWidth(s) != len("aaaa    bbbb    cccc") {
		t.Fatalf("tabs should expand to the next multiple of %d: %q", tabWidth, s)
	}
}

// Row text is one line, cut by display width, never mid-rune or mid-sequence.
func TestSanitizeLine(t *testing.T) {
	if got := sanitizeLine("one\ntwo\r\nthree", 0); got != "one two three" {
		t.Errorf("line breaks fold to spaces: %q", got)
	}
	got := sanitizeLine("héllo wörld 日本語テキスト", 14)
	if ansi.StringWidth(got) > 14 || !strings.HasSuffix(got, "…") || strings.ContainsRune(got, '�') {
		t.Errorf("cut by width with an ellipsis, runes intact: %q (width %d)", got, ansi.StringWidth(got))
	}
	if got := sanitizeLine("ok\x1b[2J then more text", 6); strings.ContainsRune(got, 0x1b) || ansi.StringWidth(got) > 6 {
		t.Errorf("sequence removed before cutting: %q", got)
	}
	if got := fitCol("a-very-long-orchestrator-name", 10); ansi.StringWidth(got) != 10 {
		t.Errorf("fitCol truncates to the width: %q", got)
	}
	if got := fitCol("ab", 5); got != "ab   " {
		t.Errorf("fitCol pads to the width: %q", got)
	}
}

// Every format character and the line/paragraph separators are dropped, not
// only the bidi controls listed by hand before (review run-224).
func TestSanitizeDropsAllFormatCharacters(t *testing.T) {
	in := "a­b c d\U000E0041e᠎f"
	if got := sanitizeTerminalText(in); got != "abcdef" {
		t.Errorf("sanitizeTerminalText(%q) = %q", in, got)
	}
}
