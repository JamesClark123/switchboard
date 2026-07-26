package escapehatch

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func ehArgsCmd() *pb.EscapeHatchCommand {
	return &pb.EscapeHatchCommand{
		Name: "greet", Command: "echo", WhenToUse: "x",
		ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN,
		Subcommands: []string{"hello"},
		Workspaces:  []string{"src/apps/*"},
	}
}

func TestHandleRunWithArgsAndWorkspace(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "src", "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc, _ := serviceWith(t, ws, ehArgsCmd())
	rec := postRun(t, svc, `{"sandbox_id":"sb1","name":"greet","workspace":"src/apps/web","args":"hello"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	run := waitTerminal(t, svc, "sb1")
	if strings.TrimSpace(run.GetOutput()) != "hello" {
		t.Errorf("output = %q, want \"hello\" (args reached the command)", run.GetOutput())
	}
	if run.GetArgs() != "hello" || run.GetWorkingDir() != "src/apps/web" {
		t.Errorf("run records args=%q dir=%q, want hello / src/apps/web", run.GetArgs(), run.GetWorkingDir())
	}
}

func TestHandleRunRejectsDisallowedArgsWith400(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "src", "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc, _ := serviceWith(t, ws, ehArgsCmd())
	rec := postRun(t, svc, `{"sandbox_id":"sb1","name":"greet","workspace":"src/apps/web","args":"publish"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("disallowed subcommand should be 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(svc.ListRuns("sb1")) != 0 {
		t.Error("a rejected invocation must run nothing")
	}
}

func TestHandleRunRejectsDisallowedWorkspaceWith400(t *testing.T) {
	svc, _ := serviceWith(t, t.TempDir(), ehArgsCmd())
	rec := postRun(t, svc, `{"sandbox_id":"sb1","name":"greet","workspace":"src/services/api","args":"hello"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("disallowed workspace should be 400, got %d", rec.Code)
	}
	if len(svc.ListRuns("sb1")) != 0 {
		t.Error("a rejected invocation must run nothing")
	}
}
