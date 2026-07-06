package grpc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/daemonctl"
)

// TestDefaultRestartClearsPidThenExecs verifies the re-exec bookkeeping: the pid
// file is cleared (so the re-exec'd serve is not blocked by its own live pid)
// and the process image is replaced via execSelf with argv[0] = the executable.
func TestDefaultRestartClearsPidThenExecs(t *testing.T) {
	dir := t.TempDir()
	pid := filepath.Join(dir, "sxbd.pid")
	if err := daemonctl.WritePID(pid, os.Getpid()); err != nil {
		t.Fatal(err)
	}

	var gotArgv0 string
	var gotArgv []string
	origExec, origDelay := execSelf, restartDelay
	execSelf = func(argv0 string, argv []string, _ []string) error {
		gotArgv0, gotArgv = argv0, argv
		return nil
	}
	restartDelay = 0
	defer func() { execSelf = origExec; restartDelay = origDelay }()

	if err := defaultRestart(pid); err != nil {
		t.Fatalf("defaultRestart: %v", err)
	}
	if gotArgv0 == "" || len(gotArgv) == 0 || gotArgv[0] != gotArgv0 {
		t.Errorf("execSelf argv0=%q argv=%v", gotArgv0, gotArgv)
	}
	if _, err := os.Stat(pid); !os.IsNotExist(err) {
		t.Error("pid file should be cleared before re-exec")
	}
}
