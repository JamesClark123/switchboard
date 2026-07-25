package escapehatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func approvalCmd(name, command string) *pb.EscapeHatchCommand {
	return &pb.EscapeHatchCommand{Name: name, Command: command, ConsentMode: pb.ConsentMode_CONSENT_MODE_REQUIRES_APPROVAL}
}

// waitStatus polls a run until it reaches want (or fails).
func waitStatus(t *testing.T, svc *Service, runID string, want pb.EscapeHatchRunStatus) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if run, ok := svc.runs.Get(runID); ok && run.GetStatus() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := svc.runs.Get(runID)
	t.Fatalf("run %s did not reach %v; last status %v", runID, want, got.GetStatus())
}

func TestApprovalRequiredDoesNotExecuteBeforeApproval(t *testing.T) {
	ws := t.TempDir()
	marker := filepath.Join(ws, "ran")
	svc, _ := serviceWith(t, ws, approvalCmd("deploy", "touch "+marker))

	run, err := svc.Invoke("sb1", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if run.GetStatus() != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL {
		t.Fatalf("status = %v, want PENDING_APPROVAL", run.GetStatus())
	}
	// Give the goroutine a moment; nothing should run before a decision.
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("command executed before approval — zero host execution is required (SC-003)")
	}
}

func TestApprovalProceedsOnApprove(t *testing.T) {
	ws := t.TempDir()
	marker := filepath.Join(ws, "ran")
	svc, prompts := serviceWith(t, ws, approvalCmd("deploy", "touch "+marker))

	run, _ := svc.Invoke("sb1", "deploy")
	// Wait until the run is registered as pending, then approve.
	time.Sleep(100 * time.Millisecond)
	if st := svc.Decide(run.GetId(), true); st == pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_UNSPECIFIED {
		t.Fatal("decide returned unspecified for a known run")
	}
	select {
	case <-prompts: // delivered on completion
	case <-time.After(10 * time.Second):
		t.Fatal("approved run did not complete")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("approved command should have run: %v", err)
	}
}

func TestApprovalDeniedDoesNotExecute(t *testing.T) {
	ws := t.TempDir()
	marker := filepath.Join(ws, "ran")
	svc, _ := serviceWith(t, ws, approvalCmd("deploy", "touch "+marker))

	run, _ := svc.Invoke("sb1", "deploy")
	time.Sleep(100 * time.Millisecond)
	svc.Decide(run.GetId(), false)
	waitStatus(t, svc, run.GetId(), pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("denied command must not run")
	}
}

func TestApprovalWindowElapsesToDenied(t *testing.T) {
	ws := t.TempDir()
	svc, _ := serviceWith(t, ws, approvalCmd("deploy", "true"))
	svc.SetApprovalWindow(50 * time.Millisecond)

	run, _ := svc.Invoke("sb1", "deploy")
	waitStatus(t, svc, run.GetId(), pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED)
}

func TestDuplicateDecisionIsNoOp(t *testing.T) {
	ws := t.TempDir()
	svc, _ := serviceWith(t, ws, approvalCmd("deploy", "true"))
	run, _ := svc.Invoke("sb1", "deploy")
	time.Sleep(100 * time.Millisecond)

	svc.Decide(run.GetId(), true)
	// A second decision after the run has been consumed is a harmless no-op.
	svc.Decide(run.GetId(), false)
	waitStatus(t, svc, run.GetId(), pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED)
}

func TestCancelPendingApprovalYieldsCancelled(t *testing.T) {
	ws := t.TempDir()
	svc, _ := serviceWith(t, ws, approvalCmd("deploy", "true"))
	run, _ := svc.Invoke("sb1", "deploy")
	time.Sleep(100 * time.Millisecond)
	// Sandbox stop cancels in-flight runs (including pending-approval ones).
	svc.Cancel("sb1")
	waitStatus(t, svc, run.GetId(), pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED)
}
