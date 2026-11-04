package editor

import (
	"errors"
	"strings"
	"testing"
)

func words(ws []Word) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Text
	}
	return out
}

func rule(err error) string {
	var ce *CheckError
	if errors.As(err, &ce) {
		return ce.Rule
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

// TestSplit covers every FR-531 bullet and FR-506's syntax rules
// (contracts/editor-launch.md §2).
func TestSplit(t *testing.T) {
	cases := []struct {
		in       string
		want     []string
		wantRule string
	}{
		{`"/Applications/My Editor.app/bin/edit" {path}`, []string{"/Applications/My Editor.app/bin/edit", "{path}"}, ""},
		{`/My\ Editor/edit {path}`, []string{"/My Editor/edit", "{path}"}, ""},
		{`edit \$HOME`, []string{"edit", "$HOME"}, ""},
		{`edit \&`, []string{"edit", "&"}, ""},
		{`edit '$HOME'`, []string{"edit", "$HOME"}, ""},
		{`edit "$HOME"`, nil, RuleShellSyntax},
		{"edit `x`", nil, RuleShellSyntax},
		{"edit \"`x`\"", nil, RuleShellSyntax},
		{`a|b`, nil, RuleShellSyntax},
		{`'a|b'`, []string{"a|b"}, ""},
		{`"a|b;c&d<e>f"`, []string{"a|b;c&d<e>f"}, ""},
		{`a; b`, nil, RuleShellSyntax},
		{`a & b`, nil, RuleShellSyntax},
		{`a < b`, nil, RuleShellSyntax},
		{`a > b`, nil, RuleShellSyntax},
		{`edit ""`, []string{"edit", ""}, ""},
		{`'' x`, []string{"", "x"}, ""},
		{`"" {path}`, []string{"", "{path}"}, ""},
		{`~`, []string{"~"}, ""},
		{`~/x`, []string{"~/x"}, ""},
		{`'~/x'`, []string{"~/x"}, ""},
		{`--dir=~/x`, []string{"--dir=~/x"}, ""},
		{`~user`, []string{"~user"}, ""},
		{"a\tb", []string{"a", "b"}, ""},
		{`a\`, nil, RuleTrailingBackslash},
		{`a "b`, nil, RuleUnbalancedQuotes},
		{`a 'b`, nil, RuleUnbalancedQuotes},
		{"a\nb", nil, RuleControlCharacter},
		{"a\x07b", nil, RuleControlCharacter},
		{"'a\nb'", nil, RuleControlCharacter},
		{``, nil, ""},
		{`   `, nil, ""},
		// Adjacent pieces join; double quotes keep a non-escaping backslash.
		{`a"b c"d`, []string{"ab cd"}, ""},
		{`"a\"b"`, []string{`a"b`}, ""},
		{`"a\\b"`, []string{`a\b`}, ""},
		{`"a\$b"`, []string{`a$b`}, ""},
		{"\"a\\`b\"", []string{"a`b"}, ""},
		{`"a\nb"`, []string{`a\nb`}, ""},
		{`kitty -e sh -c 'cd "$1" && nvim .' sh {path}`, []string{"kitty", "-e", "sh", "-c", `cd "$1" && nvim .`, "sh", "{path}"}, ""},
		{`edit *.txt ?x [a]`, []string{"edit", "*.txt", "?x", "[a]"}, ""},
	}
	for _, c := range cases {
		got, err := Split(c.in)
		if r := rule(err); r != c.wantRule {
			t.Errorf("Split(%q) rule = %q, want %q", c.in, r, c.wantRule)
			continue
		}
		if c.wantRule != "" {
			if got != nil {
				t.Errorf("Split(%q) returned words with an error", c.in)
			}
			if strings.Contains(err.Error(), c.in) && c.in != "" {
				t.Errorf("Split(%q) error must not quote the input: %v", c.in, err)
			}
			continue
		}
		if strings.Join(words(got), "\x00") != strings.Join(c.want, "\x00") {
			t.Errorf("Split(%q) = %q, want %q", c.in, words(got), c.want)
		}
	}
}

// TestSplitTildeOK pins which words Expand may expand.
func TestSplitTildeOK(t *testing.T) {
	cases := map[string]bool{
		`~`:         true,
		`~/x`:       true,
		`'~/x'`:     false,
		`\~/x`:      false,
		`--dir=~/x`: false,
		`~user`:     false,
		`"~"`:       false,
		`~"/x"`:     true,
		`x~/y`:      false,
	}
	for in, want := range cases {
		got, err := Split(in)
		if err != nil || len(got) != 1 {
			t.Fatalf("Split(%q) = %v, %v", in, got, err)
		}
		if got[0].TildeOK != want {
			t.Errorf("Split(%q).TildeOK = %v, want %v", in, got[0].TildeOK, want)
		}
	}
	// Only the first word of a command decides nothing: every word has its own flag.
	got, _ := Split(`edit ~/x ~ y`)
	if got[0].TildeOK || !got[1].TildeOK || !got[2].TildeOK || got[3].TildeOK {
		t.Errorf("per-word TildeOK = %+v", got)
	}
}

func TestCheckErrorText(t *testing.T) {
	err := &CheckError{Rule: RuleShellSyntax}
	if err.Error() != "custom command: shell-syntax" {
		t.Errorf("Error() = %q", err.Error())
	}
}
