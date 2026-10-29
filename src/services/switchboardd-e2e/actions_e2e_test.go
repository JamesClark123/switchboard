//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// actionsHookAddr is this suite's hook listener: apart from the TUI suite's
// 18765/18766 and the default 8765 a developer's daemon uses.
const actionsHookAddr = "127.0.0.1:18767"

// stubSbx writes an `sbx` that reports a supported version and fails anything
// else: an orchestrator's actions and runs need no sandbox runtime (its
// reasoning sandbox simply fails to launch).
func stubSbx(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n  version) echo \"sbx version: v0.47.0 0000000\" ;;\n  ls) [ \"$2\" = \"--json\" ] && echo '{\"vms\":[]}' ;;\n  *) echo \"stub sbx: $1 unsupported\" >&2; exit 1 ;;\nesac\n"
	path := filepath.Join(dir, "sbx")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// sequenced is an action with every built-in and custom pre/post-prompts.
func sequenced(name string) *pb.Action {
	return &pb.Action{
		Name: name, Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "Review {pr} in {repo}.",
		Arguments: []*pb.ActionArgument{{Name: "pr", Required: true}, {Name: "repo", Required: true}, {Name: "skill", DefaultValue: "review"}},
		Sequence: &pb.ActionSequence{
			ModelEnabled: true, Model: pb.ModelChoice_MODEL_CHOICE_SONNET,
			EffortEnabled: true, Effort: pb.EffortChoice_EFFORT_CHOICE_HIGH,
			ClearEnabled: true,
			PrePrompts:   []string{"/pre-setupskill", "Look at {pr} with {skill}"},
			PostPrompts:  []string{"/tear-down-skill {repo}"},
		},
	}
}

func stepTexts(steps []*pb.RunStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.GetText())
	}
	return out
}

