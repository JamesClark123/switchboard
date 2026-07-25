package escapehatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestExecutorTruncatesLargeOutput(t *testing.T) {
	// Emit ~2 MiB, well over the 1 MiB cap.
	out := NewExecutor().Run(context.Background(), t.TempDir(),
		shCmd("x", "head -c 2000000 /dev/zero | tr '\\0' 'a'"))
	if !out.Truncated {
		t.Error("output over 1 MiB should be marked truncated")
	}
	if len(out.Output) > maxCapturedOutput {
		t.Errorf("captured output %d exceeds cap %d", len(out.Output), maxCapturedOutput)
	}
}

func TestExecutorTimesOut(t *testing.T) {
	cmd := shCmd("x", "sleep 30")
	cmd.MaxDurationSeconds = 1
	start := time.Now()
	out := NewExecutor().Run(context.Background(), t.TempDir(), cmd)
	if out.Status != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_TIMED_OUT {
		t.Fatalf("status = %v, want TIMED_OUT", out.Status)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("timeout did not fire promptly: took %v", time.Since(start))
	}
}

// A cancelled run returns CANCELLED and leaves no orphaned process: the command
// would create a marker file only AFTER a sleep, so if cancellation truly killed the
// process group the marker never appears.
func TestExecutorCancelLeavesNoOrphan(t *testing.T) {
	ws := t.TempDir()
	marker := filepath.Join(ws, "marker")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Outcome, 1)
	go func() {
		done <- NewExecutor().Run(ctx, ws, shCmd("x", "sleep 5; touch "+marker))
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	out := <-done
	if out.Status != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED {
		t.Fatalf("status = %v, want CANCELLED", out.Status)
	}
	// Give any orphaned child time to (wrongly) finish its sleep + touch.
	time.Sleep(6 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Error("marker created -> the child process outlived cancellation (orphan)")
	}
}

// Two concurrent runs must not interleave their captured output (spec edge case);
// each Run owns its own boundedBuffer.
func TestExecutorConcurrentOutputNotInterleaved(t *testing.T) {
	ws := t.TempDir()
	var wg sync.WaitGroup
	results := make([]Outcome, 2)
	cmds := []string{
		"for i in $(seq 1 200); do echo AAAA; done",
		"for i in $(seq 1 200); do echo BBBB; done",
	}
	for i := range cmds {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			results[n] = NewExecutor().Run(context.Background(), ws, shCmd("x", cmds[n]))
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		lines := strings.Fields(r.Output)
		for _, l := range lines {
			if l != "AAAA" && l != "BBBB" {
				t.Fatalf("run %d output corrupted: unexpected token %q", i, l)
			}
		}
		// Each run's buffer must contain only its own token.
		if strings.Contains(r.Output, "AAAA") && strings.Contains(r.Output, "BBBB") {
			t.Errorf("run %d output interleaved two commands", i)
		}
	}
}
