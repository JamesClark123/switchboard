package escapehatch

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// TestExecutorPassesArgsAsPositional confirms agent arguments reach the fixed
// command as separate positional parameters (e.g. `pnpm install build`).
func TestExecutorPassesArgsAsPositional(t *testing.T) {
	out := NewExecutor().Run(context.Background(), t.TempDir(),
		shCmd("echo", "echo"), "", []string{"install", "build"})
	if out.Status != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED {
		t.Fatalf("status = %v (%q)", out.Status, out.Output)
	}
	if strings.TrimSpace(out.Output) != "install build" {
		t.Errorf("output = %q, want \"install build\"", out.Output)
	}
}

// TestExecutorArgsAreNotShellInjected is the core safety guarantee: a metacharacter
// in an argument is an inert positional value, never re-parsed as shell syntax. The
// argument tries to `; touch pwned` — if it were concatenated into the shell string
// the marker would be created; passed as a positional param to `true`, it is not.
func TestExecutorArgsAreNotShellInjected(t *testing.T) {
	ws := t.TempDir()
	marker := filepath.Join(ws, "pwned")
	out := NewExecutor().Run(context.Background(), ws,
		shCmd("true", "true"), "", []string{";", "touch", marker})
	if out.Status != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED {
		t.Fatalf("status = %v (%q)", out.Status, out.Output)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("shell injection succeeded: the argument was re-parsed as a command")
	}
}

// TestExecutorRunsInSelectedWorkspace confirms the resolved workspace-relative dir
// (the agent's --workspace choice, already validated) is where the command runs.
func TestExecutorRunsInSelectedWorkspace(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "src", "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := NewExecutor().Run(context.Background(), ws,
		shCmd("pwd", "pwd"), "src/apps/web", nil)
	if !strings.Contains(out.Output, filepath.Join("src", "apps", "web")) {
		t.Errorf("did not run in the selected workspace: %q", out.Output)
	}
}

func TestShellArgs(t *testing.T) {
	if got := shellArgs("pnpm", nil); !reflect.DeepEqual(got, []string{"-c", "pnpm"}) {
		t.Errorf("no-arg shellArgs = %v", got)
	}
	got := shellArgs("pnpm", []string{"install", "--frozen"})
	want := []string{"-c", `pnpm "$@"`, "sh", "install", "--frozen"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shellArgs = %v, want %v", got, want)
	}
}
