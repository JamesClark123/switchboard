//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// repoRoot resolves the monorepo root relative to this module's directory
// (src/apps/switchboard-tui-e2e → ../../..).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

// buildBinaries compiles the daemon + TUI from the workspace into a temp dir.
func buildBinaries(t *testing.T) (tui, daemon string) {
	t.Helper()
	root := repoRoot(t)
	out := t.TempDir()
	tui = filepath.Join(out, "sxb")
	daemon = filepath.Join(out, "sxbd")
	build := func(bin, pkg string) {
		cmd := exec.Command("go", "build", "-o", bin, pkg)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", pkg, err, b)
		}
	}
	build(daemon, "./src/services/switchboardd/cmd/sxbd")
	build(tui, "./src/apps/switchboard-tui/cmd/sxb")
	return tui, daemon
}

// stubSbx writes a fake `sbx` so launches succeed without Docker, and returns a
// PATH-prefix directory containing it.
func stubSbx(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/usr/bin/env bash
case "$1" in
  --version) echo "sbx-e2e 0.0" ;;
  create) echo "container-$3" ;;
  ls) echo '{"sandboxes":[]}' ;;   # IsRunning parses ls --json
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "sbx"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// startDaemon launches `sxbd serve` with a stub sbx and returns the
// socket path. The daemon is killed on test cleanup.
func startDaemon(t *testing.T, daemonBin, sbxDir string) string {
	t.Helper()
	dir := t.TempDir()
	// Keep the socket path well under the 108-char sun_path limit.
	sock := fmt.Sprintf("/tmp/sb-e2e-%d.sock", os.Getpid())
	_ = os.Remove(sock)

	cmd := exec.Command(daemonBin, "serve")
	cmd.Env = append(os.Environ(),
		"PATH="+sbxDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SWITCHBOARDD_SOCKET="+sock,
		"SWITCHBOARDD_WORKSPACE_ROOT="+filepath.Join(dir, "ws"),
		"SWITCHBOARDD_DATA_DIR="+filepath.Join(dir, "data"),
		// Isolate the PID file per test. It otherwise defaults to a GLOBAL
		// $XDG_RUNTIME_DIR/switchboard.pid, so the first test's daemon (SIGKILLed
		// on cleanup, skipping its deferred pidfile clear) would leave a stale
		// entry that makes the next test's daemon exit with "already running" and
		// never bind its socket.
		"SWITCHBOARDD_PID_FILE="+filepath.Join(dir, "data", "switchboard.pid"),
		"SWITCHBOARDD_SBX_BIN=sbx",
		"SWITCHBOARDD_HOST_ID=e2e-host",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = os.Remove(sock)
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		// Signal 0 probes liveness; if the daemon died during startup, fail fast.
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("daemon exited during startup: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("daemon socket never appeared")
	return ""
}

// ptyProcess is a process driven through a PTY.
type ptyProcess struct {
	ptmx *os.File
	cmd  *exec.Cmd
	buf  *syncBuffer
}

// spawnTUI starts the TUI under a PTY connected to the daemon socket, with srcRoot
// as the working directory (its children are the launch candidates).
func spawnTUI(t *testing.T, tuiBin, sock, srcRoot string) *ptyProcess {
	return spawnTUIWithConfig(t, tuiBin, sock, srcRoot, t.TempDir())
}

// spawnTUIWithConfig is spawnTUI with an explicit client config dir, so a test can
// inspect (or pre-seed) the client-side state the TUI writes — e.g. authored kits
// under <configDir>/kits/<id>/spec.yaml (feature 004).
func spawnTUIWithConfig(t *testing.T, tuiBin, sock, srcRoot, configDir string) *ptyProcess {
	t.Helper()
	cmd := exec.Command(tuiBin)
	cmd.Dir = srcRoot
	cmd.Env = append(os.Environ(),
		"SWITCHBOARD_LOCAL_SOCKET="+sock,
		"SWITCHBOARD_CONFIG_DIR="+configDir,
		"TERM=xterm-256color",
	)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Cols: 120, Rows: 40})

	buf := newSyncBuffer()
	go func() { _, _ = copyInto(buf, ptmx) }()

	p := &ptyProcess{ptmx: ptmx, cmd: cmd, buf: buf}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = ptmx.Close()
	})
	return p
}

func (p *ptyProcess) send(s string) { _, _ = p.ptmx.WriteString(s) }

// expect waits until the captured output contains sub, or fails after timeout.
//
// It searches the WHOLE cumulative buffer, so it matches instantly if sub was ever
// printed before. That is fine for a one-way flow, but when a step's expected text
// is also on the previous screen (e.g. a section title that stays put across an
// edit), the assertion passes on stale output and the test races ahead of the app.
// Use expectNew for those steps.
func (p *ptyProcess) expect(t *testing.T, sub string, timeout time.Duration) {
	t.Helper()
	p.expectFrom(t, 0, sub, timeout)
}

// mark returns the current output length, for use with expectNew.
func (p *ptyProcess) mark() int { return len(p.buf.String()) }

// expectNew waits for sub in output produced after mark. The TUI repaints the full
// screen on every update, so anything currently visible reappears in new output —
// which makes this a reliable "wait for the app to actually redraw" barrier rather
// than a match against what was already on screen.
func (p *ptyProcess) expectNew(t *testing.T, from int, sub string, timeout time.Duration) {
	t.Helper()
	p.expectFrom(t, from, sub, timeout)
}

func (p *ptyProcess) expectFrom(t *testing.T, from int, sub string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := p.buf.String()
		if from <= len(s) && strings.Contains(s[from:], sub) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected %q within %s (from offset %d).\n--- output ---\n%s", sub, timeout, from, p.buf.String())
}
