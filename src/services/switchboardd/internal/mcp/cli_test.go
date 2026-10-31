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
