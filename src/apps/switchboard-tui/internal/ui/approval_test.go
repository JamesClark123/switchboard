package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// asModel narrows a tea.Model back to the concrete Model for assertions on methods
// invoked directly (not through the update() helper).
func asModel(tm tea.Model) Model { return tm.(Model) }

func pendingRun() *pb.EscapeHatchRun {
	return &pb.EscapeHatchRun{
		Id:          "ehr-1",
		SandboxId:   "sb1",
		CommandName: "deploy",
		Command:     "./deploy.sh",
		Status:      pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL,
	}
}

// A pending-approval run event raises the approval modal while on the list.
func TestPendingRunOpensApprovalModal(t *testing.T) {
	m := runningSbx(&fakeDaemon{})
	tm, _ := m.handleEscapeHatchRun(pendingRun())
	out := asModel(tm)
	if out.screen != screenApproval {
		t.Fatalf("screen = %v, want screenApproval", out.screen)
	}
	v := out.approvalModal()
	for _, want := range []string{"deploy", "./deploy.sh", "HOST"} {
		if !strings.Contains(v, want) {
			t.Errorf("approval modal missing %q; got:\n%s", want, v)
		}
	}
}

// y/enter approves; the decision reaches the daemon with approved=true.
func TestApprovalApprove(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)
	tm, _ := m.handleEscapeHatchRun(pendingRun())
	tm2, cmd := asModel(tm).updateApprovalKey(press("y"))
	if asModel(tm2).screen != screenList {
		t.Errorf("screen = %v, want back to list", asModel(tm2).screen)
	}
	runCmd(cmd) // execute the decide command
	if len(d.decidedRuns) != 1 || !d.decidedRuns[0].approved || d.decidedRuns[0].runID != "ehr-1" {
		t.Fatalf("expected approve decision for ehr-1, got %+v", d.decidedRuns)
	}
}

// n/esc denies; the decision reaches the daemon with approved=false.
func TestApprovalDeny(t *testing.T) {
	for _, key := range []string{"n", "esc"} {
		d := &fakeDaemon{}
		m := runningSbx(d)
		tm, _ := m.handleEscapeHatchRun(pendingRun())
		_, cmd := asModel(tm).updateApprovalKey(press(key))
		runCmd(cmd)
		if len(d.decidedRuns) != 1 || d.decidedRuns[0].approved {
			t.Errorf("%q should deny, got %+v", key, d.decidedRuns)
		}
	}
}

// An unrecognized key is NOT consent (deny-by-default, SC-003): the modal stays up
// and nothing is decided.
func TestApprovalUnrecognizedKeyIsNotConsent(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)
	tm, _ := m.handleEscapeHatchRun(pendingRun())
	tm2, cmd := asModel(tm).updateApprovalKey(press("z"))
	if asModel(tm2).screen != screenApproval {
		t.Errorf("an unrecognized key should keep the modal up, screen = %v", asModel(tm2).screen)
	}
	if cmd != nil {
		t.Error("an unrecognized key must issue no decision command")
	}
	if len(d.decidedRuns) != 0 {
		t.Errorf("no decision should have been made, got %+v", d.decidedRuns)
	}
}

// A pending run arriving while not on the list does not steal focus; it still shows
// a badge and can be approved later.
func TestPendingRunWhileBusyDoesNotStealFocus(t *testing.T) {
	m := runningSbx(&fakeDaemon{})
	m.screen = screenNotifications
	tm, _ := m.handleEscapeHatchRun(pendingRun())
	out := asModel(tm)
	if out.screen != screenNotifications {
		t.Errorf("pending run should not steal focus from another screen, screen = %v", out.screen)
	}
	if _, ok := out.pendingRuns["ehr-1"]; !ok {
		t.Error("run should still be tracked as pending for the badge")
	}
}
