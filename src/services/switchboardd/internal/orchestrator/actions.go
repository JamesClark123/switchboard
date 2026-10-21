package orchestrator

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

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
	if a.GetType() == pb.ActionType_ACTION_TYPE_REASON && strings.TrimSpace(a.GetWorkerTag()) != "" {
		return fmt.Errorf("%w: reason actions take no worker requirement", ErrInvalid)
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
	}
	for _, p := range placeholders(a.GetBody()) {
		if !declared[p] {
			return fmt.Errorf("%w: placeholder {%s} names no argument", ErrInvalid, p)
		}
	}
	_ = existing // uniqueness is replace-by-name; nothing else to check here
	return nil
}

// normalizeAction trims the fields validateAction accepted.
func normalizeAction(a *pb.Action) *pb.Action {
	out := proto.Clone(a).(*pb.Action)
	out.Name = strings.TrimSpace(out.GetName())
	out.WorkerTag = strings.TrimSpace(out.GetWorkerTag())
	for _, arg := range out.GetArguments() {
		arg.Name = strings.TrimSpace(arg.GetName())
	}
	return out
}

// validateCall checks a call's arguments against the action (FR-140): every
// required argument present (a default counts), no undeclared name, no value
// over maxArgumentBytes.
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
		if len(args[n]) > maxArgumentBytes {
			return fmt.Errorf("%w: argument %s is larger than %d bytes", ErrInvalid, n, maxArgumentBytes)
		}
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

// compose builds the prompt (FR-139, R8): every placeholder occurrence is
// replaced by the argument's value (an optional argument with neither value
// nor default replaces with nothing); each SUPPLIED argument with no
// placeholder is appended, in declaration order, as "\n\n<name>: <value>" —
// a multi-line value on the lines after "<name>:". `{{` becomes `{`.
func compose(a *pb.Action, args map[string]string) (string, error) {
	values := map[string]string{}
	for _, arg := range a.GetArguments() {
		v, supplied := args[arg.GetName()]
		if !supplied || v == "" {
			v = arg.GetDefaultValue()
		}
		values[arg.GetName()] = v
	}
	used := map[string]bool{}
	for _, p := range placeholders(a.GetBody()) {
		used[p] = true
	}
	out := placeholder.ReplaceAllStringFunc(a.GetBody(), func(m string) string {
		if m == "{{" {
			return "{"
		}
		return values[m[1:len(m)-1]]
	})
	for _, arg := range a.GetArguments() {
		n := arg.GetName()
		v, supplied := args[n]
		if used[n] || !supplied {
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
// untouched (FR-140). Briefings are pushed to reachable workers.
func (s *Service) PutAction(id string, a *pb.Action) (*pb.Action, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	a = normalizeAction(a)
	if err := validateAction(a, rt.record().GetActions()); err != nil {
		return nil, err
	}
	now := timestamppb.New(s.clock.Now())
	var saved *pb.Action
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		for i, ex := range o.Actions {
			if ex.GetName() == a.GetName() {
				a.CreatedAt, a.UpdatedAt = ex.GetCreatedAt(), now
				o.Actions[i] = a
				saved = a
				return nil
			}
		}
		a.CreatedAt, a.UpdatedAt = now, now
		o.Actions = append(o.Actions, a)
		saved = a
		return nil
	}); err != nil {
		return nil, err
	}
	s.emitChanged(id)
	rt.pushBriefings()
	rt.rearmScheduler()
	return proto.Clone(saved).(*pb.Action), nil
}

// DeleteAction removes an action; timed events naming it are disabled with
// that reason and returned (FR-130); queued runs keep their composed prompt.
func (s *Service) DeleteAction(id, name string) ([]string, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	var disabled []string
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		if findAction(o, name) == nil {
			return fmt.Errorf("%w: action %s", ErrNotFound, name)
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
	}); err != nil {
		return nil, err
	}
	s.emitChanged(id)
	rt.pushBriefings()
	rt.rearmScheduler()
	return disabled, nil
}
