package orchestrator

import (
	"errors"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func reviewPR() *pb.Action {
	return &pb.Action{
		Name: "review-pr", Type: pb.ActionType_ACTION_TYPE_PROMPT,
		Body: "Review pull request {number} in {repo} and leave review comments.",
		Arguments: []*pb.ActionArgument{
			{Name: "number", Required: true}, {Name: "repo", Required: true}, {Name: "focus"},
		},
	}
}

func TestValidateActionRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*pb.Action)
		want   string
	}{
		"empty body":        {func(a *pb.Action) { a.Body = " " }, "body is empty"},
		"unknown type":      {func(a *pb.Action) { a.Type = pb.ActionType_ACTION_TYPE_UNSPECIFIED }, "unknown action type"},
		"bad name":          {func(a *pb.Action) { a.Name = "Review PR" }, "lowercase letters, digits and hyphens"},
		"empty name":        {func(a *pb.Action) { a.Name = "" }, "action name is required"},
		"long name":         {func(a *pb.Action) { a.Name = strings.Repeat("a", 65) }, "longer than 64"},
		"argument twice":    {func(a *pb.Action) { a.Arguments = append(a.Arguments, &pb.ActionArgument{Name: "focus"}) }, "argument focus declared twice"},
		"bad argument name": {func(a *pb.Action) { a.Arguments[0].Name = "1st" }, `argument name "1st"`},
		"empty argument":    {func(a *pb.Action) { a.Arguments[0].Name = "" }, "an argument needs a name"},
		"undeclared placeholder": {func(a *pb.Action) {
			a.Body = "Look at {nope}"
		}, "placeholder {nope} names no argument"},
		"tag on reason": {func(a *pb.Action) {
			a.Type = pb.ActionType_ACTION_TYPE_REASON
			a.WorkerTag = "ci"
		}, "reason actions take no worker requirement"},
	}
	for name, c := range cases {
		a := reviewPR()
		c.mutate(a)
		err := validateAction(a, nil)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want ErrInvalid containing %q", name, err, c.want)
		}
	}
	if err := validateAction(reviewPR(), nil); err != nil {
		t.Errorf("valid action refused: %v", err)
	}
	// `{{` is a literal, not a placeholder; a reason action without a tag is fine.
	a := reviewPR()
	a.Body = "literal {{ brace and {number}"
	if err := validateAction(a, nil); err != nil {
		t.Errorf("{{ should be a literal: %v", err)
	}
	if got := placeholders("{a} {{ {b} {a} {{x}} {1bad}"); strings.Join(got, ",") != "a,b" {
		t.Errorf("placeholders = %v", got)
	}
}

