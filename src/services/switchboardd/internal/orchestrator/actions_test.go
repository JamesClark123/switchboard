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
	a, err := putAction(env.svc, o.GetId(), reviewPR())
	if err != nil || a.GetCreatedAt() == nil || a.GetUpdatedAt() == nil {
		t.Fatalf("PutAction = %+v / %v", a, err)
	}
	if _, err := putAction(env.svc, o.GetId(), &pb.Action{Name: "bad"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid action = %v", err)
	}
	// Replace keeps created_at, bumps updated_at.
	env.clock.Advance(1_000_000_000)
	a2 := reviewPR()
	a2.Description = "v2"
	saved, _ := putAction(env.svc, o.GetId(), a2)
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
	if _, err := putAction(env.svc, "nope", reviewPR()); !errors.Is(err, ErrNotFound) {
		t.Errorf("put on unknown = %v", err)
	}
	if _, err := env.svc.DeleteAction("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete on unknown = %v", err)
	}
}

// plain gives an action an explicit sequence with nothing enabled: no steps,
// so its runs are delivered by DeliverWorkerPrompt as before (FR-208). The
// tests of the prompt delivery use it.
func plain(a *pb.Action) *pb.Action {
	a.Sequence = &pb.ActionSequence{Model: pb.ModelChoice_MODEL_CHOICE_OPUS, Effort: pb.EffortChoice_EFFORT_CHOICE_MEDIUM}
	return a
}

// withSequence returns review-pr with an explicit sequence (all built-ins on)
// and the given custom prompts; ticket and skill are declared too.
func withSequence(pre, post []string) *pb.Action {
	a := reviewPR()
	a.Arguments = append(a.Arguments, &pb.ActionArgument{Name: "ticket"}, &pb.ActionArgument{Name: "skill", DefaultValue: "review"})
	a.Sequence = &pb.ActionSequence{
		ModelEnabled: true, Model: pb.ModelChoice_MODEL_CHOICE_OPUS,
		EffortEnabled: true, Effort: pb.EffortChoice_EFFORT_CHOICE_MEDIUM,
		ClearEnabled: true, PrePrompts: pre, PostPrompts: post,
	}
	return a
}

func TestValidateSequence(t *testing.T) {
	eleven := make([]string, maxStepPrompts+1)
	for i := range eleven {
		eleven[i] = "step"
	}
	cases := map[string]struct {
		mutate func(*pb.Action)
		want   string // "" = accepted
	}{
		"legacy action":           {func(a *pb.Action) { a.Sequence = nil }, ""},
		"plain words":             {func(a *pb.Action) { a.Sequence.PrePrompts = []string{"Look at {ticket} with {skill}"} }, ""},
		"slash command":           {func(a *pb.Action) { a.Sequence.PrePrompts = []string{"/review {ticket}"} }, ""},
		"shell escape, no values": {func(a *pb.Action) { a.Sequence.PostPrompts = []string{"!git status"} }, ""},
		"literal brace first":     {func(a *pb.Action) { a.Sequence.PrePrompts = []string{"{{ticket} is {ticket}"} }, ""},
		"exactly ten":             {func(a *pb.Action) { a.Sequence.PrePrompts = eleven[:maxStepPrompts] }, ""},
		"first-word placeholder":  {func(a *pb.Action) { a.Sequence.PrePrompts = []string{"{skill} for {ticket}"} }, "pre-prompt 1: placeholder {skill} is in the first word"},
		"placeholder alone":       {func(a *pb.Action) { a.Sequence.PrePrompts = []string{"ok", "{ticket}"} }, "pre-prompt 2: placeholder {ticket} is in the first word"},
		"slash placeholder":       {func(a *pb.Action) { a.Sequence.PostPrompts = []string{"/{skill}"} }, "post-prompt 1: placeholder {skill} is in the first word"},
		"shell with placeholder":  {func(a *pb.Action) { a.Sequence.PostPrompts = []string{"!git checkout {ticket}"} }, "post-prompt 1: a prompt starting with ! may not contain a placeholder ({ticket})"},
		"indented shell":          {func(a *pb.Action) { a.Sequence.PrePrompts = []string{"  !echo {ticket}"} }, "pre-prompt 1: a prompt starting with !"},
		"newline before command":  {func(a *pb.Action) { a.Sequence.PrePrompts = []string{"\n/foo{skill}"} }, "placeholder {skill} is in the first word"},
		"eleven entries":          {func(a *pb.Action) { a.Sequence.PostPrompts = eleven }, "more than 10 post-prompts"},
		"empty":                   {func(a *pb.Action) { a.Sequence.PrePrompts = []string{""} }, "pre-prompt 1 is empty"},
		"whitespace only":         {func(a *pb.Action) { a.Sequence.PostPrompts = []string{"ok", " \n\t"} }, "post-prompt 2 is empty"},
		"oversized":               {func(a *pb.Action) { a.Sequence.PrePrompts = []string{strings.Repeat("x", maxArgumentBytes+1)} }, "pre-prompt 1 is larger than"},
		"undeclared":              {func(a *pb.Action) { a.Sequence.PrePrompts = []string{"Use {nope}"} }, "pre-prompt 1: placeholder {nope} names no argument"},
		"model unspecified":       {func(a *pb.Action) { a.Sequence.Model = pb.ModelChoice_MODEL_CHOICE_UNSPECIFIED }, "model must be one of"},
		"model unknown":           {func(a *pb.Action) { a.Sequence.Model = 99 }, "model must be one of"},
		"model off, unspecified":  {func(a *pb.Action) { a.Sequence.ModelEnabled, a.Sequence.Model = false, 0 }, "model must be one of"},
		"effort unspecified":      {func(a *pb.Action) { a.Sequence.Effort = pb.EffortChoice_EFFORT_CHOICE_UNSPECIFIED }, "effort must be one of"},
		"effort unknown":          {func(a *pb.Action) { a.Sequence.Effort = 99 }, "effort must be one of"},
	}
	for name, c := range cases {
		a := withSequence(nil, nil)
		c.mutate(a)
		err := validateAction(a, nil)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case c.want != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want ErrInvalid containing %q", name, err, c.want)
		}
	}
}

