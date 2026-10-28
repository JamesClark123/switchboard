package orchestrator

import (
	"errors"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestCheckValue(t *testing.T) {
	cases := map[string]struct {
		in, want string
		err      error
	}{
		"plain":           {"hello wörld", "hello wörld", nil},
		"newline and tab": {"a\n\tb", "a\n\tb", nil},
		"crlf":            {"a\r\nb\r\n", "a\nb\n", nil},
		"lone cr":         {"a\rb", "a\nb", nil},
		"empty":           {"", "", nil},
		"paste end":       {"x\x1b[201~\r!id\r", "", errControlChar},
		"bell":            {"a\x07", "", errControlChar},
		"del":             {"a\x7f", "", errControlChar},
		"c1 csi":          {"a\u009b2J", "", errControlChar},
		"raw 0x9b":        {"a\x9b2J", "", errInvalidUTF8},
		"nbsp is fine":    {"a b", "a b", nil},
	}
	for name, c := range cases {
		got, err := checkValue(c.in)
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("%s: %q / %v, want %q / %v", name, got, err, c.want, c.err)
		}
	}
}

// Untrusted call values are checked and normalised at call time (R13 §1).
func TestValidateCallValues(t *testing.T) {
	cases := map[string]struct {
		value, want, err string
	}{
		"paste escape":   {"1\x1b[201~\r!id\r", "", "argument number contains a control character"},
		"invalid utf-8":  {"1\x9b", "", "argument number is not valid UTF-8"},
		"crlf stored lf": {"line one\r\nline two", "line one\nline two", ""},
	}
	for name, c := range cases {
		args := map[string]string{"number": c.value, "repo": "r"}
		err := validateCall(reviewPR(), args)
		switch {
		case c.err != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %v, want %q", name, err, c.err)
		case c.err == "" && (err != nil || args["number"] != c.want):
			t.Errorf("%s: %q / %v", name, args["number"], err)
		}
	}
}

// Defaults are substituted values too: checked at save and again at compose.
func TestDefaultValuesChecked(t *testing.T) {
	a := reviewPR()
	a.Arguments[2].DefaultValue = "x\x1b]0;t\x07"
	if err := validateAction(a, nil); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "argument focus default contains a control character") {
		t.Errorf("save = %v", err)
	}
	// A record saved before the check (or a run queued by an older daemon)
	// is refused just before substitution, with nothing composed.
	a.Body = "Review {number} {focus}"
	for name, f := range map[string]func() error{
		"compose":     func() error { _, err := compose(a, map[string]string{"number": "1", "repo": "r"}); return err },
		"composeStep": func() error { _, err := composeStep("Use {focus}", a, nil); return err },
		"supplied":    func() error { _, err := compose(reviewPR(), map[string]string{"focus": "\x1b"}); return err },
	} {
		if err := f(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "contains a control character") {
			t.Errorf("%s = %v", name, err)
		}
	}
	// A CRLF default is substituted as LF.
	b := reviewPR()
	b.Body = "{focus}"
	b.Arguments[2].DefaultValue = "a\r\nb"
	if out, err := compose(b, map[string]string{"number": "1", "repo": "r"}); err != nil || out != "a\nb\n\nnumber: 1\n\nrepo: r" {
		t.Errorf("crlf default = %q / %v", out, err)
	}
}

// Authored text is never filtered: an action body or step carrying ANSI is
// saved unchanged. (Escape-hatch results reach agents through
// agent.Registry.Prompt, outside this package; checkValue is unexported, so
// nothing on that path can call it — covered by design.)
func TestAuthoredTextNotFiltered(t *testing.T) {
	env := newEnv(t)
	o, _ := env.svc.Create("bot")
	a := withSequence([]string{"Say \x1b[1mhi\x1b[0m"}, nil)
	a.Body = "Review {number} in {repo} \x1b[31mred\x1b[0m\r\n"
	saved, err := putAction(env.svc, o.GetId(), a)
	if err != nil || saved.GetBody() != a.GetBody() || saved.GetSequence().GetPrePrompts()[0] != "Say \x1b[1mhi\x1b[0m" {
		t.Fatalf("PutAction = %q / %v", saved.GetBody(), err)
	}
	run, err := env.svc.CallAction(o.GetId(), "review-pr", map[string]string{"number": "1", "repo": "r"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil || !strings.Contains(run.GetPrompt(), "\x1b[31mred") {
		t.Errorf("CallAction = %q / %v", run.GetPrompt(), err)
	}
}