func TestComposeGoldenCases(t *testing.T) {
	a := reviewPR()
	out, err := compose(a, map[string]string{"number": "412", "repo": "acme/web", "focus": "tests"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Review pull request 412 in acme/web and leave review comments.\n\nfocus: tests" {
		t.Errorf("composed = %q", out)
	}
	// Unsupplied optional without a default: nothing appended, placeholder empty.
	a2 := reviewPR()
	a2.Body = "Review {number} ({focus})"
	out, _ = compose(a2, map[string]string{"number": "1", "repo": "r"})
	if out != "Review 1 ()\n\nrepo: r" {
		t.Errorf("unsupplied optional = %q", out)
	}
	// Default fills a placeholder; a supplied empty value counts as unsupplied.
	a2.Arguments[2].DefaultValue = "everything"
	out, _ = compose(a2, map[string]string{"number": "1", "repo": "r", "focus": ""})
	if !strings.HasPrefix(out, "Review 1 (everything)") {
		t.Errorf("default = %q", out)
	}
	// Multi-line appended value and the `{{` literal (only `{{` is special, R8);
	// every occurrence is replaced.
	a3 := &pb.Action{Name: "x", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "{{json} {n} and {n}", Arguments: []*pb.ActionArgument{{Name: "n"}, {Name: "notes"}}}
	out, _ = compose(a3, map[string]string{"n": "7", "notes": "line one\nline two"})
	if out != "{json} 7 and 7\n\nnotes:\nline one\nline two" {
		t.Errorf("multi-line = %q", out)
	}
	// Oversized composed prompt.
	big := &pb.Action{Name: "b", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "{v}", Arguments: []*pb.ActionArgument{{Name: "v"}}}
	if _, err := compose(big, map[string]string{"v": strings.Repeat("x", maxPromptBytes+1)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized prompt = %v", err)
	}
}

func TestValidateCall(t *testing.T) {
	a := reviewPR()
	cases := map[string]struct {
		args map[string]string
		want string
	}{
		"missing required": {map[string]string{"number": "1"}, "argument repo is required"},
		"empty required":   {map[string]string{"number": "1", "repo": ""}, "argument repo is required"},
		"undeclared":       {map[string]string{"number": "1", "repo": "r", "extra": "x"}, "argument extra is not declared"},
		"oversized":        {map[string]string{"number": "1", "repo": strings.Repeat("r", maxArgumentBytes+1)}, "larger than"},
	}
	for name, c := range cases {
		if err := validateCall(a, c.args); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := validateCall(a, map[string]string{"number": "1", "repo": "r"}); err != nil {
		t.Errorf("valid call refused: %v", err)
	}
	// A required argument with a default needs no value.
	a.Arguments[1].DefaultValue = "acme/web"
	if err := validateCall(a, map[string]string{"number": "1"}); err != nil {
		t.Errorf("defaulted required = %v", err)
	}
}

func TestPutAndDeleteAction(t *testing.T) {
	env := newEnv(t)
	o, _ := env.svc.Create("bot")
	a, err := env.svc.PutAction(o.GetId(), reviewPR())
	if err != nil || a.GetCreatedAt() == nil || a.GetUpdatedAt() == nil {
		t.Fatalf("PutAction = %+v / %v", a, err)
	}
	if _, err := env.svc.PutAction(o.GetId(), &pb.Action{Name: "bad"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid action = %v", err)
	}
	// Replace keeps created_at, bumps updated_at.
	env.clock.Advance(1_000_000_000)
	a2 := reviewPR()
	a2.Description = "v2"
	saved, _ := env.svc.PutAction(o.GetId(), a2)
	rec, _ := env.reg.GetOrchestrator(o.GetId())
	if len(rec.GetActions()) != 1 || rec.GetActions()[0].GetDescription() != "v2" || !saved.GetCreatedAt().AsTime().Equal(a.GetCreatedAt().AsTime()) || saved.GetUpdatedAt().AsTime().Equal(a.GetUpdatedAt().AsTime()) {
		t.Errorf("replace: %+v", rec.GetActions())
	}
	// Delete disables the timed events naming it and returns their names.
	rt, _ := env.svc.runtimeFor(o.GetId())
	if _, err := rt.update(func(x *pb.Orchestrator) error {
		x.TimedEvents = []*pb.TimedEvent{{Id: "tev-1", Name: "poll", Action: "review-pr", Enabled: true}, {Id: "tev-2", Name: "other", Action: "zzz", Enabled: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	disabled, err := env.svc.DeleteAction(o.GetId(), "review-pr")
	if err != nil || len(disabled) != 1 || disabled[0] != "poll" {
		t.Fatalf("DeleteAction = %v / %v", disabled, err)
	}
	rec, _ = env.reg.GetOrchestrator(o.GetId())
	if len(rec.GetActions()) != 0 || rec.GetTimedEvents()[0].GetEnabled() || rec.GetTimedEvents()[0].GetDisabledReason() != "action review-pr was deleted" || !rec.GetTimedEvents()[1].GetEnabled() {
		t.Errorf("after delete: %+v", rec)
	}
	if _, err := env.svc.DeleteAction(o.GetId(), "review-pr"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
	if _, err := env.svc.PutAction("nope", reviewPR()); !errors.Is(err, ErrNotFound) {
		t.Errorf("put on unknown = %v", err)
	}
	if _, err := env.svc.DeleteAction("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete on unknown = %v", err)
	}
}
