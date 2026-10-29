package orchestrator

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	swb "github.com/jamesclark123/switchboard/libs/switchboard-proto"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Actions (research R8, data-model "Action"): validated at save, composed at
// call time. Placeholders are {name}; `{{` is a literal `{`.

var (
	actionNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	argNameRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	placeholder  = regexp.MustCompile(`\{\{|\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// placeholders lists the distinct placeholder names in body, in order of first
// appearance; `{{` is skipped.
func placeholders(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(body, -1) {
		if m[0] == "{{" || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// validateAction checks an action at save (FR-121/FR-137/FR-138/FR-139) against
// the orchestrator's existing actions (same-name replace allowed).
func validateAction(a *pb.Action, existing []*pb.Action) error {
	name := strings.TrimSpace(a.GetName())
	switch {
	case name == "":
		return fmt.Errorf("%w: action name is required", ErrInvalid)
	case len(name) > 64:
		return fmt.Errorf("%w: action name %q is longer than 64 characters", ErrInvalid, name)
	case !actionNameRe.MatchString(name):
		return fmt.Errorf("%w: action name %q must be lowercase letters, digits and hyphens, starting with a letter or digit", ErrInvalid, name)
	}
	switch a.GetType() {
	case pb.ActionType_ACTION_TYPE_PROMPT, pb.ActionType_ACTION_TYPE_REASON:
	default:
		return fmt.Errorf("%w: unknown action type", ErrInvalid)
	}
	if strings.TrimSpace(a.GetBody()) == "" {
		return fmt.Errorf("%w: body is empty", ErrInvalid)
	}
	declared := map[string]bool{}
	for _, arg := range a.GetArguments() {
		n := strings.TrimSpace(arg.GetName())
		switch {
		case n == "":
			return fmt.Errorf("%w: an argument needs a name", ErrInvalid)
		case !argNameRe.MatchString(n):
			return fmt.Errorf("%w: argument name %q must be letters, digits and underscores, not starting with a digit", ErrInvalid, n)
		case declared[n]:
			return fmt.Errorf("%w: argument %s declared twice", ErrInvalid, n)
		}
		declared[n] = true
		if _, err := checkValue(arg.GetDefaultValue()); err != nil {
			return fmt.Errorf("%w: argument %s default %v", ErrInvalid, n, err)
		}
	}
	for _, p := range placeholders(a.GetBody()) {
		if !declared[p] {
			return fmt.Errorf("%w: placeholder {%s} names no argument", ErrInvalid, p)
		}
	}
	if err := validateSequence(a.GetSequence(), declared); err != nil {
		return err
	}
	_ = existing // uniqueness is replace-by-name; nothing else to check here
	return nil
}

// normalizeAction trims the fields validateAction accepted.
func normalizeAction(a *pb.Action) *pb.Action {
	out := proto.Clone(a).(*pb.Action)
	out.Name = strings.TrimSpace(out.GetName())
	for _, arg := range out.GetArguments() {
		arg.Name = strings.TrimSpace(arg.GetName())
	}
	if seq := out.GetSequence(); seq != nil {
		for i, t := range seq.GetPrePrompts() {
			seq.PrePrompts[i] = strings.TrimSpace(t)
		}
		for i, t := range seq.GetPostPrompts() {
			seq.PostPrompts[i] = strings.TrimSpace(t)
		}
	}
	return out
}

// validateSequence checks an action's sequence (FR-204, R9); a missing one is
// the defaults (FR-210) and always valid. Each refusal names the entry.
func validateSequence(seq *pb.ActionSequence, declared map[string]bool) error {
	if seq == nil {
		return nil
	}
	if seq.GetModel() == pb.ModelChoice_MODEL_CHOICE_UNSPECIFIED || swb.ModelChoiceLabel(seq.GetModel()) == "unknown" {
		return fmt.Errorf("%w: model must be one of Opus, Fable, Sonnet, Haiku", ErrInvalid)
	}
	if seq.GetEffort() == pb.EffortChoice_EFFORT_CHOICE_UNSPECIFIED || swb.EffortChoiceLabel(seq.GetEffort()) == "unknown" {
		return fmt.Errorf("%w: effort must be one of low, medium, high, xhigh, max, auto", ErrInvalid)
	}
	if err := validateStepPrompts("pre-prompt", seq.GetPrePrompts(), declared); err != nil {
		return err
	}
	return validateStepPrompts("post-prompt", seq.GetPostPrompts(), declared)
}

// validateStepPrompts checks one custom list (FR-204): at most maxStepPrompts
// entries, each non-empty once trimmed, within maxArgumentBytes, naming only
// declared arguments, with no placeholder in its first word and none at all
// when it starts with `!` — the author, not a call's value, decides what kind
// of input a step is and which command it runs.
func validateStepPrompts(what string, prompts []string, declared map[string]bool) error {
	if len(prompts) > maxStepPrompts {
		return fmt.Errorf("%w: more than %d %ss", ErrInvalid, maxStepPrompts, what)
	}
	for i, raw := range prompts {
		t := strings.TrimSpace(raw)
		switch {
		case t == "":
			return fmt.Errorf("%w: %s %d is empty", ErrInvalid, what, i+1)
		case len(t) > maxArgumentBytes:
			return fmt.Errorf("%w: %s %d is larger than %d bytes", ErrInvalid, what, i+1, maxArgumentBytes)
		}
		names := placeholders(t)
		for _, p := range names {
			if !declared[p] {
				return fmt.Errorf("%w: %s %d: placeholder {%s} names no argument", ErrInvalid, what, i+1, p)
			}
		}
		if first := placeholders(strings.Fields(t)[0]); len(first) > 0 {
			return fmt.Errorf("%w: %s %d: placeholder {%s} is in the first word; start with your own text", ErrInvalid, what, i+1, first[0])
		}
		if strings.HasPrefix(t, "!") && len(names) > 0 {
			return fmt.Errorf("%w: %s %d: a prompt starting with ! may not contain a placeholder ({%s})", ErrInvalid, what, i+1, names[0])
		}
	}
	return nil
}

// effectiveSequence is the only reader of Action.sequence (R9): a missing one
// (every action saved before SWI-17) reads as the defaults — Opus, medium,
// Clear and empty custom lists (FR-210). The result must not be modified.
func effectiveSequence(a *pb.Action) *pb.ActionSequence {
	if seq := a.GetSequence(); seq != nil {
		return seq
	}
	return &pb.ActionSequence{
		ModelEnabled: true, Model: pb.ModelChoice_MODEL_CHOICE_OPUS,
		EffortEnabled: true, Effort: pb.EffortChoice_EFFORT_CHOICE_MEDIUM,
		ClearEnabled: true,
	}
}

// buildSteps snapshots a run's sequence at call time (FR-205, FR-207): Model?,
// Effort?, the custom pre-prompts, the action prompt (prompt, already composed;
// the frame is added at delivery), the custom post-prompts, Clear?. An action
// with no enabled built-in and no custom prompt gets no steps (FR-208).
func buildSteps(a *pb.Action, args map[string]string, prompt string) ([]*pb.RunStep, error) {
	seq := effectiveSequence(a)
	if !seq.GetModelEnabled() && !seq.GetEffortEnabled() && !seq.GetClearEnabled() &&
		len(seq.GetPrePrompts()) == 0 && len(seq.GetPostPrompts()) == 0 {
		return nil, nil
	}
	var steps []*pb.RunStep
	if seq.GetModelEnabled() {
		m := swb.ModelChoiceLabel(seq.GetModel())
		steps = append(steps, &pb.RunStep{Role: pb.StepRole_STEP_ROLE_SETUP, Kind: pb.StepKind_STEP_KIND_MODEL,
			Text: "/model " + strings.ToLower(m), Expect: m})
	}
	if seq.GetEffortEnabled() {
		e := swb.EffortChoiceLabel(seq.GetEffort())
		steps = append(steps, &pb.RunStep{Role: pb.StepRole_STEP_ROLE_SETUP, Kind: pb.StepKind_STEP_KIND_EFFORT,
			Text: "/effort " + e, Expect: e})
	}
	custom := func(role pb.StepRole, what string, prompts []string) error {
		for i, t := range prompts {
			text, err := composeStep(t, a, args)
			if err != nil {
				return fmt.Errorf("%s %d: %w", what, i+1, err)
			}
			steps = append(steps, &pb.RunStep{Role: role, Kind: pb.StepKind_STEP_KIND_CUSTOM, Text: text})
		}
		return nil
	}
	if err := custom(pb.StepRole_STEP_ROLE_SETUP, "pre-prompt", seq.GetPrePrompts()); err != nil {
		return nil, err
	}
	steps = append(steps, &pb.RunStep{Role: pb.StepRole_STEP_ROLE_ACTION, Kind: pb.StepKind_STEP_KIND_ACTION_PROMPT, Text: prompt})
	if err := custom(pb.StepRole_STEP_ROLE_CLEANUP, "post-prompt", seq.GetPostPrompts()); err != nil {
		return nil, err
	}
	if seq.GetClearEnabled() {
		steps = append(steps, &pb.RunStep{Role: pb.StepRole_STEP_ROLE_CLEANUP, Kind: pb.StepKind_STEP_KIND_CLEAR, Text: "/clear"})
	}
	return steps, nil
}

// validateCall checks a call's arguments against the action (FR-140): every
// required argument present (a default counts), no undeclared name, no value
// over maxArgumentBytes, and no control character (checkValue, R13 §1). Each
// value in args is replaced by its normalised form (CRLF/CR → LF).
func validateCall(a *pb.Action, args map[string]string) error {
	declared := map[string]*pb.ActionArgument{}
	for _, arg := range a.GetArguments() {
		declared[arg.GetName()] = arg
	}
	names := make([]string, 0, len(args))
	for n := range args {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := declared[n]; !ok {
			return fmt.Errorf("%w: argument %s is not declared by action %s", ErrInvalid, n, a.GetName())
		}
		v, err := checkValue(args[n])
		if err != nil {
			return fmt.Errorf("%w: argument %s %v", ErrInvalid, n, err)
		}
		if len(v) > maxArgumentBytes {
			return fmt.Errorf("%w: argument %s is larger than %d bytes", ErrInvalid, n, maxArgumentBytes)
		}
		args[n] = v
	}
	for _, arg := range a.GetArguments() {
		if !arg.GetRequired() {
			continue
		}
		if v, ok := args[arg.GetName()]; (!ok || v == "") && arg.GetDefaultValue() == "" {
			return fmt.Errorf("%w: argument %s is required by action %s", ErrInvalid, arg.GetName(), a.GetName())
		}
	}
	return nil
}

// callValues returns each declared argument's substituted value: the call's,
// else its default (an optional argument with neither is empty), each checked
// again by checkValue just before substitution (R13 §1: defence in depth for a
// run queued by an older daemon), plus the supplied values, normalised.
func callValues(a *pb.Action, args map[string]string) (values, supplied map[string]string, err error) {
	values, supplied = map[string]string{}, map[string]string{}
	for _, arg := range a.GetArguments() {
		n := arg.GetName()
		v, ok := args[n]
		if ok {
			if supplied[n], err = checkValue(v); err != nil {
				return nil, nil, fmt.Errorf("%w: argument %s %v", ErrInvalid, n, err)
			}
		}
		if !ok || v == "" {
			v = arg.GetDefaultValue()
		}
		if values[n], err = checkValue(v); err != nil {
			return nil, nil, fmt.Errorf("%w: argument %s %v", ErrInvalid, n, err)
		}
	}
	return values, supplied, nil
}

// fill replaces every placeholder occurrence in text by its value; `{{`
// becomes `{`.
func fill(text string, values map[string]string) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		if m == "{{" {
			return "{"
		}
		return values[m[1:len(m)-1]]
	})
}

// compose builds the prompt (FR-139, R8): every placeholder occurrence is
// replaced by the argument's value (an optional argument with neither value
// nor default replaces with nothing); each SUPPLIED argument with no
// placeholder in the body is appended, in declaration order, as
// "\n\n<name>: <value>" — a multi-line value on the lines after "<name>:" —
// even when a pre/post prompt references it (FR-207). `{{` becomes `{`.
func compose(a *pb.Action, args map[string]string) (string, error) {
	values, supplied, err := callValues(a, args)
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, p := range placeholders(a.GetBody()) {
		used[p] = true
	}
	out := fill(a.GetBody(), values)
	for _, arg := range a.GetArguments() {
		n := arg.GetName()
		v, ok := supplied[n]
		if used[n] || !ok {
			continue
		}
		if strings.Contains(v, "\n") {
			out += "\n\n" + n + ":\n" + v
		} else {
			out += "\n\n" + n + ": " + v
		}
	}
	if len(out) > maxPromptBytes {
		return "", fmt.Errorf("%w: the composed prompt is larger than %d bytes", ErrInvalid, maxPromptBytes)
	}
	return out, nil
}

// composeStep fills a custom pre/post prompt as compose fills the body, with
// nothing appended (FR-207).
func composeStep(text string, a *pb.Action, args map[string]string) (string, error) {
	values, _, err := callValues(a, args)
	if err != nil {
		return "", err
	}
	out := fill(text, values)
	if len(out) > maxPromptBytes {
		return "", fmt.Errorf("%w: the composed prompt is larger than %d bytes", ErrInvalid, maxPromptBytes)
	}
	return out, nil
}

// findAction returns the orchestrator's action by name.
func findAction(o *pb.Orchestrator, name string) *pb.Action {
	for _, a := range o.GetActions() {
		if a.GetName() == name {
			return a
		}
	}
	return nil
}

// PutAction creates or replaces an action (FR-121); runs already created are
// untouched (FR-140). The lane fields are checked (FR-316/FR-316a/FR-318a) and
// the save is guarded (FR-308a); briefings are pushed in the background.
func (s *Service) PutAction(req *pb.PutActionRequest) (*pb.Action, error) {
	rt, err := s.runtimeFor(req.GetOrchestratorId())
	if err != nil {
		return nil, err
	}
	a, err := prepareAction(req)
	if err != nil {
		return nil, err
	}
	rec, ch, _, err := rt.guarded(req.GetGuard(), putActionChange(a, timestamppb.New(s.clock.Now())))
	if err != nil {
		return nil, err
	}
	rt.afterLaneChange(ch)
	rt.rearmScheduler()
	return proto.Clone(findAction(rec, a.GetName())).(*pb.Action), nil
}

// DeleteAction removes an action; timed events naming it are disabled with
// that reason and returned (FR-130); queued runs keep their composed prompt.
func (s *Service) DeleteAction(id, name string) ([]string, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	var disabled []string
	// runs.mu orders the kept-setting writes after a stepper's read-modify-write
	// of a queued run (claim, unclaim), which would otherwise overwrite them.
	rt.runs.mu.Lock()
	_, err = rt.updateRuns(func(o *pb.Orchestrator, runs RunTx) error {
		disabled = nil
		gone := findAction(o, name)
		if gone == nil {
			return fmt.Errorf("%w: action %s", ErrNotFound, name)
		}
		// Queued runs keep the deleted action's lane setting (FR-314b).
		queued, err := runs.Queued()
		if err != nil {
			return err
		}
		for _, r := range queued {
			if r.GetAction() != name || r.GetKeptLanes() != nil {
				continue
			}
			r.KeptLanes = &pb.KeptLaneSetting{LaneIds: slices.Clone(gone.GetLaneIds()), Mode: gone.GetLaneMode()}
			if err := runs.Put(r); err != nil {
				return err
			}
		}
		kept := o.Actions[:0]
		for _, a := range o.Actions {
			if a.GetName() != name {
				kept = append(kept, a)
			}
		}
		o.Actions = kept
		for _, ev := range o.TimedEvents {
			if ev.GetAction() == name && ev.GetEnabled() {
				ev.Enabled = false
				ev.DisabledReason = "action " + name + " was deleted"
				disabled = append(disabled, ev.GetName())
			}
		}
		return nil
	})
	rt.runs.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.emitChanged(id)
	rt.requestBriefing()
	rt.rearmScheduler()
	rt.triggerDispatch()
	return disabled, nil
}
