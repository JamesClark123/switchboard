package mcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Timeouts are package constants, never environment variables (plan Constraints;
// research R11).
const (
	listTimeout   = 60 * 1e9  // 60s: `mcp ls` + per-remote `auth status`
	mutateTimeout = 120 * 1e9 // 2m: `mcp add`/`mcp rm` may fetch metadata
	loadTimeout   = 60 * 1e9  // 60s per `mcp load` at launch
)

// WorkerServerPrefix names the per-worker gateway registrations the daemon
// creates for an orchestrator's workers (feature 009, research R4):
// `sxb-orch-<sandbox8>`. They are switchboard-internal: hidden from every
// listing, never marked, never appended to Sandbox.mcp_servers, and a developer
// cannot register a name with the prefix.
const WorkerServerPrefix = "sxb-orch-"

// CLI runs the host sandbox CLI. Every argv it produces comes from the documented
// table in contracts/sbx-mcp-cli.md and nowhere else.
type CLI struct {
	Bin string
}

// argv builders — the only place `sbx mcp` flags are spelled.

func argvList() []string                  { return []string{"mcp", "ls"} }
func argvAuthStatus(name string) []string { return []string{"mcp", "auth", "status", name, "--json"} }

// argvRemove always passes --force: a real host (2026-10-10) prompts
// `Remove MCP server "<name>" and its OAuth credential? (y/N)` and the daemon
// runs it with no terminal, so an unflagged rm would abort or hang.
func argvRemove(name string) []string { return []string{"mcp", "rm", name, "--force"} }

// argvAuthorize is `sbx mcp auth <name>`, with one-time scopes (`--scope` each,
// requested exactly as given) or `--no-scope`, never both (SWI-21 FR-418).
func argvAuthorize(name string, scopes []string, noScope bool) []string {
	a := []string{"mcp", "auth", name}
	if noScope {
		return append(a, "--no-scope")
	}
	for _, s := range scopes {
		a = append(a, "--scope", s)
	}
	return a
}

// argvInspect reads one server's details (SWI-21 research R11). The JSON schema
// is undocumented; parseInspect reads it tolerantly.
func argvInspect(name string) []string { return []string{"mcp", "inspect", name, "--json"} }

// argvVersion is `sbx version` (confirmed on a real host: it prints
// "sbx version: v0.47.0 <commit>"); there is no --version flag.
func argvVersion() []string { return []string{"version"} }
func argvAddRemote(name, url string, headers []string, clientID, authServer string, scopes []string) []string {
	// Always --skip-auth: authorization is the separate Authorize step (R2).
	// Each custom header is one documented `--header 'Name: value'` flag
	// (FR-103); credential headers carry a `${placeholder}` the gateway fills
	// from the host secret `mcp:<name>:<placeholder>`. A server without
	// Dynamic Client Registration takes a pre-registered `--client-id` and,
	// when it publishes no OAuth metadata, `--oauth-authorization-server`
	// (FR-105). Their combination with --skip-auth is assumed, not confirmed
	// on a real host (SWI-20).
	a := []string{"mcp", "add", name, "--url", url}
	for _, h := range headers {
		a = append(a, "--header", h)
	}
	if clientID != "" {
		a = append(a, "--client-id", clientID)
	}
	if authServer != "" {
		a = append(a, "--oauth-authorization-server", authServer)
	}
	// Default OAuth scopes, one `--scope` each (SWI-21 FR-416).
	for _, s := range scopes {
		a = append(a, "--scope", s)
	}
	return append(a, "--skip-auth")
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

// argvUnload detaches a loaded server from a sandbox (SWI-3): the server stays
// registered on the host. Mirrors argvLoad.
func argvUnload(name, sandbox string) []string {
	return []string{"mcp", "unload", name, "--sandbox", sandbox}
}

// --- feature 009: the per-worker registration (research R4) ---
//
// The worker side registers one remote server per worker and loads it into the
// sandbox; these are the only places the three argv rows are spelled, pinned
// by argv-asserting tests (contracts/orchestrator-mcp.md).

// AddRemote registers a remote endpoint without authorizing it:
// `sbx mcp add <name> --url <url> --skip-auth`. The combined output is returned
// so a warning the runtime prints (the loopback SSRF notice) can be logged.
func (c *CLI) AddRemote(ctx context.Context, name, url string) (string, error) {
	mctx, cancel := context.WithTimeout(ctx, time.Duration(mutateTimeout))
	defer cancel()
	return c.run(mctx, argvAddRemote(name, url, nil, "", "", nil)...)
}

// Load attaches a registered server to a sandbox live:
// `sbx mcp load <name> --sandbox <ref>`.
func (c *CLI) Load(ctx context.Context, name, sandboxRef string) (string, error) {
	lctx, cancel := context.WithTimeout(ctx, time.Duration(loadTimeout))
	defer cancel()
	return c.run(lctx, argvLoad(name, sandboxRef)...)
}

// Remove unregisters a server: `sbx mcp rm <name> --force`.
func (c *CLI) Remove(ctx context.Context, name string) (string, error) {
	mctx, cancel := context.WithTimeout(ctx, time.Duration(mutateTimeout))
	defer cancel()
	return c.run(mctx, argvRemove(name)...)
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

// runBounded runs a save's `mcp rm` / `mcp add` (SWI-21 research R7): the child
// gets its own process group, which is killed on the context deadline, and the
// call returns only after the child has exited, so "the process has exited"
// holds before any re-check. The combined output is returned; the error never
// carries stdin (there is none).
func (c *CLI) runBounded(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	setPgid(cmd)
	cmd.Cancel = func() error { killPgid(cmd); return nil }
	cmd.WaitDelay = 2e9 // 2s: bound a lingering pipe read after the group is killed
	out, err := cmd.CombinedOutput()
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
