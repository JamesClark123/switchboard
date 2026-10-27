package grpc

import (
	"testing"
	"time"
)

func TestReconcileGateThrottles(t *testing.T) {
	now := time.Unix(1_000, 0)
	g := &reconcileGate{now: func() time.Time { return now }}
	runs := 0
	run := func() { g.run(func() { runs++ }) }

	run()
	run() // within the window
	if runs != 1 {
		t.Fatalf("runs = %d after two calls in the window; want 1", runs)
	}
	now = now.Add(reconcileWindow)
	run()
	if runs != 2 {
		t.Fatalf("runs = %d once the window passed; want 2", runs)
	}
}

// A caller arriving while a reconcile runs does not wait for it.
func TestReconcileGateDoesNotQueueBehindARunningPass(t *testing.T) {
	g := &reconcileGate{}
	entered, release := make(chan struct{}), make(chan struct{})
	go g.run(func() { close(entered); <-release })
	<-entered
	ran := false
	done := make(chan struct{})
	go func() { g.run(func() { ran = true }); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a second caller waited behind the running reconcile")
	}
	close(release)
	if ran {
		t.Error("a second caller ran its own reconcile while one was running")
	}
}
