package orchestrator

import (
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestRenderBriefingFollowsTheContractTemplate(t *testing.T) {
	actions := []*pb.Action{
		{Name: "review-pr", Type: pb.ActionType_ACTION_TYPE_PROMPT, Description: "Review a pull request and leave comments.", Arguments: []*pb.ActionArgument{
			{Name: "number", Required: true}, {Name: "repo", Required: true}, {Name: "focus"},
		}},
		{Name: "triage", Type: pb.ActionType_ACTION_TYPE_REASON, WorkerTag: "senior"},
	}
	got := renderBriefing("review-bot", false, actions)
	for _, want := range []string{
		"## Orchestrator: review-bot\n\n",
		"This sandbox is a **worker** of the switchboard orchestrator **review-bot**.",
		"Runs arrive as prompts framed `[switchboard orchestrator \"review-bot\" · run <id> · action \"<name>\"]`.",
		"**Every run ends with a completion report**: call `orchestrator_report_completion`",
		"Report once per run. Do not report for runs that are not yours.",
		"Tools (MCP server `sxb-orch-…`, the orchestrator):",
		"- `orchestrator_list_actions` —", "- `orchestrator_call_action` —", "- `orchestrator_check_run` —", "- `orchestrator_report_completion` —",
		"Workflow per action type:",
		"Actions currently offered:\n- `review-pr` (prompt) — Review a pull request and leave comments. Arguments: `number` (required), `repo` (required), `focus`.\n- `triage` (reason) — (no description). Workers tagged #senior only.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("briefing lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "reasoning sandbox** of") {
		t.Error("a worker briefing must not claim the reasoning role")
	}
	// Reasoning sandbox variant, no actions.
	r := renderBriefing("review-bot", true, nil)
	if !strings.Contains(r, "This sandbox is the **reasoning sandbox** of the switchboard orchestrator **review-bot** — runs here are `reason` actions whose summary *is* the answer.") {
		t.Errorf("reasoning briefing = %s", r)
	}
	if !strings.HasSuffix(r, "Actions currently offered:\n- (none yet)") {
		t.Errorf("empty action list = %q", r[len(r)-60:])
	}
	// The MCP instructions are the short form.
	if s := RenderInstructions("review-bot", false); !strings.Contains(s, `orchestrator "review-bot"`) || !strings.Contains(s, "a worker") {
		t.Errorf("instructions = %q", s)
	}
	if s := RenderInstructions("review-bot", true); !strings.Contains(s, "the reasoning sandbox") {
		t.Errorf("instructions = %q", s)
	}
}
