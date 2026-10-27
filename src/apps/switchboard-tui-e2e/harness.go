//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// PATH-prefix directory containing it. Every invocation is appended to
// <dir>/sbx.log (one argv per line) so a test can assert which lifecycle
// commands ran — e.g. that a source edit never stopped the sandbox (feature 007).
func stubSbx(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/usr/bin/env bash
echo "$*" >> "` + filepath.Join(dir, "sbx.log") + `"
case "$1" in
  version) echo "sbx version: v0.46.0 0411f50ee4700fe7bd37e6e7e3aced563e850ca9" ;;   # real format; >= sbxkit.MinSbxVersion so kit/gateway gates pass
  # Sandbox state lives in <dir>/sbx-state/<name> ("running"|"stopped") so ls
  # answers like the real runtime: the daemon reconciles every list against it
  # (SWI-13), and an always-empty listing would read as "everything stopped".
  create) mkdir -p "` + dir + `/sbx-state"; echo running > "` + dir + `/sbx-state/$3"; echo "container-$3" ;;
  stop) if [ -f "` + dir + `/sbx-state/$2" ]; then echo stopped > "` + dir + `/sbx-state/$2"; fi ;;
  rm) rm -f "` + dir + `/sbx-state/$2" ;;
  ls)
    printf '{"sandboxes":['; sep=''
    for f in "` + dir + `"/sbx-state/*; do
      [ -f "$f" ] || continue
      printf '%s{"name":"%s","id":"%s","status":"%s"}' "$sep" "$(basename "$f")" "$(basename "$f")" "$(cat "$f")"; sep=','
    done
    echo ']}' ;;
  exec)
    # feature 009: the daemon's agent PTY runs "sbx exec -it <ref> bash -lc ...";
    # the stub agent copies everything typed into it to <dir>/agent-<ref>.in so a
    # test can assert what a delivered run looked like. Like Claude Code, it fires
    # the SessionStart hook injected into its workspace once it is up (SWI-15:
    # nothing is typed into an agent before that; host.docker.internal is the
    # host's loopback from a container). "sbx exec <ref> true"
    # (resume) starts the sandbox.
    if [ "$2" = "-it" ]; then
      wd=$(printf '%s' "$6" | sed -n "s/^cd '\([^']*\)'.*/\1/p")
      hook=$(grep -o "http://[^'?]*?event=SessionStart[^']*" "$wd/.claude/settings.local.json" 2>/dev/null | head -1 | sed -e 's/\\u0026/\&/g' -e 's#//host.docker.internal:#//127.0.0.1:#')
      if [ -n "$hook" ]; then
        ( sleep 0.3; curl -s -m 2 -o /dev/null -X POST -H 'Content-Type: application/json' -d '{"hook_event_name":"SessionStart","source":"startup"}' "$hook" ) &
      fi
      cat > "` + dir + `/agent-$3.in"
    elif [ -f "` + dir + `/sbx-state/$2" ]; then echo running > "` + dir + `/sbx-state/$2"; fi ;;
  mcp)
    # feature 008: one registered remote server; add/rm/load/auth succeed.
    case "$2 $3" in
      "ls ") printf 'NAME     TRANSPORT     STATUS       GATEWAY                 DETAIL\nnotion   remote http   needs auth   LOCAL, managed by you   sbx mcp auth notion\n' ;;
      "auth status") echo '[{"server_name":"notion","status":"unauthorized"}]' ;;
      "load "*) echo "MCP server \"$3\" loaded into sandbox \"$5\" (live)" ;;   # real success line (2026-10-12)
      "rm "*) case "$*" in *--force*) printf 'No local OAuth token found for MCP server "%s"\nMCP server "%s" removed\n' "$3" "$3" ;;
                          *) printf 'Remove MCP server "%s" and its OAuth credential? (y/N): ' "$3"; exit 1 ;; esac ;;
      *) exit 0 ;;
    esac ;;
  secret)
    # feature 008 (FR-104): the documented secret-store table; set reads the value from stdin.
    case "$2" in
      ls) printf 'SCOPE      TYPE      NAME        SECRET\n(global)   service   github      gho_GCaw4o****...****43qy\n' ;;
      set) cat > "$(dirname "$0")/secret.stdin"; echo "Secret stored." ;;
      rm) echo "removed" ;;
      *) exit 0 ;;
    esac ;;
  kit)
    # feature 004/008: kit add recreates the container under the same name (real lines, 2026-10-12).
    case "$2" in
      add) echo "Recreating sandbox \"$3\" to apply augmented kit list..."; echo "Kit \"$(basename "$4")\" added to sandbox \"$3\"" ;;
      *) exit 0 ;;
    esac ;;
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
	sock, _ := startDaemonAt(t, daemonBin, sbxDir)
	return sock
}

