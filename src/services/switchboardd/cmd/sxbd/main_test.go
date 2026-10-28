package main

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func ctlSandbox(id string, st pb.SandboxState) *pb.Sandbox {
	return &pb.Sandbox{Id: id, State: st, Controller: &pb.SandboxController{OrchestratorId: "o"}}
}

// PR #17 review run-484: a worker re-adopted RUNNING is brought up (gateway
// reload, then its agent) and not warm-started beside that; the rest are.
func TestStartWorkersLeavesReadoptedToBringUp(t *testing.T) {
	readopted := ctlSandbox("a", pb.SandboxState_SANDBOX_STATE_RUNNING)
	stopped := ctlSandbox("b", pb.SandboxState_SANDBOX_STATE_STOPPED) // moved to STOPPED
	plain := &pb.Sandbox{Id: "c", State: pb.SandboxState_SANDBOX_STATE_RUNNING}
	other := ctlSandbox("d", pb.SandboxState_SANDBOX_STATE_RUNNING)
	var up, warm []string
	startWorkers([]*pb.Sandbox{readopted, stopped, plain}, []*pb.Sandbox{readopted, stopped, plain, other},
		func(sb *pb.Sandbox) { up = append(up, sb.GetId()) },
		func(sb *pb.Sandbox) { warm = append(warm, sb.GetId()) })
	if len(up) != 1 || up[0] != "a" {
		t.Errorf("brought up %v, want [a]", up)
	}
	if want := []string{"b", "c", "d"}; len(warm) != 3 || warm[0] != want[0] || warm[1] != want[1] || warm[2] != want[2] {
		t.Errorf("warm-started %v, want %v (never the re-adopted a)", warm, want)
	}
}

type fakeReconciler struct {
	errs  []error
	moved []*pb.Sandbox
	calls int
}

func (f *fakeReconciler) Reconcile(context.Context) ([]*pb.Sandbox, error) {
	f.calls++
	if f.calls <= len(f.errs) {
		return nil, f.errs[f.calls-1]
	}
	return f.moved, nil
}

// The warm start waits for a retry that reads the runtime, and gets what it moved.
func TestRetryReconcileReportsTheFirstRead(t *testing.T) {
	r := &fakeReconciler{errs: []error{errors.New("runtime down")}, moved: []*pb.Sandbox{ctlSandbox("a", pb.SandboxState_SANDBOX_STATE_RUNNING)}}
	var got [][]*pb.Sandbox
	retryReconcile(context.Background(), r, []time.Duration{0, 0, 0}, func(m []*pb.Sandbox) { got = append(got, m) })
	if r.calls != 2 || len(got) != 1 || len(got[0]) != 1 || got[0][0].GetId() != "a" {
		t.Errorf("calls %d, reported %v", r.calls, got)
	}
	// Never read: nothing is reported.
	r = &fakeReconciler{errs: []error{errors.New("x"), errors.New("x")}}
	got = nil
	retryReconcile(context.Background(), r, []time.Duration{0, 0}, func(m []*pb.Sandbox) { got = append(got, m) })
	if r.calls != 2 || got != nil {
		t.Errorf("unread runtime: calls %d, reported %v", r.calls, got)
	}
	// A stopped daemon stops retrying.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = &fakeReconciler{}
	retryReconcile(ctx, r, []time.Duration{time.Hour}, func([]*pb.Sandbox) { t.Error("reported after stop") })
	if r.calls != 0 {
		t.Errorf("reconciled after stop: %d", r.calls)
	}
}
