// Package secret manages the host runtime's secret store (`sbx secret`) for one
// daemon (feature 008, FR-104). Values arrive over the daemon connection, are
// written to the CLI's standard input exactly once, and never appear in argv,
// logs, errors, or any response: the list carries only the masked values the
// runtime prints.
package secret

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Timeouts are package constants, never environment variables (research R11).
const (
	listTimeout   = 30 * time.Second
	mutateTimeout = 60 * time.Second
)

// CLI runs the host sandbox CLI. Every argv it produces comes from the
// documentation-derived table in contracts/sbx-secret-cli.md and nowhere else;
// quickstart Scenario 0 reconciles each row on a real host.
type CLI struct {
	Bin string
}

// argv builders — the only place `sbx secret` flags are spelled.

func argvList() []string { return []string{"secret", "ls"} }

// argvSet stores a service secret read from stdin (the documented
// non-interactive form: `echo "$TOKEN" | sbx secret set -g github`); global is
// the runtime's default scope, `--sandbox <name>` scopes it to one sandbox, and
// `--force` overwrites an existing value without a prompt.
func argvSet(name, sandbox string) []string {
	a := []string{"secret", "set", name}
	if sandbox != "" {
		a = append(a, "--sandbox", sandbox)
	}
	return append(a, "--force")
}

// argvRemove removes a service secret by name or a registry credential by host
// (`--registry`), optionally sandbox-scoped; `-f` skips the confirmation the
// runtime documents for registry removals.
func argvRemove(name, sandbox, typ string) []string {
	a := []string{"secret", "rm"}
	if typ == "registry" {
		a = append(a, "--registry", name)
	} else {
		a = append(a, name)
	}
	if sandbox != "" {
		a = append(a, "--sandbox", sandbox)
	}
	return append(a, "-f")
}

// run executes `sbx <args…>` with stdin piped in (when non-empty) and returns
// the combined output; on failure the error carries the output tail but never
// the stdin.
func (c *CLI) run(ctx context.Context, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("sbx %s: %w%s", strings.Join(args, " "), err, outputTail(out))
	}
	return string(out), nil
}

// outputTail renders the last lines of a failed command for the error message.
func outputTail(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return ": " + strings.Join(lines, " | ")
}
