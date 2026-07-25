package escapehatch

import (
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

type fakeEmitter struct {
	runs   []*pb.EscapeHatchRun
	notifs []pb.NotificationKind
}

func (f *fakeEmitter) PublishEscapeHatchRun(run *pb.EscapeHatchRun) {
	f.runs = append(f.runs, run)
}
func (f *fakeEmitter) EmitNotification(sandboxID string, kind pb.NotificationKind, msg string, now time.Time) *pb.NotificationEvent {
	f.notifs = append(f.notifs, kind)
	return &pb.NotificationEvent{}
}

type fakeSandboxes struct {
	sb  *pb.Sandbox
	err error
}

func (f *fakeSandboxes) Get(id string) (*pb.Sandbox, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sb, nil
}

func newTestService(sb *pb.Sandbox) (*Service, *fakeEmitter, *[]string) {
	em := &fakeEmitter{}
	var prompts []string
	prompt := func(sandboxID string, spec *pb.AgentSpec, text string) error {
		prompts = append(prompts, text)
		return nil
	}
	svc := New(&fakeSandboxes{sb: sb}, em, prompt)
	svc.SetClock(func() time.Time { return time.Unix(0, 0) })
	return svc, em, &prompts
}

func TestPublishRunningEmitsEventNoNotification(t *testing.T) {
	svc, em, prompts := newTestService(&pb.Sandbox{Id: "sb1"})
	svc.publish(&pb.EscapeHatchRun{SandboxId: "sb1", CommandName: "a", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING})
	if len(em.runs) != 1 {
		t.Errorf("want 1 event, got %d", len(em.runs))
	}
	if len(em.notifs) != 0 {
		t.Errorf("RUNNING should emit no notification, got %v", em.notifs)
	}
	if len(*prompts) != 0 {
		t.Errorf("RUNNING should not deliver to agent, got %v", *prompts)
	}
}

func TestPublishPendingApprovalEmitsNeedsApproval(t *testing.T) {
	svc, em, _ := newTestService(&pb.Sandbox{Id: "sb1"})
	svc.publish(&pb.EscapeHatchRun{SandboxId: "sb1", CommandName: "a", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL})
	if len(em.notifs) != 1 || em.notifs[0] != pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_NEEDS_APPROVAL {
		t.Errorf("want one NEEDS_APPROVAL notification, got %v", em.notifs)
	}
}

func TestPublishTerminalEmitsRunCompleteAndDelivers(t *testing.T) {
	terminal := []pb.EscapeHatchRunStatus{
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_TIMED_OUT,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED,
	}
	for _, st := range terminal {
		svc, em, prompts := newTestService(&pb.Sandbox{Id: "sb1", Agent: &pb.AgentSession{Spec: &pb.AgentSpec{Kind: "claude"}}})
		svc.publish(&pb.EscapeHatchRun{SandboxId: "sb1", CommandName: "a", Status: st})
		if len(em.runs) != 1 {
			t.Errorf("%v: want 1 event, got %d", st, len(em.runs))
		}
		if len(em.notifs) != 1 || em.notifs[0] != pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_RUN_COMPLETE {
			t.Errorf("%v: want one RUN_COMPLETE notification, got %v", st, em.notifs)
		}
		if len(*prompts) != 1 {
			t.Errorf("%v: want one agent delivery, got %d", st, len(*prompts))
		}
	}
}

func TestDeliverSurvivesLookupFailure(t *testing.T) {
	// Delivery must be best-effort: a missing sandbox must not panic.
	em := &fakeEmitter{}
	svc := New(&fakeSandboxes{err: errors.New("gone")}, em, func(string, *pb.AgentSpec, string) error { return nil })
	svc.SetClock(func() time.Time { return time.Unix(0, 0) })
	svc.publish(&pb.EscapeHatchRun{SandboxId: "sb1", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED})
	if len(em.runs) != 1 {
		t.Errorf("event should still be emitted despite lookup failure")
	}
}

func TestCallbackMessageIncludesOutcomeAndOutput(t *testing.T) {
	msg := callbackMessage(&pb.EscapeHatchRun{
		CommandName: "install-deps",
		Status:      pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED,
		ExitStatus:  0,
		Output:      "added 42 packages",
	})
	if !strings.Contains(msg, "install-deps") || !strings.Contains(msg, "succeeded") || !strings.Contains(msg, "added 42 packages") {
		t.Errorf("callback message missing detail: %q", msg)
	}
	trunc := callbackMessage(&pb.EscapeHatchRun{CommandName: "x", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED, Output: "partial", OutputTruncated: true})
	if !strings.Contains(trunc, "truncated") {
		t.Errorf("truncated output should be flagged: %q", trunc)
	}
}

func TestLookupCommandIsAllowlist(t *testing.T) {
	sb := &pb.Sandbox{EscapeHatchCommands: []*pb.EscapeHatchCommand{auto("install-deps", "pnpm install")}}
	if _, ok := lookupCommand(sb, "install-deps"); !ok {
		t.Error("known command should resolve")
	}
	if _, ok := lookupCommand(sb, "rm-rf"); ok {
		t.Error("unknown command must NOT resolve (allowlist)")
	}
}
