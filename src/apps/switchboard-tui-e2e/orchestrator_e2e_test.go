//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestTUITwoDaemonsE2E is the feature-009 harness smoke test: two stub daemons
// (local + "remote"), the remote one reached through the stub ssh that execs
// `sxbd dial-stdio`; the daemon screen lists both connected, and shift+→ once
// more reaches the Jobs screen (feature 010: Sandboxes → Daemons → Jobs).
func TestTUITwoDaemonsE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	remote := startSecondDaemon(t, daemon, sbxDir)
	sshDir := stubSSH(t, remote.sock, daemon)

	p := spawnTUIWithHosts(t, tui, sock, seedSource(t), t.TempDir(), sshDir)
	p.expect(t, "Switchboard", 20*time.Second)

	m := p.mark()
	p.send("\x1b[1;2C") // shift+→: daemons
	p.expectNew(t, m, "(local ", 10*time.Second)
	p.expect(t, "(ssh remote)", 5*time.Second)
	// The startup sign-in dialed the remote host through the stub ssh: the
	// status line reports it.
	p.expect(t, "signed in to 1 of 1 saved hosts", 20*time.Second)

	m = p.mark()
	p.send("\x1b[1;2C") // shift+→: jobs
	p.expectNew(t, m, "Overview", 10*time.Second)
	p.expect(t, "No orchestrators yet", 5*time.Second)
	m = p.mark()
	p.send("\x1b[1;2D") // shift+←: back to daemons
	p.expectNew(t, m, "(local ", 5*time.Second)
	p.send("q")
}

// TestTUIOrchestratorCreateE2E drives US1 through the real binaries: Daemons ›
// <local> › Orchestrators (feature 010) lists none, `n` creates review-bot on
// that daemon and the row is listed reachable; the TUI is relaunched and the
// row is still there (persisted on the daemon); `d` plus confirm removes it.
func TestTUIOrchestratorCreateE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	configDir := t.TempDir()
	src := seedSource(t)

	p := spawnTUIWithConfig(t, tui, sock, src, configDir)
	p.expect(t, "Switchboard", 20*time.Second)
	p.expect(t, "Jobs", 5*time.Second) // the header strip, on the list screen too
	openDaemonOrchs(t, p, "(no orchestrators on ")

	p.send("n")
	p.expect(t, "New orchestrator", 10*time.Second)
	p.send("review-bot")
	p.send("\x13") // ctrl+s
	p.expect(t, "created review-bot", 15*time.Second)
	p.expect(t, "reachable", 10*time.Second)
	p.send("q")

	// Relaunch: the orchestrator survives the TUI (it lives on the daemon).
	p = spawnTUIWithConfig(t, tui, sock, src, configDir)
	p.expect(t, "Switchboard", 20*time.Second)
	openDaemonOrchs(t, p, "review-bot")
	p.expect(t, "0 workers", 5*time.Second)

	p.send("d")
	p.expect(t, "Remove orchestrator review-bot?", 10*time.Second)
	p.expect(t, "reasoning sandbox is destroyed", 5*time.Second)
	p.send("y")
	p.expect(t, "removed review-bot", 15*time.Second)
	// This process never showed the empty state before the removal, so a plain
	// expect is the right barrier (the renderer repaints only changed lines).
	p.expect(t, "(no orchestrators on ", 10*time.Second)
	p.send("q")
}

