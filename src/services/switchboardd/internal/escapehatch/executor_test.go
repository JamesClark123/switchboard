package escapehatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func shCmd(name, command string) *pb.EscapeHatchCommand {
	return &pb.EscapeHatchCommand{Name: name, Command: command, ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN}
}

func TestExecutorRunsInWorkspace(t *testing.T) {
	ws := t.TempDir()
	out := NewExecutor().Run(context.Background(), ws, shCmd("pwd", "pwd"))
	if out.Status != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED {
		t.Fatalf("status = %v, want SUCCEEDED (output: %q)", out.Status, out.Output)
	}
	// macOS resolves /var -> /private/var; compare the base.
	if !strings.Contains(out.Output, filepath.Base(ws)) {
		t.Errorf("command did not run in the workspace dir: %q (ws %q)", out.Output, ws)
	}
}

func TestExecutorHonoursRelativeWorkingDir(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := shCmd("pwd", "pwd")
	cmd.WorkingDir = "sub/dir"
	out := NewExecutor().Run(context.Background(), ws, cmd)
	if !strings.Contains(out.Output, filepath.Join("sub", "dir")) {
		t.Errorf("did not honour working_dir: %q", out.Output)
	}
}

func TestExecutorCapturesStdoutAndStderr(t *testing.T) {
	out := NewExecutor().Run(context.Background(), t.TempDir(), shCmd("x", "echo out; echo err 1>&2"))
	if !strings.Contains(out.Output, "out") || !strings.Contains(out.Output, "err") {
		t.Errorf("should capture both stdout and stderr: %q", out.Output)
	}
}

func TestExecutorReportsNonZeroExit(t *testing.T) {
	out := NewExecutor().Run(context.Background(), t.TempDir(), shCmd("x", "echo boom; exit 3"))
	if out.Status != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED {
		t.Fatalf("status = %v, want FAILED", out.Status)
	}
	if out.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", out.ExitCode)
	}
	if !strings.Contains(out.Output, "boom") {
		t.Errorf("failed run should still carry output: %q", out.Output)
	}
}

func TestExecutorReportsLaunchFailure(t *testing.T) {
	// A working_dir that does not exist makes the process fail to start.
	cmd := shCmd("x", "true")
	out := NewExecutor().Run(context.Background(), "/no/such/workspace/at/all", cmd)
	if out.Status != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED {
		t.Fatalf("status = %v, want FAILED for a launch failure", out.Status)
	}
	if out.Output == "" {
		t.Error("launch failure should carry diagnostics, not be silently dropped")
	}
}

func TestResolveWorkdirRejectsEscape(t *testing.T) {
	ws := t.TempDir()
	if _, err := resolveWorkdir(ws, "../../etc"); err == nil {
		t.Error("expected an error for a working_dir that escapes the workspace")
	}
	if _, err := resolveWorkdir(ws, "/etc"); err == nil {
		t.Error("expected an error for an absolute working_dir")
	}
	if _, err := resolveWorkdir(ws, "sub/ok"); err != nil {
		t.Errorf("nested relative dir should be allowed: %v", err)
	}
}
