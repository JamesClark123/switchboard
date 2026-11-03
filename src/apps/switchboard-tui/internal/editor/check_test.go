package editor

import (
	"reflect"
	"testing"
)

func mustSplit(t *testing.T, s string) []Word {
	t.Helper()
	ws, err := Split(s)
	if err != nil {
		t.Fatalf("Split(%q): %v", s, err)
	}
	return ws
}

// TestCheck covers every FR-506 bullet that runs on the split words.
func TestCheck(t *testing.T) {
	cases := []struct {
		in       string
		wantRule string
	}{
		{``, RuleEmpty},
		{`   `, RuleEmpty},
		{`"" {path}`, RuleEmptyExecutable},
		{`'' x`, RuleEmptyExecutable},
		{`{path}/bin/edit`, RuleMarkerInExecutable},
		{`{host} x`, RuleMarkerInExecutable},
		{`edit{path}`, RuleMarkerInExecutable},
		{`FOO=1 x`, RuleAssignment},
		{`"FOO=1" x`, RuleAssignment},
		{`'FOO=1' x`, RuleAssignment},
		{`_x=1 edit`, RuleAssignment},
		{`API_TOKEN=abc123 myeditor {path}`, RuleAssignment},
		{`./FOO=1/edit`, ""},
		{`1FOO=1 x`, ""}, // not a variable name
		{`edit FOO=1`, ""},
		{`edit`, ""},
		{`kitty -e sh -c 'cd "$1" && nvim .' sh {path}`, ""},
	}
	for _, c := range cases {
		err := Check(mustSplit(t, c.in))
		if r := rule(err); r != c.wantRule {
			t.Errorf("Check(%q) rule = %q, want %q", c.in, r, c.wantRule)
		}
	}
}

func TestExpand(t *testing.T) {
	ws := mustSplit(t, `~/bin/edit ~ '~/x' --dir=~/y`)
	got, err := Expand(ws, "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/me/bin/edit", "/home/me", "~/x", "--dir=~/y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %q, want %q", got, want)
	}
	// No home and a word that needs it: refused. No home and no such word: fine.
	if _, err := Expand(ws, ""); rule(err) != RuleNoHome {
		t.Errorf("Expand with no home rule = %q", rule(err))
	}
	if got, err := Expand(mustSplit(t, `edit '~/x'`), ""); err != nil || got[1] != "~/x" {
		t.Errorf("Expand without a tilde word = %q, %v", got, err)
	}
}

func TestSubstitute(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		path     string
		host     string
		want     []string
		wantPath bool
	}{
		{"marker replaced inside a word", []string{"edit", "--open", "{path}", "extra"}, "/ws/a b", "", []string{"edit", "--open", "/ws/a b", "extra"}, true},
		{"path appended when absent", []string{"edit"}, "/ws", "", []string{"edit", "/ws"}, false},
		{"bare host dropped locally", []string{"edit", "{host}", "{path}"}, "/ws", "", []string{"edit", "/ws"}, true},
		{"bare host kept remotely", []string{"edit", "{host}", "{path}"}, "/ws", "user@box", []string{"edit", "user@box", "/ws"}, true},
		{"embedded host empty locally", []string{"edit", "--remote", "ssh-remote+{host}", "{path}"}, "/ws", "", []string{"edit", "--remote", "ssh-remote+", "/ws"}, true},
		{"both markers in one word", []string{"edit", "{host}:{path}"}, "/ws", "u@b", []string{"edit", "u@b:/ws"}, true},
		{"a marker inside a substituted value is not re-expanded", []string{"edit", "{path}"}, "/ws/{host}/x", "u@b", []string{"edit", "/ws/{host}/x"}, true},
		{"a path holding {path} is not re-expanded", []string{"edit", "{path}"}, "/ws/{path}", "", []string{"edit", "/ws/{path}"}, true},
		{"positional sh -c form", []string{"kitty", "-e", "sh", "-c", `cd "$1" && nvim .`, "sh", "{path}"}, "/tmp/x$(touch pwned)", "", []string{"kitty", "-e", "sh", "-c", `cd "$1" && nvim .`, "sh", "/tmp/x$(touch pwned)"}, true},
		{"unknown braces stay", []string{"edit", "{other}"}, "/ws", "", []string{"edit", "{other}", "/ws"}, false},
	}
	for _, c := range cases {
		got, hadPath := Substitute(c.args, c.path, c.host)
		if !reflect.DeepEqual(got, c.want) || hadPath != c.wantPath {
			t.Errorf("%s: Substitute = %q, %v; want %q, %v", c.name, got, hadPath, c.want, c.wantPath)
		}
	}
}

// TestSC005PathNeverSplits: a path containing spaces, quotes or shell syntax is
// one argument, unchanged, and never produces a new argument (SC-005).
func TestSC005PathNeverSplits(t *testing.T) {
	args := []string{"kitty", "-e", "sh", "-c", `cd "$1" && nvim .`, "sh", "{path}"}
	for _, path := range []string{"/tmp/x$(touch pwned)", "/tmp/a b", `/tmp/a"b'c`, "/tmp/a;b|c&d", "/tmp/{host}"} {
		argv, _ := Substitute(args, path, "")
		if len(argv) != len(args) || argv[len(argv)-1] != path {
			t.Errorf("path %q: argv = %q", path, argv)
		}
		for i := range args[:len(args)-1] {
			if argv[i] != args[i] {
				t.Errorf("path %q changed arg %d: %q", path, i, argv[i])
			}
		}
	}
	if !HasHostMarker([]string{"a", "x{host}y"}) || HasHostMarker([]string{"a", "{path}"}) {
		t.Error("HasHostMarker wrong")
	}
}