func TestNormalizeActionTrimsStepPrompts(t *testing.T) {
	a := normalizeAction(withSequence([]string{"  /review now \n"}, []string{"\tdone "}))
	if seq := a.GetSequence(); seq.GetPrePrompts()[0] != "/review now" || seq.GetPostPrompts()[0] != "done" {
		t.Errorf("sequence = %+v", seq)
	}
	if normalizeAction(reviewPR()).GetSequence() != nil {
		t.Error("a legacy action must stay without a sequence")
	}
}

func TestEffectiveSequence(t *testing.T) {
	legacy := effectiveSequence(reviewPR())
	if !legacy.GetModelEnabled() || legacy.GetModel() != pb.ModelChoice_MODEL_CHOICE_OPUS ||
		!legacy.GetEffortEnabled() || legacy.GetEffort() != pb.EffortChoice_EFFORT_CHOICE_MEDIUM ||
		!legacy.GetClearEnabled() || len(legacy.GetPrePrompts())+len(legacy.GetPostPrompts()) != 0 {
		t.Errorf("legacy = %+v", legacy)
	}
	a := withSequence([]string{"x"}, nil)
	if effectiveSequence(a) != a.GetSequence() {
		t.Error("an explicit sequence is read as stored")
	}
}

// stepsOf renders steps as role/kind/text/expect lines.
func stepsOf(steps []*pb.RunStep) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.GetRole().String()+" "+s.GetKind().String()+" "+s.GetText()+"|"+s.GetExpect())
	}
	return out
}

