package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// loggingSbx writes a stub `sbx` that records every argv line to <dir>/sbx.log.
func loggingSbx(t *testing.T) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "sbx")
	logPath = filepath.Join(dir, "sbx.log")
	script := "#!/usr/bin/env bash\necho \"$*\" >> \"" + logPath + "\"\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

// Feature 008: the exclusive attach mode rides `sbx create` as one --static-mcp
// flag after the kit flags; the additive mode uses the documented `mcp load`.
func TestSbxRunnerStaticMcpAndLoadArgv(t *testing.T) {
	bin, logPath := loggingSbx(t)
	r := &SbxRunner{Bin: bin}
	if _, err := r.Launch(context.Background(), LaunchSpec{Name: "sb", WorkspacePath: "/ws", KitSources: []string{"/k"}, StaticMcp: []string{"notion", "linear"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadMcp(context.Background(), "sb", "notion", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("argv log = %q", lines)
	}
	if lines[0] != "create --name sb claude /ws --kit /k --static-mcp notion,linear" {
		t.Errorf("create argv = %q", lines[0])
	}
	if lines[1] != "mcp load notion --sandbox sb" {
		t.Errorf("load argv = %q", lines[1])
	}
	// No static set => no flag at all.
	if _, err := r.Launch(context.Background(), LaunchSpec{Name: "sb2", WorkspacePath: "/ws"}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(logPath)
	if strings.Contains(strings.Split(strings.TrimSpace(string(b)), "\n")[2], "static-mcp") {
		t.Error("an empty StaticMcp must not render --static-mcp")
	}
}

// SWI-3: detaching rides the documented `mcp unload <name> --sandbox <ref>`.
func TestSbxRunnerUnloadMcpArgv(t *testing.T) {
	bin, logPath := loggingSbx(t)
	r := &SbxRunner{Bin: bin}
	if err := r.UnloadMcp(context.Background(), "sb", "notion", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	if got := strings.TrimSpace(string(b)); got != "mcp unload notion --sandbox sb" {
		t.Errorf("unload argv = %q", got)
	}
}

// holdingSbx writes a stub `sbx` that leaves a helper holding its output, records
// the helper's pid and then runs rest. setsid escapes sbx's process group.
func holdingSbx(t *testing.T, helper, rest string) (bin, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "sbx")
	pidFile = filepath.Join(dir, "helper.pid")
	script := "#!/usr/bin/env bash\n" + helper + " &\necho $! > " + pidFile + ".tmp\nmv " + pidFile + ".tmp " + pidFile + "\necho started\n" + rest + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pid := helperPid(pidFile); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return bin, pidFile
}

// helperPid is the pid holdingSbx's stub recorded, or 0 before it has.
func helperPid(pidFile string) int {
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// cancelOnceStarted cancels once the stub has recorded its helper, so the cancel
// can't land before the helper exists, and sends the time of the cancel.
func cancelOnceStarted(pidFile string, cancel context.CancelFunc) <-chan time.Time {
	cancelled := make(chan time.Time, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for helperPid(pidFile) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		cancelled <- time.Now()
		cancel()
	}()
	return cancelled
}

// waitHelperGone fails unless the process is gone (or a zombie being reaped).
func waitHelperGone(t *testing.T, pid int) {
	t.Helper()
	if pid == 0 {
		t.Fatal("no helper pid") // Kill(0, 0) would test this test's own group
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		if st, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); !strings.Contains(string(st), ") Z") {
			t.Errorf("helper %d survived the cancel", pid)
		}
	}
}

// SWI-26 (review run-1528): cancelling an `sbx mcp` call ends it promptly and
// kills its child, which would otherwise keep the call, and the runtime hold, alive.
func TestSbxRunnerMcpCancelKillsItsChild(t *testing.T) {
	calls := map[string]func(r *SbxRunner, ctx context.Context) error{
		"load":   func(r *SbxRunner, ctx context.Context) error { return r.LoadMcp(ctx, "sb", "srv", nil) },
		"unload": func(r *SbxRunner, ctx context.Context) error { return r.UnloadMcp(ctx, "sb", "srv", nil) },
	}
	for kind, call := range calls {
		t.Run(kind, func(t *testing.T) {
			bin, pidFile := holdingSbx(t, "sleep 60", "wait")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cancelled := cancelOnceStarted(pidFile, cancel)
			if err := call(&SbxRunner{Bin: bin}, ctx); err == nil {
				t.Fatal("a cancelled call should fail")
			}
			if took := time.Since(<-cancelled); took >= heldCallWaitDelay {
				t.Errorf("the cancelled call took %v", took)
			}
			waitHelperGone(t, helperPid(pidFile))
		})
	}
}

// A helper outside the group can't hold a cancelled call past the wait delay.
func TestSbxRunnerMcpCancelDoesNotWaitOnAnEscapedHelper(t *testing.T) {
	bin, pidFile := holdingSbx(t, "setsid sleep 60", "wait")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := cancelOnceStarted(pidFile, cancel)
	if err := (&SbxRunner{Bin: bin}).LoadMcp(ctx, "sb", "srv", nil); err == nil {
		t.Fatal("a cancelled call should fail")
	}
	if took := time.Since(<-cancelled); took > heldCallWaitDelay+3*time.Second {
		t.Errorf("the cancelled call took %v", took)
	}
}

// An `sbx` that exited 0 has succeeded, whatever it left holding its output, and
// one that failed has failed.
func TestSbxRunnerMcpExitBesideAHelper(t *testing.T) {
	bin, _ := holdingSbx(t, "sleep 30", "exit 0")
	var lines []string
	if err := (&SbxRunner{Bin: bin}).LoadMcp(context.Background(), "sb", "srv", func(l string) { lines = append(lines, l) }); err != nil {
		t.Errorf("exit 0 = %v", err)
	}
	if len(lines) != 1 || lines[0] != "started" {
		t.Errorf("logged %q", lines)
	}
	bin, _ = holdingSbx(t, "sleep 30", "exit 3")
	var ee interface{ ExitCode() int }
	if err := (&SbxRunner{Bin: bin}).LoadMcp(context.Background(), "sb", "srv", nil); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Errorf("exit 3 = %v", err)
	}
}

// SWI-26 (review run-1545): an exit 0 holds if the context ends during the wait on
// the helper: nothing is left to cancel, and the load has happened.
func TestSbxRunnerMcpExitThenCancelledSucceeds(t *testing.T) {
	sbxPid := filepath.Join(t.TempDir(), "sbx.pid")
	bin, _ := holdingSbx(t, "sleep 30", "echo $$ > "+sbxPid+".tmp\nmv "+sbxPid+".tmp "+sbxPid+"\nexit 0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer cancel()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if pid := helperPid(sbxPid); pid > 0 && syscall.Kill(pid, 0) != nil {
				// Reaped: give Wait a moment to move on to the wait for the pipes.
				time.Sleep(100 * time.Millisecond)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := (&SbxRunner{Bin: bin}).LoadMcp(ctx, "sb", "srv", nil); err != nil {
		t.Errorf("load = %v", err)
	}
	if ctx.Err() == nil {
		t.Error("the cancel should have landed during the wait")
	}
}
