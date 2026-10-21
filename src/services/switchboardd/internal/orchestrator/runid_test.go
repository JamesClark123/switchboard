package orchestrator

import "fmt"

// runID mirrors runs.go's id scheme for tests that seed runs directly.
func runID(orchID string, seq uint64) string {
	short := orchID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("run-%s-%d", short, seq)
}