// startDaemonAt is startDaemon plus the daemon's controlled workspace root, so a
// test can inspect what a sandbox's workspace copy actually holds on disk.
func startDaemonAt(t *testing.T, daemonBin, sbxDir string) (sock, workspaceRoot string) {
	// The hook HTTP port is fixed per daemon; the second daemon (feature 009)
	// takes the next one so both can run on one machine.
	return startDaemonWith(t, daemonBin, sbxDir, "e2e-host", fmt.Sprintf("/tmp/sb-e2e-%d.sock", os.Getpid()), "127.0.0.1:18765")
}

// secondDaemon is the result of startSecondDaemon: everything a test needs to
// reach the "remote" daemon directly.
type secondDaemon struct {
	sock, workspaceRoot, dataDir, hookAddr string
}

// startSecondDaemon starts a second `sxbd` on its own socket, data dir and
// workspace root, advertising host id "remote" (feature 009, research R16). It
// stands in for the worker machine: the TUI reaches it through stubSSH.
func startSecondDaemon(t *testing.T, daemonBin, sbxDir string) secondDaemon {
	t.Helper()
	sock := fmt.Sprintf("/tmp/sb-e2e-r-%d.sock", os.Getpid())
	s, ws := startDaemonWith(t, daemonBin, sbxDir, "remote", sock, "127.0.0.1:18766")
	return secondDaemon{sock: s, workspaceRoot: ws, dataDir: filepath.Join(filepath.Dir(ws), "data"), hookAddr: "127.0.0.1:18766"}
}

// startDaemonWith starts one daemon with an explicit host id, socket and hook
// address.
func startDaemonWith(t *testing.T, daemonBin, sbxDir, hostID, sock, hookAddr string) (string, string) {
	t.Helper()
	return startDaemonWithPath(t, daemonBin, hostID, sock, hookAddr, sbxDir)
}

