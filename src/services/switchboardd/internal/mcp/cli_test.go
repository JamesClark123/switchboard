package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runBounded kills the child's whole process group on the deadline and returns
// only after the child has exited (research R7).
func TestRunBoundedReapsBeforeReturn(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := filepath.Join(dir, "sbx")
	// The script backgrounds a grandchild in the same group, records its pid, and hangs.
	script := "#!/usr/bin/env bash\nsleep 60 &\necho $! > " + pidFile + "\necho started\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := (&CLI{Bin: bin}).runBounded(ctx, "mcp", "add", "x")
	if err == nil || !strings.Contains(out, "started") {
		t.Fatalf("runBounded = %q, %v", out, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("runBounded took %v", time.Since(start))
	}
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid == 0 {
		t.Fatal("no grandchild pid")
	}
	// The grandchild was in the killed group: it is gone (or a zombie being reaped).
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		if st, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); !strings.Contains(string(st), ") Z") {
			t.Errorf("grandchild %d survived the group kill", pid)
		}
	}
}

func TestRunBoundedSuccessAndFailure(t *testing.T) {
	bin, _ := scriptedSbx(t, `
  "ok") echo fine ;;
  "bad") echo "nope here" >&2; exit 3 ;;`)
	c := &CLI{Bin: bin}
	if out, err := c.runBounded(context.Background(), "ok"); err != nil || strings.TrimSpace(out) != "fine" {
		t.Errorf("ok = %q, %v", out, err)
	}
	if out, err := c.runBounded(context.Background(), "bad"); err == nil || !strings.Contains(err.Error(), "nope here") || !strings.Contains(out, "nope") {
		t.Errorf("bad = %q, %v", out, err)
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
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return bin, pidFile
}

func helperPid(t *testing.T, pidFile string) int {
	t.Helper()
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid == 0 {
		t.Fatal("no helper pid")
	}
	return pid
}

// cancelOnceStarted cancels once the stub has recorded its helper, so the cancel
// can't land before the helper exists, and sends the time of the cancel.
func cancelOnceStarted(pidFile string, cancel context.CancelFunc) <-chan time.Time {
	cancelled := make(chan time.Time, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(pidFile); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancelled <- time.Now()
		cancel()
	}()
	return cancelled
}

// SWI-26 (review run-1528): cancelling a load ends it promptly and kills its
// child, which would otherwise keep the call, and the runtime hold, alive.
func TestLoadCancelKillsItsChild(t *testing.T) {
	bin, pidFile := holdingSbx(t, "sleep 60", "wait")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := cancelOnceStarted(pidFile, cancel)
	if _, err := (&CLI{Bin: bin}).Load(ctx, "srv", "sb"); err == nil {
		t.Fatal("a cancelled load should fail")
	}
	if took := time.Since(<-cancelled); took >= 2*time.Second {
		t.Errorf("the cancelled load took %v, so it waited on its child", took)
	}
	pid := helperPid(t, pidFile)
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		if st, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); !strings.Contains(string(st), ") Z") {
			t.Errorf("child %d survived the cancel", pid)
		}
	}
}

// A helper outside the group can't hold a cancelled load past the wait delay.
func TestLoadCancelDoesNotWaitOnAnEscapedHelper(t *testing.T) {
	bin, pidFile := holdingSbx(t, "setsid sleep 60", "wait")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := cancelOnceStarted(pidFile, cancel)
	if _, err := (&CLI{Bin: bin}).Load(ctx, "srv", "sb"); err == nil {
		t.Fatal("a cancelled load should fail")
	}
	if took := time.Since(<-cancelled); took > 5*time.Second {
		t.Errorf("the cancelled load took %v", took)
	}
}

// An `sbx` that exited 0 has succeeded, whatever it left holding its output.
func TestLoadExitingZeroBesideAHelperSucceeds(t *testing.T) {
	bin, _ := holdingSbx(t, "sleep 30", "exit 0")
	start := time.Now()
	out, err := (&CLI{Bin: bin}).Load(context.Background(), "srv", "sb")
	if err != nil || !strings.Contains(out, "started") {
		t.Fatalf("load = %q, %v", out, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the load took %v", took)
	}
}

// SWI-26 (review run-1545): that holds if the context ends during the wait on the
// helper: nothing is left to cancel, and the load has happened.
func TestLoadExitingZeroThenCancelledSucceeds(t *testing.T) {
	sbxPid := filepath.Join(t.TempDir(), "sbx.pid")
	bin, _ := holdingSbx(t, "sleep 30", "echo $$ > "+sbxPid+".tmp\nmv "+sbxPid+".tmp "+sbxPid+"\nexit 0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer cancel()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(sbxPid); err == nil {
				if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 && syscall.Kill(pid, 0) != nil {
					// Reaped: give Wait a moment to move on to the wait for the pipes.
					time.Sleep(100 * time.Millisecond)
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if _, err := (&CLI{Bin: bin}).Load(ctx, "srv", "sb"); err != nil {
		t.Errorf("load = %v", err)
	}
	if ctx.Err() == nil {
		t.Error("the cancel should have landed during the wait")
	}
}