// TestTUIAttachLocalAndRemoteE2E drives US2 through two stub daemons: `o` on a
// local sandbox registers the per-worker gateway server on the local stub
// (`mcp add … --url http://127.0.0.1:` then `mcp load`, never stop/rm/create);
// `o` on a sandbox of the "remote" daemon first offers to add that host to the
// orchestrator, and the attach then reaches the remote stub through the stub
// ssh; `o` again detaches (`mcp rm … --force`).
func TestTUIAttachLocalAndRemoteE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	localSbx := stubSbx(t)
	remoteSbx := stubSbx(t)
	remote := startSecondDaemon(t, daemon, remoteSbx)
	sshDir := stubSSH(t, remote.sock, daemon)
	// The orchestrator's daemon dials the remote host itself (`ssh … sxbd
	// dial-stdio`), so the stub ssh goes on its PATH as well as the TUI's.
	sock, _ := startDaemonWithPath(t, daemon, "e2e-host", fmt.Sprintf("/tmp/sb-e2e-%d.sock", os.Getpid()), "127.0.0.1:18765", localSbx, sshDir)
	src := seedSource(t)

	// The remote daemon's workspace root gets a folder so the launch browser,
	// which roots on the target host's workspace root, has a candidate.
	if err := os.MkdirAll(filepath.Join(remote.workspaceRoot, "rproj"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := spawnTUIWithHosts(t, tui, sock, src, t.TempDir(), sshDir)
	launchOne(t, p) // a local sandbox
	connectRemote(t, p)
	// A sandbox on the remote daemon through the wizard's host pick (H).
	m0 := p.mark()
	p.send("n")
	p.expectNew(t, m0, "Launch sandbox", 10*time.Second)
	m0 = p.mark()
	p.send("H")
	p.expectNew(t, m0, "Create the sandbox on which host?", 10*time.Second)
	p.send("j")
	m0 = p.mark()
	p.send("\r")
	p.expectNew(t, m0, "rproj", 15*time.Second)
	p.send(" ")
	m0 = p.mark()
	p.send("\r")
	p.expectNew(t, m0, "@remote", 15*time.Second) // the new row on the All tab names its host
	deadline0 := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline0) && !has(sbxCalls(t, remoteSbx), "create ") {
		time.Sleep(200 * time.Millisecond)
	}
	if !has(sbxCalls(t, remoteSbx), "create ") {
		t.Fatalf("the remote stub should have created the sandbox; calls: %v", sbxCalls(t, remoteSbx))
	}

	// Create the orchestrator on the local daemon.
	createOrchestrator(t, p)
	backToList(t, p)

	// Local attach: the All tab's first row is the local sandbox.
	m1 := p.mark()
	p.send("o")
	p.expectNew(t, m1, "to an orchestrator", 10*time.Second)
	p.send("\r")
	p.expect(t, "attached", 15*time.Second)
	calls := sbxCalls(t, localSbx)
	var sawAdd, sawLoad bool
	for _, c := range calls {
		switch {
		case strings.HasPrefix(c, "mcp add sxb-orch-") && strings.Contains(c, "--url http://127.0.0.1:") && strings.HasSuffix(c, "--skip-auth"):
			sawAdd = true
		case strings.HasPrefix(c, "mcp load sxb-orch-") && strings.Contains(c, "--sandbox "):
			if !sawAdd {
				t.Errorf("mcp load must follow mcp add: %v", calls)
			}
			sawLoad = true
		case strings.HasPrefix(c, "stop ") || strings.HasPrefix(c, "rm ") || (strings.HasPrefix(c, "create ") && sawAdd):
			t.Errorf("attach must never stop/rm/create the sandbox: %q", c)
		}
	}
	if !sawAdd || !sawLoad {
		t.Fatalf("expected the local stub to see mcp add then mcp load; calls: %v", calls)
	}

	// Remote attach: move to the remote sandbox's row (the remote host's tab:
	// All · localhost · remote), then `o` offers to add the host first.
	m := p.mark()
	p.send("\x1b[C")
	p.send("\x1b[C")
	p.expectNew(t, m, "rproj", 10*time.Second)
	m = p.mark()
	p.send("o")
	p.expectNew(t, m, "will add this host", 10*time.Second)
	p.send("\r")
	p.expect(t, "Add host remote to review-bot?", 10*time.Second)
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "attached", 30*time.Second)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if has(sbxCalls(t, remoteSbx), "mcp add sxb-orch-") && has(sbxCalls(t, remoteSbx), "mcp load sxb-orch-") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if rc := sbxCalls(t, remoteSbx); !has(rc, "mcp add sxb-orch-") || !has(rc, "mcp load sxb-orch-") {
		t.Fatalf("the remote stub should see the registration through the stub ssh; calls: %v\n%s", rc, p.tail(4000))
	}

	// Detach the remote worker. Workers leave the everyday tabs once attached
	// (FR-115), so the detach goes through the orchestrator's Workers view
	// (Daemons › <local> › Orchestrators › review-bot): the roster lists the
	// local worker first, the remote one second; `x` confirms, then `mcp rm
	// --force` runs on the remote stub.
	openDaemonOrchs(t, p, "2 workers")
	p.send("\r")
	p.expect(t, "Orchestrator review-bot", 10*time.Second)
	p.send("j")
	time.Sleep(300 * time.Millisecond)
	m = p.mark()
	p.send("x")
	p.expectNew(t, m, "Detach", 10*time.Second)
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "detached", 20*time.Second)
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !has(sbxCalls(t, remoteSbx), "mcp rm sxb-orch-") {
		time.Sleep(200 * time.Millisecond)
	}
	if rc := sbxCalls(t, remoteSbx); !has(rc, "mcp rm sxb-orch-") {
		t.Fatalf("detach should run mcp rm --force on the remote stub; calls: %v\n%s", rc, p.tail(4000))
	}
	p.send("q")
}

