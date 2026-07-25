package escapehatch

import (
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// An auto-run command's outcome is delivered back to the agent via the prompt func,
// with no terminal attached (SC-005). The fake prompt stands in for
// agent.Registry.Prompt, which writes to the PTY regardless of client attachment.
func TestAutoRunDeliversOutcomeToAgent(t *testing.T) {
	svc, prompts := serviceWith(t, t.TempDir(), shCmd("greet", "echo hello-world"))

	run, err := svc.Invoke("sb1", "greet")
	if err != nil {
		t.Fatal(err)
	}
	if run.GetStatus() != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING {
		t.Fatalf("auto-run should start RUNNING, got %v", run.GetStatus())
	}

	select {
	case msg := <-prompts:
		if !strings.Contains(msg, "greet") || !strings.Contains(msg, "succeeded") || !strings.Contains(msg, "hello-world") {
			t.Errorf("callback missing outcome/output: %q", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no result delivered to the agent")
	}
}

func TestNonZeroExitDeliveredAsFailure(t *testing.T) {
	svc, prompts := serviceWith(t, t.TempDir(), shCmd("boom", "echo nope 1>&2; exit 2"))
	if _, err := svc.Invoke("sb1", "boom"); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-prompts:
		if !strings.Contains(msg, "failed") || !strings.Contains(msg, "exit 2") || !strings.Contains(msg, "nope") {
			t.Errorf("failure callback missing detail: %q", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no failure result delivered to the agent")
	}
}
