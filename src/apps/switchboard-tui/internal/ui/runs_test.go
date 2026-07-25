package ui

import (
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The E key opens the run-review screen and loads the sandbox's runs.
func TestEscapeHatchRunsScreenLoadsRuns(t *testing.T) {
	d := &fakeDaemon{
		runs: []*pb.EscapeHatchRun{
			{Id: "ehr-1", SandboxId: "sb1", CommandName: "install-deps", Command: "pnpm install", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED, ExitStatus: 0, Output: "added 42 packages"},
		},
	}
	m := runningSbx(d)
	out, cmd := update(m, press("E"))
	if out.screen != screenRuns {
		t.Fatalf("screen = %v, want screenRuns", out.screen)
	}
	// The load command returns the runs; feed the result back in.
	out, _ = update(out, runCmd(cmd))
	v := out.viewRuns()
	if !strings.Contains(v, "install-deps") || !strings.Contains(v, "succeeded") {
		t.Errorf("run review should list the run + status; got:\n%s", v)
	}
}

// Selecting a run and pressing enter reveals its command, status, exit code, and
// captured output (FR-042, SC-006).
func TestEscapeHatchRunDetail(t *testing.T) {
	d := &fakeDaemon{
		runs: []*pb.EscapeHatchRun{
			{Id: "ehr-1", SandboxId: "sb1", CommandName: "e2e", Command: "pnpm test:e2e", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED, ExitStatus: 1, Output: "1 test failed", OutputTruncated: true},
		},
	}
	m := runningSbx(d)
	out, cmd := update(m, press("E"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))
	v := out.viewRuns()
	for _, want := range []string{"pnpm test:e2e", "failed", "exit 1", "1 test failed", "truncated"} {
		if !strings.Contains(v, want) {
			t.Errorf("run detail missing %q; got:\n%s", want, v)
		}
	}
}

// A sandbox with a pending-approval run shows an awaiting-approval row badge.
func TestEscapeHatchBadgeShowsAwaitingApproval(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)
	tm, _ := m.handleEscapeHatchRun(pendingRun())
	m2 := asModel(tm)
	if badge := m2.escapeHatchBadge("sb1"); !strings.Contains(badge, "awaiting approval") {
		t.Errorf("expected an awaiting-approval badge, got %q", badge)
	}
	// After the run resolves, the badge clears.
	resolved := pendingRun()
	resolved.Status = pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED
	tm3, _ := m2.handleEscapeHatchRun(resolved)
	if badge := asModel(tm3).escapeHatchBadge("sb1"); badge != "" {
		t.Errorf("badge should clear once the run resolves, got %q", badge)
	}
}

// esc returns from the run screen to the list.
func TestEscapeHatchRunsScreenEsc(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)
	out, cmd := update(m, press("E"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("esc"))
	if out.screen != screenList {
		t.Errorf("esc should return to the list, screen = %v", out.screen)
	}
}
