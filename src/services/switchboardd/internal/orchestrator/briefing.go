package orchestrator

import (
	"fmt"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The briefing (research R12, contracts/orchestrator-mcp.md §Briefing): the
// managed CLAUDE.md block body rendered from one template and one data set —
// the orchestrator's name, the sandbox's role and the current actions. The
// worker side writes it verbatim; the MCP server's instructions carry the
// short form.

// renderBriefing renders the block body for a worker (or the reasoning sandbox).
func renderBriefing(name string, reasoning bool, actions []*pb.Action) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Orchestrator: %s\n\n", name)
	if reasoning {
		fmt.Fprintf(&b, "This sandbox is the **reasoning sandbox** of the switchboard orchestrator **%s** — runs here are `reason` actions whose summary *is* the answer.\n\n", name)
	} else {
		fmt.Fprintf(&b, "This sandbox is a **worker** of the switchboard orchestrator **%s**.\n\n", name)
	}
	b.WriteString("How an orchestrated sandbox behaves:\n")
	fmt.Fprintf(&b, "- Runs arrive as prompts framed `[switchboard orchestrator %q · run <id> · action \"<name>\"]`.\n", name)
	b.WriteString("  Do the run you are given. Ask for input through the normal conversation when you must; the\n  developer sees it.\n")
	b.WriteString("- **Every run ends with a completion report**: call `orchestrator_report_completion` with a short\n  summary of what you did (1–2 sentences). Do this before you finish. If the call fails, retry; a\n  failed report does not end the run — the developer will be asked to review it instead.\n")
	b.WriteString("- Report once per run. Do not report for runs that are not yours.\n\n")
	b.WriteString("Tools (MCP server `sxb-orch-…`, the orchestrator):\n")
	b.WriteString("- `orchestrator_list_actions` — the actions you may delegate, with their arguments. Use it before\n  calling an action you have not seen listed.\n")
	b.WriteString("- `orchestrator_call_action` — delegate work by calling an action with arguments. You get a run\n  id and queue position; the run executes on another sandbox (a `prompt` action on a free worker;\n  a `reason` action on the reasoning sandbox), not here.\n")
	b.WriteString("- `orchestrator_check_run` — the state, outcome and summary of a run you delegated.\n")
	b.WriteString("- `orchestrator_report_completion` — end your current run with a summary.\n\n")
	b.WriteString("Workflow per action type:\n")
	b.WriteString("- `prompt`: the orchestrator delivers the composed prompt to a free worker that satisfies the\n  action's requirements; that worker reports completion.\n")
	b.WriteString("- `reason`: the same on the reasoning sandbox; its summary is the result you read back with\n  `orchestrator_check_run`.\n\n")
	b.WriteString("Actions currently offered:\n")
	if len(actions) == 0 {
		b.WriteString("- (none yet)")
		return b.String()
	}
	for i, a := range actions {
		if i > 0 {
			b.WriteString("\n")
		}
		desc := a.GetDescription()
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "- `%s` (%s) — %s.", a.GetName(), actionTypeLabel(a.GetType()), strings.TrimSuffix(desc, "."))
		if len(a.GetArguments()) > 0 {
			var args []string
			for _, arg := range a.GetArguments() {
				s := "`" + arg.GetName() + "`"
				if arg.GetRequired() {
					s += " (required)"
				}
				args = append(args, s)
			}
			fmt.Fprintf(&b, " Arguments: %s.", strings.Join(args, ", "))
		}
		if a.GetWorkerTag() != "" {
			fmt.Fprintf(&b, " Workers tagged #%s only.", a.GetWorkerTag())
		}
	}
	return b.String()
}

// RenderInstructions is the short form the worker side puts in the MCP
// server's `instructions` (it needs no action list).
func RenderInstructions(name string, reasoning bool) string {
	role := "a worker"
	if reasoning {
		role = "the reasoning sandbox"
	}
	return fmt.Sprintf("This sandbox is %s of the switchboard orchestrator %q. Runs arrive as framed prompts; every run ends with orchestrator_report_completion (one short summary, once per run; retry if it fails). orchestrator_list_actions lists what you may delegate, orchestrator_call_action queues it on another sandbox, orchestrator_check_run reads a delegated run's outcome.", role, name)
}

// pushBriefings sends the current name + briefing to every reachable worker
// (rename, action changes); hosts that are down catch up on reconnect.
func (rt *runtime) pushBriefings() {
	for _, h := range rt.hostIDs() {
		api, err := rt.apiFor(h)
		if err != nil {
			continue
		}
		ctx, cancel := rt.callCtx(unaryTimeout)
		rt.pushBriefingsTo(ctx, h, api)
		cancel()
	}
}