// TestActionSequenceE2E covers SWI-17's action storage over the daemon's
// gRPC API (T036): PutAction refuses prompts that break each FR-204 rule,
// naming the entry; a valid sequence round-trips; a legacy action without a
// sequence reads back without one, and a call of it gets the defaults
// (/model opus, /effort medium, the action prompt, /clear).
func TestActionSequenceE2E(t *testing.T) {
	bin := buildDaemon(t)
	dir := t.TempDir()
	d := startDaemon(t, bin, filepath.Join(dir, "data"), filepath.Join(dir, "ws"),
		"SWITCHBOARDD_SBX_BIN="+stubSbx(t), "SWITCHBOARDD_HOOK_ADDR="+actionsHookAddr)
	defer d.stop()
	c := dial(t, d.sock)
	ctx := context.Background()
	o, err := c.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "e2e-bot"})
	if err != nil {
		t.Fatal(err)
	}

	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "step"
	}
	for _, r := range []struct {
		name string
		edit func(s *pb.ActionSequence)
		want string
	}{
		{"first-word placeholder", func(s *pb.ActionSequence) { s.PrePrompts = []string{"ok", "{skill} now"} }, "pre-prompt 2: placeholder {skill} is in the first word"},
		{"slash placeholder", func(s *pb.ActionSequence) { s.PostPrompts = []string{"/{skill}"} }, "post-prompt 1: placeholder {skill} is in the first word"},
		{"shell with a value", func(s *pb.ActionSequence) { s.PostPrompts = []string{"!git checkout {pr}"} }, "post-prompt 1: a prompt starting with ! may not contain a placeholder ({pr})"},
		{"undeclared", func(s *pb.ActionSequence) { s.PrePrompts = []string{"Use {nope}"} }, "pre-prompt 1: placeholder {nope} names no argument"},
		{"too many", func(s *pb.ActionSequence) { s.PostPrompts = eleven }, "more than 10 post-prompts"},
		{"empty", func(s *pb.ActionSequence) { s.PrePrompts = []string{"ok", " "} }, "pre-prompt 2 is empty"},
		{"model", func(s *pb.ActionSequence) { s.Model = pb.ModelChoice_MODEL_CHOICE_UNSPECIFIED }, "model must be one of"},
		{"effort", func(s *pb.ActionSequence) { s.Effort = pb.EffortChoice_EFFORT_CHOICE_UNSPECIFIED }, "effort must be one of"},
	} {
		a := sequenced("bad")
		r.edit(a.Sequence)
		_, err := putAction(ctx, c, o.GetId(), a)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), r.want) {
			t.Errorf("%s: %v, want InvalidArgument naming %q", r.name, err, r.want)
		}
	}

	if _, err := putAction(ctx, c, o.GetId(), sequenced("review")); err != nil {
		t.Fatal(err)
	}
	legacy := &pb.Action{Name: "legacy", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "Tidy {repo}.", Arguments: []*pb.ActionArgument{{Name: "repo", Required: true}}}
	if _, err := putAction(ctx, c, o.GetId(), legacy); err != nil {
		t.Fatal(err)
	}
	det, err := c.GetOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: o.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]*pb.Action{}
	for _, a := range det.GetSummary().GetOrchestrator().GetActions() {
		actions[a.GetName()] = a
	}
	if _, ok := actions["bad"]; ok || len(actions) != 2 {
		t.Errorf("actions = %v", actions)
	}
	got, want := actions["review"].GetSequence(), sequenced("review").GetSequence()
	if !got.GetModelEnabled() || got.GetModel() != want.GetModel() || !got.GetEffortEnabled() || got.GetEffort() != want.GetEffort() || !got.GetClearEnabled() ||
		strings.Join(got.GetPrePrompts(), "|") != strings.Join(want.GetPrePrompts(), "|") || strings.Join(got.GetPostPrompts(), "|") != strings.Join(want.GetPostPrompts(), "|") {
		t.Errorf("sequence round-trip = %v", got)
	}
	if actions["legacy"].GetSequence() != nil {
		t.Errorf("a legacy action reads back with no sequence: %v", actions["legacy"].GetSequence())
	}

	// Calls snapshot the steps: the defaults for the legacy action, the
	// custom prompts with their placeholders filled for the other.
	run, err := c.CallAction(ctx, &pb.CallActionRequest{OrchestratorId: o.GetId(), Action: "legacy", Arguments: map[string]string{"repo": "acme/web"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stepTexts(run.GetSteps()), "|"); got != "/model opus|/effort medium|Tidy acme/web.|/clear" {
		t.Errorf("legacy steps = %q", got)
	}
	run, err = c.CallAction(ctx, &pb.CallActionRequest{OrchestratorId: o.GetId(), Action: "review", Arguments: map[string]string{"pr": "412", "repo": "acme/web"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stepTexts(run.GetSteps()), "|"); got != "/model sonnet|/effort high|/pre-setupskill|Look at 412 with review|Review 412 in acme/web.|/tear-down-skill acme/web|/clear" {
		t.Errorf("review steps = %q", got)
	}
	if got, err := c.GetRun(ctx, &pb.GetRunRequest{OrchestratorId: o.GetId(), RunId: run.GetId()}); err != nil || got.GetState() != pb.RunState_RUN_STATE_QUEUED || len(got.GetSteps()) != 7 {
		t.Errorf("queued run = %v, %v", got, err)
	}
}

// putAction saves an action as a lanes-aware client does (SWI-18): the
// open-lane guard's current list is fetched and confirmed.
func putAction(ctx context.Context, c pb.SwitchboardClient, id string, a *pb.Action) (*pb.Action, error) {
	req := &pb.PutActionRequest{OrchestratorId: id, Action: a, SetsLanes: true}
	if imp, err := c.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{OrchestratorId: id, Change: &pb.CheckLaneChangeRequest_PutAction{PutAction: req}}); err == nil {
		req.Guard = &pb.LaneGuardConfirm{UnlanedActions: imp.GetUnlanedActions(), UnlanedRuns: imp.GetUnlanedRuns()}
	}
	return c.PutAction(ctx, req)
}