// connectRemote makes sure the "remote" host is connected: the daemon screen's
// `c` on its row (blank password = key/agent auth, which the stub ssh accepts)
// reports either a fresh connection or that the startup sign-in already made it.
func connectRemote(t *testing.T, p *ptyProcess) {
	t.Helper()
	m := p.mark()
	p.send("\x1b[1;2C")
	p.expectNew(t, m, "(local ", 10*time.Second) // the daemon screen's rows
	p.send("j")
	time.Sleep(300 * time.Millisecond) // let the cursor move land before c
	m = p.mark()
	p.send("c")
	deadline := time.Now().Add(30 * time.Second)
	answered := false
	for time.Now().Before(deadline) {
		out := p.buf.String()[m:]
		switch {
		case strings.Contains(out, "connected to remote") || strings.Contains(out, "remote is already connected"):
			m2 := p.mark()
			p.send("\x1b[1;2D")
			p.expectNew(t, m2, "All Sandboxes", 10*time.Second) // the list's tab bar
			return
		case strings.Contains(out, "could not connect to remote"):
			t.Fatalf("remote connect failed:\n%s", out)
		case !answered && strings.Contains(out, "Connect to remote"):
			answered = true
			p.send("\r") // blank password: key/agent auth (the stub accepts anything)
		}
		time.Sleep(100 * time.Millisecond)
	}
	all := p.buf.String()
	if len(all) > 6000 {
		all = all[len(all)-6000:]
	}
	t.Fatalf("remote never connected (mark %d, buffer %d):\n%s", m, len(p.buf.String()), all)
}

