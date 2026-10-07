package mcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Timeouts are package constants, never environment variables (plan Constraints;
// research R11).
const (
	listTimeout   = 60 * 1e9  // 60s: `mcp ls` + per-remote `auth status`
	mutateTimeout = 120 * 1e9 // 2m: `mcp add`/`mcp rm` may fetch metadata
	loadTimeout   = 60 * 1e9  // 60s per `mcp load` at launch
)

// CLI runs the host sandbox CLI. Every argv it produces comes from the documented
// table in contracts/sbx-mcp-cli.md and nowhere else.
type CLI struct {
	Bin string
}

// argv builders — the only place `sbx mcp` flags are spelled.

func argvList() []string                  { return []string{"mcp", "ls"} }
func argvAuthStatus(name string) []string { return []string{"mcp", "auth", "status", name, "--json"} }
func argvRemove(name string) []string     { return []string{"mcp", "rm", name} }
func argvAuthorize(name string) []string  { return []string{"mcp", "auth", name} }
func argvVersion() []string               { return []string{"--version"} }
func argvAddRemote(name, url string) []string {
	// Always --skip-auth: authorization is the separate Authorize step (R2).
	return []string{"mcp", "add", name, "--url", url, "--skip-auth"}
}
func argvAddLocal(name, command string, args []string, dir string) []string {
	out := []string{"mcp", "add", name, "--command", command}
	if len(args) > 0 {
		out = append(out, "--args", strings.Join(args, ","))
	}
	if dir != "" {
		out = append(out, "--dir", dir)
	}
	return append(out, "--skip-auth")
}
func argvLoad(name, sandbox string) []string {
	return []string{"mcp", "load", name, "--sandbox", sandbox}
}

// run executes the CLI and returns its combined output. On failure the error
// carries the tail of that output: sbx explains failures there, not in its exit
// status, and the client shows the error verbatim (FR-078).
func (c *CLI) run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, c.Bin, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w%s", c.Bin, strings.Join(args, " "), err, outputTail(out))
	}
	return string(out), nil
}

// runStreaming executes the CLI in its own process group, delivering each output
// line (stdout and stderr interleaved) to onLine as it arrives. Cancelling ctx
// kills the whole group — the authorize flow must never outlive its deadline.
func (c *CLI) runStreaming(ctx context.Context, onLine func(string), args ...string) error {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	setPgid(cmd)
	cmd.Cancel = func() error { killPgid(cmd); return nil }
	cmd.WaitDelay = 2e9 // 2s: bound any lingering pipe read after the group is killed

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return fmt.Errorf("%s %s: %w", c.Bin, strings.Join(args, " "), err)
	}
	var wg sync.WaitGroup
	var tail []string
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			tail = append(tail, line)
			if len(tail) > 5 {
				tail = tail[1:]
			}
			if onLine != nil {
				onLine(line)
			}
		}
	}()
	waitErr := cmd.Wait()
	_ = pw.Close()
	wg.Wait()
	if waitErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s %s: %w%s", c.Bin, strings.Join(args, " "), waitErr, outputTail([]byte(strings.Join(tail, "\n"))))
	}
	return nil
}

// outputTail renders the last few non-empty lines of out as a ": a | b" suffix
// (newlines collapsed — status lines are single-line), or "" when empty. Mirrors
// sandbox/runner.go so failures read the same everywhere.
func outputTail(out []byte) string {
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	tail := strings.Join(lines, " | ")
	if r := []rune(tail); len(r) > 400 {
		tail = "…" + string(r[len(r)-400:])
	}
	return ": " + tail
}
