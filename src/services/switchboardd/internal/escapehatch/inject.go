package escapehatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

const (
	// wrapperRelPath is where the invocation wrapper is written inside the workspace
	// (bind-mounted into the sandbox at the same path). It sits beside the
	// feature-003 session marker under .switchboard/.
	wrapperRelPath = ".switchboard/escape-hatch"
	// claudeMdRelPath is the workspace CLAUDE.md the agent auto-loads; the rule block
	// is injected between the markers below, leaving any user content intact.
	claudeMdRelPath = "CLAUDE.md"
	ruleBeginMarker = "<!-- switchboard:escape-hatch:begin -->"
	ruleEndMarker   = "<!-- switchboard:escape-hatch:end -->"
)

// Inject writes (or, when the resolved set is empty, removes) this sandbox's
// escape-hatch wrapper and CLAUDE.md rule block into the workspace. It is called at
// every bring-up (Launch/Refresh/AddKit), mirroring how the daemon injects the agent
// hooks and the session marker. Best-effort like those: a failure is non-fatal and
// returned for logging.
func Inject(workspacePath, sandboxID, callbackURL string, commands []*pb.EscapeHatchCommand) error {
	if len(commands) == 0 {
		return removeInjection(workspacePath)
	}
	if err := writeWrapper(workspacePath, sandboxID, callbackURL); err != nil {
		return err
	}
	return writeRuleBlock(workspacePath, commands)
}

// writeWrapper lays down the executable wrapper. It embeds the sandbox id and the
// callback URL and forwards only the command NAME to the daemon — it can never send
// a command string (SC-004).
func writeWrapper(workspacePath, sandboxID, callbackURL string) error {
	path := filepath.Join(workspacePath, wrapperRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	script := fmt.Sprintf(`#!/bin/sh
# switchboard escape-hatch wrapper (feature 005) — generated, do not edit.
# Usage: escape-hatch <command-name>
# Sends ONLY the command name; the daemon resolves it against this sandbox's
# allowlist and runs the fixed, pre-authorized command on the host. Async: this
# returns immediately and the result is delivered back to the agent when the run
# finishes.
if [ -z "$1" ]; then
  echo "usage: escape-hatch <command-name>" >&2
  exit 2
fi
curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"sandbox_id\":\"%s\",\"name\":\"$1\"}" \
  %s
`, sandboxID, callbackURL)
	return os.WriteFile(path, []byte(script), 0o755)
}

// removeInjection deletes the wrapper and strips the rule block (no commands attached
// => nothing invokable, no rule; FR-037).
func removeInjection(workspacePath string) error {
	if err := os.Remove(filepath.Join(workspacePath, wrapperRelPath)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return stripRuleBlock(workspacePath)
}

// stripRuleBlock removes the managed block from CLAUDE.md, leaving user content.
func stripRuleBlock(workspacePath string) error {
	path := filepath.Join(workspacePath, claudeMdRelPath)
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	stripped := removeBlock(string(existing))
	if strings.TrimSpace(stripped) == "" {
		// The file was only our block — remove it entirely.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(stripped), 0o644)
}

// removeBlock returns content with the marker-delimited block (and its surrounding
// blank lines) removed. Content without the block is returned unchanged.
func removeBlock(content string) string {
	begin := strings.Index(content, ruleBeginMarker)
	if begin < 0 {
		return content
	}
	end := strings.Index(content, ruleEndMarker)
	if end < 0 {
		return content
	}
	end += len(ruleEndMarker)
	before := strings.TrimRight(content[:begin], "\n")
	after := strings.TrimLeft(content[end:], "\n")
	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after + "\n"
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after + "\n"
	}
}
