package ui

import (
	"context"
	"strings"
	"testing"
)

func TestInitialListLoadingSpinner(t *testing.T) {
	// Before the first list load arrives, the page shows a refreshing spinner.
	m := sized(New(&fakeDaemon{}, "/work"))
	if !m.listLoading {
		t.Fatal("a fresh model should be loading its first list")
	}
	if !strings.Contains(m.View(), "refreshing") {
		t.Error("list view should show a refreshing indicator while loading")
	}
	// Once the list arrives, the indicator clears.
	m, _ = update(m, sandboxesMsg(nil))
	if m.listLoading {
		t.Error("listLoading should clear once sandboxes arrive")
	}
}

func TestPerRowBusySpinnerOnStop(t *testing.T) {
	d := &fakeDaemon{}
	sbs := seeded(d, 1)
	m := withSandboxes(New(d, "/work"), sbs)
	id := sbs[0].GetId()

	// Pressing stop marks the sandbox's row busy and dispatches the daemon call.
	m, cmd := update(m, press("s"))
	if m.busy[id] != "stopping" {
		t.Fatalf("stop should mark sandbox %s busy, got %v", id, m.busy)
	}
	if !strings.Contains(m.viewList(), "stopping") {
		t.Error("the busy row should show a 'stopping' indicator")
	}

	// The daemon responds; the per-row spinner clears and a list refresh begins.
	msg := runCmd(cmd)
	if _, ok := msg.(statusMsg); !ok {
		t.Fatalf("stop should yield a statusMsg, got %T", msg)
	}
	m, _ = update(m, msg)
	if len(m.busy) != 0 {
		t.Errorf("busy should clear after the response, got %v", m.busy)
	}
	if !m.listLoading {
		t.Error("a refresh should be in flight after the action completes")
	}
	if !strings.Contains(m.viewList(), "refreshing") {
		t.Error("list should show the refreshing indicator during reload")
	}
}

func TestBusyClearsOnError(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work"), seeded(d, 1))
	m = m.startBusy("a0000000", "destroying")
	if len(m.busy) == 0 {
		t.Fatal("startBusy should mark the sandbox busy")
	}
	m, _ = update(m, errMsg{err: context.DeadlineExceeded})
	if len(m.busy) != 0 || m.listLoading {
		t.Errorf("an error should clear loading state: busy=%v listLoading=%v", m.busy, m.listLoading)
	}
}
