package sandbox

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// A second acquire is refused, naming the operation that already holds the latch
// (FR-066), and the refusal is recognisable by its sentinel.
func TestOpLockSecondAcquireRefusedWithFirstLabel(t *testing.T) {
	var l opLock
	if err := l.acquire("sb-1", "add sources"); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	err := l.acquire("sb-1", "refresh")
	if err == nil {
		t.Fatal("second acquire must be refused while the first holds the latch")
	}
	if !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want it to wrap ErrBusy", err)
	}
	if !strings.Contains(err.Error(), "add sources in progress") {
		t.Errorf("err = %q, want it to name the in-flight operation", err)
	}
	if label, ok := l.holder("sb-1"); !ok || label != "add sources" {
		t.Errorf("holder = (%q, %v), want (add sources, true)", label, ok)
	}
}

func TestOpLockReleaseFrees(t *testing.T) {
	var l opLock
	if err := l.acquire("sb-1", "refresh"); err != nil {
		t.Fatal(err)
	}
	l.release("sb-1")
	if err := l.acquire("sb-1", "remove sources"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if _, ok := l.holder("sb-1"); !ok {
		t.Error("latch should be held again after the second acquire")
	}
	// Releasing an unheld latch is harmless.
	l.release("never-held")
}

// N racing acquires for one sandbox yield exactly one winner — the latch is what
// makes "never interleave on one workspace" (FR-066) hold under concurrency.
func TestOpLockConcurrentAcquiresYieldOneWinner(t *testing.T) {
	var l opLock
	const n = 64
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := l.acquire("sb-1", "add sources"); err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}

// Latches are per sandbox: an operation on one never blocks another.
func TestOpLockDistinctSandboxesNeverContend(t *testing.T) {
	var l opLock
	if err := l.acquire("sb-1", "add sources"); err != nil {
		t.Fatal(err)
	}
	if err := l.acquire("sb-2", "refresh"); err != nil {
		t.Fatalf("a different sandbox must not be blocked: %v", err)
	}
	if err := l.acquire("sb-3", "remove sources"); err != nil {
		t.Fatalf("a third sandbox must not be blocked: %v", err)
	}
}