func TestBuildSteps(t *testing.T) {
	args := map[string]string{"number": "7", "repo": "r", "ticket": "SWI-1"}
	cases := map[string]struct {
		mutate func(*pb.ActionSequence)
		want   []string
	}{
		"all on": {nil, []string{
			"STEP_ROLE_SETUP STEP_KIND_MODEL /model opus|Opus",
			"STEP_ROLE_SETUP STEP_KIND_EFFORT /effort medium|medium",
			"STEP_ROLE_SETUP STEP_KIND_CUSTOM /review SWI-1|",
			"STEP_ROLE_SETUP STEP_KIND_CUSTOM Use review twice: review|",
			"STEP_ROLE_ACTION STEP_KIND_ACTION_PROMPT PROMPT|",
			"STEP_ROLE_CLEANUP STEP_KIND_CUSTOM Summarise SWI-1|",
			"STEP_ROLE_CLEANUP STEP_KIND_CLEAR /clear|",
		}},
		"all off": {func(s *pb.ActionSequence) {
			s.ModelEnabled, s.EffortEnabled, s.ClearEnabled, s.PrePrompts, s.PostPrompts = false, false, false, nil, nil
		}, nil},
		"only clear": {func(s *pb.ActionSequence) {
			s.ModelEnabled, s.EffortEnabled, s.PrePrompts, s.PostPrompts = false, false, nil, nil
		}, []string{"STEP_ROLE_ACTION STEP_KIND_ACTION_PROMPT PROMPT|", "STEP_ROLE_CLEANUP STEP_KIND_CLEAR /clear|"}},
		"only a post-prompt": {func(s *pb.ActionSequence) {
			s.ModelEnabled, s.EffortEnabled, s.ClearEnabled, s.PrePrompts = false, false, false, nil
		}, []string{"STEP_ROLE_ACTION STEP_KIND_ACTION_PROMPT PROMPT|", "STEP_ROLE_CLEANUP STEP_KIND_CUSTOM Summarise SWI-1|"}},
		"fable, auto": {func(s *pb.ActionSequence) {
			s.Model, s.Effort, s.ClearEnabled, s.PrePrompts, s.PostPrompts = pb.ModelChoice_MODEL_CHOICE_FABLE, pb.EffortChoice_EFFORT_CHOICE_AUTO, false, nil, nil
		}, []string{
			"STEP_ROLE_SETUP STEP_KIND_MODEL /model fable|Fable",
			"STEP_ROLE_SETUP STEP_KIND_EFFORT /effort auto|auto",
			"STEP_ROLE_ACTION STEP_KIND_ACTION_PROMPT PROMPT|",
		}},
	}
	for name, c := range cases {
		a := withSequence([]string{"/review {ticket}", "Use {skill} twice: {skill}"}, []string{"Summarise {ticket}"})
		if c.mutate != nil {
			c.mutate(a.Sequence)
		}
		steps, err := buildSteps(a, args, "PROMPT")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := stepsOf(steps); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
	// A legacy action gets the default built-ins around its prompt (FR-210).
	steps, _ := buildSteps(reviewPR(), args, "P")
	if len(steps) != 4 || steps[0].GetExpect() != "Opus" || steps[1].GetExpect() != "medium" || steps[3].GetKind() != pb.StepKind_STEP_KIND_CLEAR {
		t.Errorf("legacy steps = %q", stepsOf(steps))
	}
}

func TestBuildStepsComposeFailure(t *testing.T) {
	// A value that bypassed validateCall (a run queued by an older daemon) fails
	// the snapshot with the entry named.
	bad := map[string]string{"number": "1", "repo": "r", "ticket": "\x1b[201~"}
	for name, a := range map[string]*pb.Action{
		"pre-prompt 1":  withSequence([]string{"Look at {ticket}"}, nil),
		"post-prompt 1": withSequence(nil, []string{"Look at {ticket}"}),
	} {
		if _, err := buildSteps(a, bad, "P"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), name+": ") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestComposeStep(t *testing.T) {
	a := withSequence(nil, nil)
	cases := map[string]struct {
		text string
		args map[string]string
		want string
	}{
		"value, every occurrence": {"Use {ticket} and {ticket}", map[string]string{"ticket": "T-1"}, "Use T-1 and T-1"},
		"default":                 {"Run {skill}", nil, "Run review"},
		"empty value -> default":  {"Run {skill}", map[string]string{"skill": ""}, "Run review"},
		"missing optional":        {"Look at {ticket}.", nil, "Look at ."},
		"literal brace":           {"Keep {{ticket} {ticket}", map[string]string{"ticket": "x"}, "Keep {ticket} x"},
		"nothing appended":        {"Plain step", map[string]string{"number": "1", "repo": "r", "focus": "tests"}, "Plain step"},
	}
	for name, c := range cases {
		got, err := composeStep(c.text, a, c.args)
		if err != nil || got != c.want {
			t.Errorf("%s: %q / %v, want %q", name, got, err, c.want)
		}
	}
	if _, err := composeStep("Use {ticket}", a, map[string]string{"ticket": strings.Repeat("x", maxPromptBytes+1)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized step = %v", err)
	}
}

// An argument referenced only by a pre-prompt is still appended to the action
// prompt: appending depends on the body alone (FR-207).
func TestArgumentOnlyInPrePromptIsAppended(t *testing.T) {
	a := withSequence([]string{"Open {ticket}"}, nil)
	args := map[string]string{"number": "1", "repo": "r", "ticket": "SWI-9"}
	prompt, err := compose(a, args)
	if err != nil || !strings.HasSuffix(prompt, "\n\nticket: SWI-9") {
		t.Fatalf("prompt = %q / %v", prompt, err)
	}
	steps, _ := buildSteps(a, args, prompt)
	if steps[2].GetText() != "Open SWI-9" || steps[3].GetText() != prompt {
		t.Errorf("steps = %q", stepsOf(steps))
	}
}