// startDaemonWithPath is startDaemonWith with every PATH-prefix directory the
// daemon needs (the stub sbx, and the stub ssh when it must reach a second
// daemon as an orchestrator host).
func startDaemonWithPath(t *testing.T, daemonBin, hostID, sock, hookAddr string, pathDirs ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	workspaceRoot := filepath.Join(dir, "ws")
	// Keep the socket path well under the 108-char sun_path limit.
	_ = os.Remove(sock)

	cmd := exec.Command(daemonBin, "serve")
	cmd.Env = append(os.Environ(),
		"PATH="+strings.Join(pathDirs, string(os.PathListSeparator))+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SWITCHBOARDD_SOCKET="+sock,
		"SWITCHBOARDD_WORKSPACE_ROOT="+workspaceRoot,
		"SWITCHBOARDD_DATA_DIR="+filepath.Join(dir, "data"),
		// Isolate the PID file per test. It otherwise defaults to a GLOBAL
		// $XDG_RUNTIME_DIR/switchboard.pid, so the first test's daemon (SIGKILLed
		// on cleanup, skipping its deferred pidfile clear) would leave a stale
		// entry that makes the next test's daemon exit with "already running" and
		// never bind its socket.
		"SWITCHBOARDD_PID_FILE="+filepath.Join(dir, "data", "switchboard.pid"),
		"SWITCHBOARDD_SBX_BIN=sbx",
		"SWITCHBOARDD_HOST_ID="+hostID,
		"SWITCHBOARDD_HOOK_ADDR="+hookAddr,
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
			return sock, workspaceRoot
		}
		// Signal 0 probes liveness; if the daemon died during startup, fail fast.
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("daemon exited during startup: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("daemon socket never appeared")
	return "", ""
}

// stubSSH writes a fake `ssh` onto a PATH-prefix directory. It ignores every
// argument and execs the second daemon's `sxbd dial-stdio` against remoteSock,
// so the orchestrator's daemon (and the TUI) reach the "remote" daemon through
// the real SSH code path without a network (feature 009, research R16). The
// returned directory also receives a copy of the stub sbx when sbxDir is
// given, so one PATH prefix serves both.
func stubSSH(t *testing.T, remoteSock, daemonBin string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/usr/bin/env bash\n# stub ssh: every target is the second daemon\nexport SWITCHBOARDD_SOCKET=" + remoteSock + "\nexec " + daemonBin + " dial-stdio\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// spawnTUIWithHosts is spawnTUIWithConfig with hosts.toml pre-seeded with the
// "remote" host (an ssh entry whose target the stub ssh ignores) and the stub
// ssh on the TUI's PATH, so the daemon screen can connect it.
func spawnTUIWithHosts(t *testing.T, tuiBin, sock, srcRoot, configDir, sshDir string) *ptyProcess {
	t.Helper()
	hosts := "[[hosts]]\nid = \"local\"\ndisplay_name = \"localhost\"\nkind = \"local\"\nsocket_path = \"" + sock + "\"\n\n" +
		"[[hosts]]\nid = \"remote\"\ndisplay_name = \"remote\"\nkind = \"ssh\"\nssh_target = \"remote\"\n"
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "hosts.toml"), []byte(hosts), 0o644); err != nil {
		t.Fatal(err)
	}
	// Auto sign-in is on by default: the TUI dials the remote host at startup
	// through the stub ssh (key/agent auth, which the stub accepts).
	return spawnTUIWithEnv(t, tuiBin, sock, srcRoot, configDir, []string{"PATH=" + sshDir + string(os.PathListSeparator) + os.Getenv("PATH")})
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
	return spawnTUIWithEnv(t, tuiBin, sock, srcRoot, configDir, nil)
}

// spawnTUIWithEnv is spawnTUIWithConfig with extra environment entries (a PATH
// carrying the stub ssh, feature 009).
func spawnTUIWithEnv(t *testing.T, tuiBin, sock, srcRoot, configDir string, extra []string) *ptyProcess {
	t.Helper()
	cmd := exec.Command(tuiBin)
	cmd.Dir = srcRoot
	cmd.Env = append(os.Environ(),
		"SWITCHBOARD_LOCAL_SOCKET="+sock,
		"SWITCHBOARD_CONFIG_DIR="+configDir,
		"TERM=xterm-256color",
	)
	cmd.Env = append(cmd.Env, extra...)
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

// tab moves a huh form to its next field and lets the focus change land before
// the next keystrokes: huh switches fields through a command, so runes sent in
// the same instant as the tab would still reach the previous field.
func (p *ptyProcess) tab() {
	p.send("\t")
	time.Sleep(300 * time.Millisecond)
}

// tail returns the end of the captured output with escape sequences removed,
// for failure messages that need the last screen.
func (p *ptyProcess) tail(n int) string {
	s := ansiRe.ReplaceAllString(p.buf.String(), "")
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][B0]`)

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

// expectRow waits for a line matching re (styling stripped) in output produced
// after mark: a row's columns, which a plain substring cannot match across the
// colour escapes between them.
func (p *ptyProcess) expectRow(t *testing.T, from int, re string, timeout time.Duration) {
	t.Helper()
	rx := regexp.MustCompile(re)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s := p.buf.String(); from <= len(s) && rx.MatchString(ansiRe.ReplaceAllString(s[from:], "")) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected a row matching %q within %s.\n--- output ---\n%s", re, timeout, p.tail(3000))
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
