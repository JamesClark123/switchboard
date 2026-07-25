package escapehatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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
	b.WriteString(fmt.Sprintf("Invoke with: `%s <command-name>`\n\n", wrapperRelPath))
	for _, c := range commands {
		gate := "auto-run"
		if c.GetConsentMode() == pb.ConsentMode_CONSENT_MODE_REQUIRES_APPROVAL {
			gate = "requires the developer's approval"
		}
		fmt.Fprintf(&b, "- **%s** (%s) — %s\n", c.GetName(), gate, c.GetWhenToUse())
	}
	b.WriteString("\n")
	b.WriteString(ruleEndMarker)
	return b.String()
}

// writeRuleBlock injects (or replaces) the managed rule block in the workspace
// CLAUDE.md, creating the file when absent and preserving any user content around the
// block.
func writeRuleBlock(workspacePath string, commands []*pb.EscapeHatchCommand) error {
	path := filepath.Join(workspacePath, claudeMdRelPath)
	block := renderRule(commands)

	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(path, []byte(block+"\n"), 0o644)
		}
		return err
	}
	// Replace an existing block in place; otherwise append.
	content := string(existing)
	var updated string
	if strings.Contains(content, ruleBeginMarker) && strings.Contains(content, ruleEndMarker) {
		updated = replaceBlock(content, block)
	} else {
		updated = strings.TrimRight(content, "\n") + "\n\n" + block + "\n"
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

// replaceBlock swaps the existing marker-delimited block for block, leaving the
// surrounding user content untouched.
func replaceBlock(content, block string) string {
	begin := strings.Index(content, ruleBeginMarker)
	end := strings.Index(content, ruleEndMarker)
	if begin < 0 || end < 0 {
		return content
	}
	end += len(ruleEndMarker)
	return content[:begin] + block + content[end:]
}
