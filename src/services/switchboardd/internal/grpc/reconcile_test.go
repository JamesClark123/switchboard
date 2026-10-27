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
