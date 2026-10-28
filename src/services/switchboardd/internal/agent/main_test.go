package agent

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as a fake agent for the probe and reap script tests
// (orphan_test.go): run as `claude --dangerously-skip-permissions …` with
// SXB_FAKE_AGENT set, the test binary just sleeps. "deaf" ignores TERM;
// "nested" first starts a nested `claude --dangerously-skip-permissions -p`
// of itself, as a skill's `claude -p` would.
func TestMain(m *testing.M) {
	switch os.Getenv("SXB_FAKE_AGENT") {
	case "":
		os.Exit(m.Run())
	case "deaf":
		signal.Ignore(syscall.SIGTERM)
	case "nested":
		self, _ := os.Executable()
		child := exec.Command(self, "--dangerously-skip-permissions", "-p")
		child.Args[0] = "claude"
		child.Env = append(os.Environ(), "SXB_FAKE_AGENT=plain")
		_ = child.Start()
		defer func() { _ = child.Process.Kill() }()
	}
	time.Sleep(time.Minute)
}
