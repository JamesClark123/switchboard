package editor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// script writes a stand-in editor into a temp dir and returns its path.
func script(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func shortGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := exitGrace
	exitGrace = d
	t.Cleanup(func() { exitGrace = old })
}

func waitReport(t *testing.T, s *Started) (ExitReport, bool) {
	t.Helper()
	select {
	case r, ok := <-s.Report:
		return r, ok
	case <-time.After(10 * time.Second):
		t.Fatal("no report and the channel never closed")
	}
	return ExitReport{}, false
}

func TestLaunchExitZeroClosesWithNoReport(t *testing.T) {
	s, err := NewLauncher().Launch([]string{script(t, "ok", "exit 0"), "/ws"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := waitReport(t, s); ok {
		t.Error("a zero exit must not be reported")
	}
}

func TestLaunchNonZeroExitIsReported(t *testing.T) {
	s, err := NewLauncher().Launch([]string{script(t, "three", "exit 3")})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := waitReport(t, s)
	if !ok || r.Status != 3 || r.Signal != "" {
		t.Errorf("report = %+v, %v; want status 3", r, ok)
	}
}

func TestLaunchSignalIsReported(t *testing.T) {
	s, err := NewLauncher().Launch([]string{script(t, "term", "kill -TERM $$")})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := waitReport(t, s)
	if !ok || r.Status != -1 || !strings.Contains(r.Signal, "terminated") {
		t.Errorf("report = %+v, %v; want a signal", r, ok)
	}
}

func TestLaunchStillRunningAfterGraceClosesAndDoesNotWait(t *testing.T) {
	shortGrace(t, 100*time.Millisecond)
	start := time.Now()
	s, err := NewLauncher().Launch([]string{script(t, "sleep", "sleep 30")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(s.pid, syscall.SIGKILL) })
	if _, ok := waitReport(t, s); ok {
		t.Error("a command still running after the grace must not be reported")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("Launch waited for the child")
	}
}

func TestLaunchMissingExecutable(t *testing.T) {
	_, err := NewLauncher().Launch([]string{filepath.Join(t.TempDir(), "missing"), "/ws"})
	if err == nil {
		t.Fatal("expected an error for a missing executable")
	}
	// The innermost OS reason is reachable through errors.Unwrap.
	inner := err
	for errors.Unwrap(inner) != nil {
		inner = errors.Unwrap(inner)
	}
	if !errors.Is(err, os.ErrNotExist) || inner.Error() == err.Error() {
		t.Errorf("err = %v (innermost %v)", err, inner)
	}
	if _, err := NewLauncher().Launch(nil); err == nil {
		t.Error("expected an error for an empty argv")
	}
}

func TestLaunchDetachedInOwnSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("session id is read from /proc on Linux")
	}
	shortGrace(t, 50*time.Millisecond)
	s, err := NewLauncher().Launch([]string{script(t, "sleep", "sleep 30")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(s.pid, syscall.SIGKILL) })
	b, err := os.ReadFile("/proc/" + strconv.Itoa(s.pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	// Fields after the parenthesised comm: state ppid pgrp session …
	rest := b[strings.LastIndex(string(b), ")")+2:]
	f := strings.Fields(string(rest))
	sid, _ := strconv.Atoi(f[3])
	pgrp, _ := strconv.Atoi(f[2])
	if sid != s.pid || pgrp != s.pid {
		t.Errorf("child session %d, group %d; want its own (%d)", sid, pgrp, s.pid)
	}
	if _, ok := waitReport(t, s); ok {
		t.Error("unexpected report")
	}
}

func TestExitReport(t *testing.T) {
	if _, failed := exitReport(nil); failed {
		t.Error("nil is no failure")
	}
	if r, failed := exitReport(errors.New("wait: no child")); !failed || r.Status != -1 {
		t.Errorf("non-exit error = %+v, %v", r, failed)
	}
}