// has reports whether any call starts with prefix.
func has(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// TestTUIActionRunE2E drives US3 through the real binaries: create review-bot,
// attach the stub-launched local sandbox, author review-pr with two arguments on
// the Jobs Actions tab, call it with values — the Jobs Overview shows the run in
// progress and the stub agent (the daemon's PTY runs `sbx exec -it …`, which the
// stub copies to a file) received the framed prompt with both substituted
// values and the appended argument; a second call waits at position 1.
func TestTUIActionRunE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	p := spawnTUI(t, tui, sock, seedSource(t))
	launchOne(t, p)

	// Orchestrator + attach.
	createOrchestrator(t, p)
	backToList(t, p)
	m := p.mark()
	p.send("o")
	p.expectNew(t, m, "to an orchestrator", 10*time.Second)
	p.send("\r")
	p.expect(t, "attached", 15*time.Second)

	// Author the action on the Jobs Actions tab (shift+← from the list).
	openJobs(t, p)
	p.gotoView(t, "no actions — press a to add one")
	m = p.mark()
	p.send("a")
	p.expectNew(t, m, "Body", 10*time.Second)
	p.send("review-pr")
	p.tab()
	p.send("Review a pull request")
	p.tab() // type select: prompt (default)
	p.tab()
	p.send("Review pull request {number} in {repo} and leave review comments.")
	p.tab() // no lanes on review-bot: Lanes and Mode are hidden
	p.send("number *\nrepo *\nfocus")
	p.send("\x13")
	p.expect(t, "saved action review-pr", 15*time.Second)
	p.expect(t, "{number*, repo*, focus}", 10*time.Second)

	// Call it: the form asks for the three arguments.
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "number *", 10*time.Second)
	p.send("412")
	p.tab()
	p.send("acme/web")
	p.tab()
	p.send("tests")
	p.send("\x13")
	p.expect(t, "queued run-", 15*time.Second)
	p.expect(t, "at position 1", 5*time.Second)

	// The Overview shows it running; the stub agent received the framed prompt.
	m = p.mark()
	p.send("\x1b[D") // ← Overview
	p.expectRow(t, m, `review-pr +running +review-bot @`, 20*time.Second)
	// The stub agent copies its stdin to agent-<container ref>.in.
	deadline := time.Now().Add(20 * time.Second)
	var typed string
	for time.Now().Before(deadline) && typed == "" {
		files, _ := filepath.Glob(filepath.Join(sbxDir, "agent-*.in"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "focus: tests") {
				typed = string(b)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, want := range []string{
		`[switchboard orchestrator "review-bot" · run run-`,
		"Review pull request 412 in acme/web and leave review comments.",
		"\n\nfocus: tests",
		"call orchestrator_report_completion",
	} {
		if !strings.Contains(typed, want) {
			t.Errorf("the agent should have received %q; typed:\n%s", want, typed)
		}
	}
	// A second call waits at position 1 (one worker, one run).
	m = p.mark()
	p.send("\x1b[C") // → Actions
	p.expectNew(t, m, "{number*, repo*, focus}", 10*time.Second)
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "number *", 10*time.Second)
	p.send("7")
	p.tab()
	p.send("acme/web")
	m = p.mark()
	p.send("\x13")
	p.expectNew(t, m, "at position 1", 15*time.Second)
	m = p.mark()
	p.send("\x1b[D") // ← Overview: the queued row
	p.expectRow(t, m, `#1 +review-pr +user +review-bot @`, 10*time.Second)
	p.send("q")
}

// sandboxIDFromHooks reads a launched sandbox's id out of the hook settings the
// daemon injects into its workspace (the curl payload embeds it).
func sandboxIDFromHooks(t *testing.T, workspaceRoot string) string {
	t.Helper()
	// Only launched sandboxes (sandbox-<id8>): the orchestrator's reasoning
	// sandbox (reasoning-<id8>) has hooks too.
	files, _ := filepath.Glob(filepath.Join(workspaceRoot, "sandbox-*", ".claude", "settings.local.json"))
	re := regexp.MustCompile(`sandbox_id[^0-9a-f]*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if m := re.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	t.Fatalf("no hook settings with a sandbox id under %s (files: %v)", workspaceRoot, files)
	return ""
}

// lastLineWith returns the most recent captured line containing sub.
func (p *ptyProcess) lastLineWith(sub string) string {
	lines := strings.Split(p.tail(1<<20), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], sub) {
			return lines[i]
		}
	}
	return ""
}

// openDaemonOrchs opens Daemons › <local daemon> › Orchestrators from the
// sandbox list (feature 010: orchestrator administration lives under the daemon
// that hosts it) and waits for want. Arrow keys, not j/k: runes sent together
// would arrive as one key.
func openDaemonOrchs(t *testing.T, p *ptyProcess, want string) {
	t.Helper()
	m := p.mark()
	p.send("\x1b[1;2C") // shift+→: daemons
	p.expectNew(t, m, "(local ", 10*time.Second)
	p.send("\x1b[A") // the local daemon is the first row
	time.Sleep(300 * time.Millisecond)
	m = p.mark()
	p.send("\r") // its options: MCP gateway · Secrets · Orchestrators
	p.expectNew(t, m, "Secrets", 10*time.Second)
	p.send("\x1b[B")
	p.send("\x1b[B")
	time.Sleep(300 * time.Millisecond)
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, want, 10*time.Second)
}

// openJobs opens the Jobs screen from the sandbox list (shift+← wraps to it).
func openJobs(t *testing.T, p *ptyProcess) {
	t.Helper()
	m := p.mark()
	p.send("\x1b[1;2D")
	p.expectNew(t, m, "Overview", 10*time.Second)
}

// backToList returns to the sandbox list from Daemons › Orchestrators (shift+←).
func backToList(t *testing.T, p *ptyProcess) {
	t.Helper()
	m := p.mark()
	p.send("\x1b[1;2D")
	p.expectNew(t, m, "All Sandboxes", 10*time.Second)
}

// createOrchestrator makes review-bot on the local daemon from the sandbox list
// and stays on Daemons › <local> › Orchestrators, its row highlighted.
func createOrchestrator(t *testing.T, p *ptyProcess) {
	t.Helper()
	openDaemonOrchs(t, p, "(no orchestrators on ")
	p.send("n")
	p.expect(t, "New orchestrator", 10*time.Second)
	p.send("review-bot")
	p.send("\x13")
	p.expect(t, "created review-bot", 15*time.Second)
}

// addPingAction authors a no-argument action on the Jobs Actions tab, reached
// from Daemons › Orchestrators (shift+→), and stays there. Callers attach no
// worker first, so the save goes through the open-lane guard.
func addPingAction(t *testing.T, p *ptyProcess) {
	t.Helper()
	m := p.mark()
	p.send("\x1b[1;2C") // shift+→: jobs
	p.expectNew(t, m, "Overview", 10*time.Second)
	p.gotoView(t, "no actions — press a to add one")
	m = p.mark()
	p.send("a")
	p.expectNew(t, m, "Body", 10*time.Second)
	p.send("ping")
	p.tab()
	p.send("Ping the worker")
	p.tab() // type: prompt
	p.tab()
	p.send("Reply with pong.")
	p.tab() // arguments: blank (no lanes: Lanes and Mode are hidden)
	m = p.mark()
	p.send("\x13")
	// No worker is attached yet, so the un-laned action is saved through the
	// open-lane guard's confirm (SWI-18 FR-308a).
	p.expectNew(t, m, "Save anyway?", 15*time.Second)
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "saved action ping", 15*time.Second)
}

// SWI-18 US1: a lane made in the Lanes view, the worker added to it, an action
// exclusive to it; a call runs on that worker.
func TestTUILanesE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock := startDaemon(t, daemon, stubSbx(t))
	p := spawnTUI(t, tui, sock, seedSource(t))
	launchOne(t, p)
	createOrchestrator(t, p)
	backToList(t, p)
	m := p.mark()
	p.send("o")
	p.expectNew(t, m, "to an orchestrator", 10*time.Second)
	p.send("\r")
	p.expect(t, "attached", 15*time.Second)

	// Daemons › Orchestrators › review-bot › Lanes (← from Workers).
	openDaemonOrchs(t, p, "review-bot")
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "Orchestrator review-bot", 10*time.Second)
	m = p.mark()
	p.send("\x1b[D")
	p.expectNew(t, m, "no lanes yet — a to add one", 10*time.Second)
	m = p.mark()
	p.send("a")
	p.expectNew(t, m, "New lane", 10*time.Second)
	p.send("frontend")
	m = p.mark()
	p.send("\x13")
	p.expectNew(t, m, "saved lane frontend", 15*time.Second)
	m = p.mark()
	p.send("w")
	p.expectNew(t, m, "Members of frontend", 10*time.Second)
	p.send(" ")
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "saved members of frontend", 15*time.Second)
	p.expect(t, "members: sandbox", 10*time.Second)

	// Jobs › Actions: an action exclusive to frontend.
	m = p.mark()
	p.send("\x1b") // back to Orchestrators
	p.expectNew(t, m, "review-bot", 10*time.Second)
	m = p.mark()
	p.send("\x1b[1;2C") // shift+→: jobs
	p.expectNew(t, m, "Overview", 10*time.Second)
	p.gotoView(t, "no actions — press a to add one")
	m = p.mark()
	p.send("a")
	p.expectNew(t, m, "Body", 10*time.Second)
	p.send("build")
	p.tab()
	p.tab() // type: prompt
	p.tab()
	p.send("Build it.")
	p.tab()
	p.expect(t, "Lanes (prompt only)", 10*time.Second)
	p.send(" ") // tick frontend
	p.tab()     // mode: exclusive (preselected)
	p.tab()     // arguments
	m = p.mark()
	p.send("\x13")
	p.expectNew(t, m, "saved action build", 15*time.Second)
	p.expect(t, "⇢ frontend · exclusive", 10*time.Second)

	// Call it: it runs on the lane's worker.
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "build", 10*time.Second)
	m = p.mark()
	p.send("\x13")
	p.expectNew(t, m, "queued run-", 15*time.Second)
	m = p.mark()
	p.send("\x1b[D") // ← Overview
	p.expectRow(t, m, `build +running +review-bot @.* on sandbox`, 20*time.Second)
	p.send("q")
}

func TestTUIHiddenWorkersE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock, wsRoot := startDaemonAt(t, daemon, sbxDir)
	p := spawnTUI(t, tui, sock, seedSource(t))
	launchOne(t, p)
	createOrchestrator(t, p)
	backToList(t, p)

	// Attaching hides the worker from the everyday tabs: All is now empty.
	m := p.mark()
	p.send("o")
	p.expectNew(t, m, "to an orchestrator", 10*time.Second)
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "attached", 15*time.Second)
	p.expectNew(t, m, "No sandboxes", 10*time.Second)

	// O shows the Orchestrator Sandboxes tab with the row and its third line.
	m = p.mark()
	p.send("O")
	p.expectNew(t, m, "Orchestrator Sandboxes", 10*time.Second)
	p.expectNew(t, m, "orchestrator: review-bot", 10*time.Second)
	// → leaves the tab and it disappears from the bar.
	m = p.mark()
	p.send("\x1b[C")
	p.expectNew(t, m, "All Sandboxes", 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	if bar := p.lastLineWith("All Sandboxes"); strings.Contains(bar, "Orchestrator Sandboxes") {
		t.Fatalf("the tab should hide after leaving it: %q", bar)
	}

	// The agent asking for input surfaces the worker at the top, marked. The
	// hook posts the way the injected command does: event + sandbox in the
	// query, Claude Code's hook input (with the notification_type) as the body.
	id := sandboxIDFromHooks(t, wsRoot)
	m = p.mark()
	resp, err := http.Post("http://127.0.0.1:18765/hook?event=Notification&sandbox_id="+id, "application/json", strings.NewReader(`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("hook POST = %d", resp.StatusCode)
	}
	p.expectNew(t, m, "worker · needs input", 15*time.Second)
	p.send("q")
}

func TestTUITimedEventE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	src := seedSource(t)
	p := spawnTUI(t, tui, sock, src)
	p.expect(t, "Switchboard", 20*time.Second)
	createOrchestrator(t, p)
	addPingAction(t, p)

	// Events tab: a timed event on ping, every minute, dedupe on (default).
	p.gotoView(t, "no events — press a to add one")
	m := p.mark()
	p.send("a")
	p.expectNew(t, m, "Every (minutes)", 10*time.Second)
	p.send("poll")
	p.tab() // action: ping (the only one)
	p.tab()
	p.send("\x15") // clear the default interval
	p.send("1")
	p.tab() // fixed arguments: none
	p.tab() // enabled: yes
	m = p.mark()
	p.send("\x13")
	for _, want := range []string{"poll", "every 1m", "[on]", "next ", "dedupe"} {
		p.expectNew(t, m, want, 15*time.Second)
	}
	// space flips it off and back on.
	m = p.mark()
	p.send(" ")
	p.expectNew(t, m, "[off]", 10*time.Second)
	m = p.mark()
	p.send(" ")
	p.expectNew(t, m, "[on]", 10*time.Second)

	// Persisted on the daemon: a fresh TUI still lists it.
	p.send("q")
	time.Sleep(500 * time.Millisecond)
	p2 := spawnTUI(t, tui, sock, src)
	p2.expect(t, "Switchboard", 20*time.Second)
	openJobs(t, p2)
	p2.gotoView(t, "every 1m")
	p2.send("q")
}

