package escapehatch

import (
	"fmt"
	"path/filepath"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/managedblock"
)

// renderRule builds the marker-delimited managed block injected into the workspace
// CLAUDE.md (research R3). It enumerates the resolved commands with their when-to-use
// guidance, consent mode, the exact wrapper invocation, and the runs-on-host /
// asynchronous-result statements, so the agent reaches for the escape hatch instead
// of attempting the equivalent inside the sandbox (US3).
func renderRule(commands []*pb.EscapeHatchCommand) string {
	var b strings.Builder
	b.WriteString(ruleBeginMarker)
	b.WriteString("\n## Escape-hatch commands\n\n")
	b.WriteString("These commands run on the HOST, OUTSIDE this sandbox, in this sandbox's workspace. ")
	b.WriteString("Invoke one by name with the wrapper below; do NOT try to run its equivalent inside the sandbox. ")
	b.WriteString("Execution is asynchronous: the wrapper returns immediately and the result is delivered back to you as a follow-up message when the command finishes.\n\n")
	fmt.Fprintf(&b, "Invoke with: `%s <command-name> [--workspace <dir>] [-- <args...>]`\n", wrapperRelPath)
	b.WriteString("Only pass `--workspace` / `-- <args>` to a command that lists workspaces / subcommands below; the daemon rejects anything not listed.\n\n")
	for _, c := range commands {
		gate := "auto-run"
		if c.GetConsentMode() == pb.ConsentMode_CONSENT_MODE_REQUIRES_APPROVAL {
			gate = "requires the developer's approval"
		}
		fmt.Fprintf(&b, "- **%s** (%s) — %s\n", c.GetName(), gate, c.GetWhenToUse())
		if subs := nonEmpty(c.GetSubcommands()); len(subs) > 0 {
			fmt.Fprintf(&b, "  - subcommands (pass one after `--`): %s\n", strings.Join(subs, " | "))
		} else if pat := strings.TrimSpace(c.GetArgsPattern()); pat != "" {
			fmt.Fprintf(&b, "  - arguments after `--` must match: `%s`\n", pat)
		}
		if ws := nonEmpty(c.GetWorkspaces()); len(ws) > 0 {
			fmt.Fprintf(&b, "  - target one workspace with `--workspace`: %s\n", strings.Join(ws, ", "))
		}
	}
	b.WriteString("\n")
	b.WriteString(ruleEndMarker)
	return b.String()
}

// writeRuleBlock injects (or replaces) the managed rule block in the workspace
// CLAUDE.md, creating the file when absent and preserving any user content around the
// block (the shared managedblock mechanics, feature 009 research R12).
func writeRuleBlock(workspacePath string, commands []*pb.EscapeHatchCommand) error {
	path := filepath.Join(workspacePath, claudeMdRelPath)
	return managedblock.WriteBlock(path, ruleBeginMarker, ruleEndMarker, renderRule(commands))
}