// gotoView presses → on the orchestrator detail or the Jobs screen until a view
// or tab renders marker (both reopen on whichever was last shown).
func (p *ptyProcess) gotoView(t *testing.T, marker string) {
	t.Helper()
	for i := 0; i < 7; i++ {
		m := p.mark()
		p.send("\x1b[C")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if s := p.buf.String(); m <= len(s) && strings.Contains(s[m:], marker) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	t.Fatalf("no view rendered %q\n%s", marker, p.tail(3000))
}

// mcpPost sends one JSON-RPC tools/call to a worker endpoint the way the host
// gateway does (stateless streamable HTTP, JSON responses).
func mcpPost(t *testing.T, url, tool, args string) (int, string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestTUIWorkerToolsE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	localSbx := stubSbx(t)
	remoteSbx := stubSbx(t)
	remote := startSecondDaemon(t, daemon, remoteSbx)
	sshDir := stubSSH(t, remote.sock, daemon)
	sock, _ := startDaemonWithPath(t, daemon, "e2e-host", fmt.Sprintf("/tmp/sb-e2e-%d.sock", os.Getpid()), "127.0.0.1:18765", localSbx, sshDir)
	if err := os.MkdirAll(filepath.Join(remote.workspaceRoot, "rproj"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := spawnTUIWithHosts(t, tui, sock, seedSource(t), t.TempDir(), sshDir)
	p.expect(t, "Switchboard", 20*time.Second)
	connectRemote(t, p)
	// A sandbox on the remote daemon.
	m := p.mark()
	p.send("n")
	p.expectNew(t, m, "Launch sandbox", 10*time.Second)
	m = p.mark()
	p.send("H")
	p.expectNew(t, m, "Create the sandbox on which host?", 10*time.Second)
	p.send("j")
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "rproj", 15*time.Second)
	p.send(" ")
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "@remote", 15*time.Second)
	createOrchestrator(t, p)
	addPingAction(t, p)
	m = p.mark()
	p.send("\x1b") // Jobs › esc: the sandbox list
	p.expectNew(t, m, "All Sandboxes", 10*time.Second)

	// Attach the remote sandbox (the only row), adding the host first.
	m = p.mark()
	p.send("o")
	p.expectNew(t, m, "will add this host", 10*time.Second)
	p.send("\r")
	p.expect(t, "Add host remote to review-bot?", 10*time.Second)
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "attached", 30*time.Second)
	re := regexp.MustCompile(`--url (http://127\.0\.0\.1:18766/orchestrator/w/[0-9a-f]+/mcp)`)
	var url string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && url == "" {
		for _, c := range sbxCalls(t, remoteSbx) {
			if mm := re.FindStringSubmatch(c); mm != nil {
				url = mm[1]
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if url == "" {
		t.Fatalf("no worker registration on the remote stub: %v", sbxCalls(t, remoteSbx))
	}

	// The worker's agent calls an action through its gateway: a run queued with
	// the sandbox as origin, delivered back to it (the only worker).
	code, body := mcpPost(t, url, "orchestrator_call_action", `{"action":"ping","arguments":{}}`)
	if code != http.StatusOK || !strings.Contains(body, `"state":"queued"`) {
		t.Fatalf("call_action = %d %s", code, body)
	}
	openJobs(t, p)
	m = p.mark()
	p.gotoView(t, "In progress")                      // the Overview (Jobs reopens on its last tab)
	p.expectNew(t, m, "triggered by", 20*time.Second) // the origin, cut at the PTY width
	p.expectNew(t, m, "running", 20*time.Second)
	deadline = time.Now().Add(20 * time.Second)
	var typed string
	for time.Now().Before(deadline) && typed == "" {
		files, _ := filepath.Glob(filepath.Join(remoteSbx, "agent-*.in"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "Reply with pong.") {
				typed = string(b)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(typed, `[switchboard orchestrator "review-bot" · run run-`) {
		t.Errorf("the remote agent should have received the framed prompt; typed:\n%s", typed)
	}
	// The completion report closes the run: it moves to history with the summary.
	code, body = mcpPost(t, url, "orchestrator_report_completion", `{"summary":"pong sent"}`)
	if code != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("report = %d %s", code, body)
	}
	p.expect(t, "pong sent", 20*time.Second) // the History row's result
	// The briefing block names the orchestrator and the four tools.
	var briefing string
	files, _ := filepath.Glob(filepath.Join(remote.workspaceRoot, "*", "CLAUDE.md"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			briefing += string(b)
		}
	}
	for _, want := range []string{"<!-- switchboard:orchestrator:begin -->", "## Orchestrator: review-bot", "orchestrator_list_actions", "orchestrator_call_action", "orchestrator_check_run", "orchestrator_report_completion", "<!-- switchboard:orchestrator:end -->"} {
		if !strings.Contains(briefing, want) {
			t.Errorf("CLAUDE.md lacks %q (files %v)", want, files)
		}
	}
	// Detach from the orchestrator's Workers view (Daemons › <local> ›
	// Orchestrators › review-bot): the token is revoked, the endpoint answers 404.
	m = p.mark()
	p.send("\x1b") // Jobs › esc: the sandbox list
	p.expectNew(t, m, "All Sandboxes", 10*time.Second)
	openDaemonOrchs(t, p, "1 worker")
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "Orchestrator review-bot", 10*time.Second)
	m = p.mark()
	p.send("x")
	p.expectNew(t, m, "Detach", 10*time.Second)
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "detached", 20*time.Second)
	deadline = time.Now().Add(10 * time.Second)
	code = 0
	for time.Now().Before(deadline) && code != http.StatusNotFound {
		code, _ = mcpPost(t, url, "orchestrator_list_actions", `{}`)
		time.Sleep(200 * time.Millisecond)
	}
	if code != http.StatusNotFound {
		t.Errorf("after detach the endpoint should answer 404, got %d", code)
	}
	p.send("q")
}

func TestTUIReasoningSandboxE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	p := spawnTUI(t, tui, sock, seedSource(t))
	p.expect(t, "Switchboard", 20*time.Second)
	createOrchestrator(t, p)

	// Creating the orchestrator created reasoning-<id8> on an empty workspace:
	// no seeded folder in the create argv.
	var created string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && created == "" {
		for _, c := range sbxCalls(t, sbxDir) {
			if strings.HasPrefix(c, "create ") && strings.Contains(c, "reasoning-") {
				created = c
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if created == "" || strings.Contains(created, "proj") {
		t.Fatalf("reasoning create = %q; calls: %v", created, sbxCalls(t, sbxDir))
	}
	// Absent from All, on the O tab marked reasoning.
	m := p.mark()
	p.send("\x1b[1;2D")
	p.expectNew(t, m, "All Sandboxes", 10*time.Second)
	p.expectNew(t, m, "No sandboxes", 10*time.Second)
	m = p.mark()
	p.send("O")
	p.expectNew(t, m, "Orchestrator Sandboxes", 10*time.Second)
	p.expectNew(t, m, "reasoning-", 10*time.Second)
	p.expectNew(t, m, "orchestrator: review-bot (reasoning)", 10*time.Second)

	// b rebuilds it: rm then a fresh create.
	openDaemonOrchs(t, p, "review-bot")
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "Orchestrator review-bot", 10*time.Second)
	p.gotoView(t, "default: empty context")
	before := len(sbxCalls(t, sbxDir))
	m = p.mark()
	p.send("b")
	p.expectNew(t, m, "Rebuild the reasoning sandbox of review-bot?", 10*time.Second)
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "reasoning sandbox rebuilt", 30*time.Second)
	var sawRm, sawCreate bool
	for _, c := range sbxCalls(t, sbxDir)[before:] {
		switch {
		case strings.HasPrefix(c, "rm ") && strings.Contains(c, "reasoning-"):
			sawRm = true
		case strings.HasPrefix(c, "create ") && strings.Contains(c, "reasoning-"):
			if !sawRm {
				t.Errorf("create must follow rm: %v", sbxCalls(t, sbxDir)[before:])
			}
			sawCreate = true
		}
	}
	if !sawRm || !sawCreate {
		t.Fatalf("rebuild should rm then create; calls: %v", sbxCalls(t, sbxDir)[before:])
	}

	// A reason action call is delivered to the reasoning sandbox; actions are
	// authored on the Jobs Actions tab (esc to the orchestrators, shift+→).
	m = p.mark()
	p.send("\x1b")
	p.expectNew(t, m, "0 workers", 10*time.Second)
	m = p.mark()
	p.send("\x1b[1;2C")
	p.expectNew(t, m, "Overview", 10*time.Second)
	p.gotoView(t, "no actions — press a to add one")
	m = p.mark()
	p.send("a")
	p.expectNew(t, m, "Body", 10*time.Second)
	p.send("think")
	p.tab()
	p.send("Think it over")
	p.tab()
	p.send("\x1b[B") // type: reason
	p.tab()
	p.send("Think hard about it.")
	p.tab()
	p.tab()
	m = p.mark()
	p.send("\x13")
	p.expectNew(t, m, "saved action think", 15*time.Second)
	p.expect(t, "(reason)", 10*time.Second)
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "No arguments", 10*time.Second)
	m = p.mark()
	p.send("\x13")
	p.expectNew(t, m, "queued run-", 15*time.Second)
	deadline = time.Now().Add(20 * time.Second)
	var typed string
	for time.Now().Before(deadline) && typed == "" {
		files, _ := filepath.Glob(filepath.Join(sbxDir, "agent-reasoning-*.in"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "Think hard about it.") {
				typed = string(b)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(typed, `[switchboard orchestrator "review-bot" · run run-`) || !strings.Contains(typed, `action "think"`) {
		t.Errorf("the reasoning sandbox should have received the framed reason prompt; typed:\n%s", typed)
	}
	p.send("q")
}
