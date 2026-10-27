package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// terminalRuns is beta's run list: one run per state, on workers of hostb.
func terminalRuns() *pb.ListRunsResponse {
	inflight := func(id, sb string, st pb.RunState) *pb.Run {
		return &pb.Run{Id: id, Action: "review-pr", State: st, SandboxId: sb, SandboxHostId: "hostb"}
	}
	return &pb.ListRunsResponse{
		Queued: []*pb.Run{{Id: "run-q", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1}},
		InFlight: []*pb.Run{
			inflight("run-run", "w-run", pb.RunState_RUN_STATE_RUNNING),
			inflight("run-in", "w-in", pb.RunState_RUN_STATE_AWAITING_INPUT),
			inflight("run-rev", "w-rev", pb.RunState_RUN_STATE_AWAITING_REVIEW),
			inflight("run-gone", "w-gone", pb.RunState_RUN_STATE_RUNNING),
			inflight("run-stop", "w-stop", pb.RunState_RUN_STATE_RUNNING),
		},
		History: []*pb.Run{{Id: "run-done", Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, SandboxId: "w-run", SandboxHostId: "hostb", EndedAt: at(0)}},
	}
}

func runningWorker(id string) *pb.Sandbox {
	return &pb.Sandbox{Id: id, DisplayName: "worker-" + id, State: pb.SandboxState_SANDBOX_STATE_RUNNING}
}

// terminalJobs is the Jobs screen on one in-process daemon (hostb) whose
// workers are in the sandbox list, so the in-place terminal attaches for real.
func terminalJobs(t *testing.T) (Model, *fakeDaemon) {
	t.Helper()
	d := &fakeDaemon{hostID: "hostb", orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}, orchRuns: terminalRuns()}
	m := withSandboxes(sized(New(d, "/work").WithSbx("sbx")), []*pb.Sandbox{
		runningWorker("w-run"), runningWorker("w-in"), runningWorker("w-rev"),
		{Id: "w-stop", DisplayName: "worker-w-stop", State: pb.SandboxState_SANDBOX_STATE_STOPPED},
	})
	return openJobs(m), d
}

// terminalHosts wires two connected hosts whose sandboxes share the id "w-run".
func terminalHosts(t *testing.T) Model {
	t.Helper()
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}, orchRuns: terminalRuns()}
	m, _, _ := twoFakeHosts(t, a, b)
	m.hostAgg = []client.HostSandboxes{
		{Host: client.HostConn{Entry: client.HostEntry{ID: "hosta", DisplayName: "alpha"}, State: client.HostConnected},
			Sandboxes: []*pb.Sandbox{runningWorker("w-run")}},
		{Host: client.HostConn{Entry: client.HostEntry{ID: "hostb", DisplayName: "beta"}, State: client.HostConnected},
			Sandboxes: []*pb.Sandbox{runningWorker("w-run"), runningWorker("w-in")}},
	}
	return m
}

// SWI-14: t on an in-flight Overview run opens its worker's terminal on the
// daemon that hosts it, and ctrl+q returns to the same Jobs tab and row.
func TestJobsTerminalFromOverview(t *testing.T) {
	for _, id := range []string{"run-run", "run-in", "run-rev"} {
		m, d := terminalJobs(t)
		m = overviewAt(t, m, id)
		cursor := m.jobs.cursors[jobsOverview]
		if h := m.renderHelp(m.jobsHelp()); !strings.Contains(h, "terminal") || !strings.Contains(h, "external terminal") {
			t.Errorf("%s: help = %q", id, h)
		}
		m, cmd := update(m, press("t"))
		opened, ok := runCmd(cmd).(termOpenedMsg)
		if !ok {
			t.Fatalf("%s: t should attach, status %q", id, m.jobs.status)
		}
		m, _ = update(m, opened)
		want := strings.TrimPrefix(id, "run-")
		if m.screen != screenTerminal || d.attachedID != "w-"+want {
			t.Fatalf("%s: screen %v, attached %q", id, m.screen, d.attachedID)
		}
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
		if m.screen != screenJobs || m.jobs.tab != jobsOverview || m.jobs.cursors[jobsOverview] != cursor {
			t.Errorf("%s: back on screen %v tab %v cursor %d (want %d)", id, m.screen, m.jobs.tab, m.jobs.cursors[jobsOverview], cursor)
		}
		if !d.termClosed || !strings.Contains(m.status, "detached from worker-w-"+want) {
			t.Errorf("%s: detach status %q", id, m.status)
		}
	}
}

// SWI-14: t and T refuse on the surface's line for runs that are not in flight,
// whose worker is unknown, or whose worker is not running.
func TestJobsTerminalRefusals(t *testing.T) {
	m, d := terminalJobs(t)
	for _, c := range []struct{ id, key, want string }{
		{"run-q", "t", "only an in-progress run has a terminal"},
		{"run-done", "T", "only an in-progress run has a terminal"},
		{"run-gone", "t", "worker sandbox w-gone is not on a connected daemon"},
	} {
		m = overviewAt(t, m, c.id)
		m.jobs.status = ""
		mm, cmd := update(m, press(c.key))
		if cmd != nil || mm.screen != screenJobs || mm.jobs.status != c.want {
			t.Errorf("%s %s: status %q (want %q), cmd %v", c.id, c.key, mm.jobs.status, c.want, cmd != nil)
		}
		if inFlight := c.id == "run-gone" || c.id == "run-stop"; strings.Contains(mm.renderHelp(mm.jobsHelp()), "terminal") != inFlight {
			t.Errorf("%s: only in-flight rows offer the terminal", c.id)
		}
	}
	// A stopped worker gets the Sandboxes list's start-first hint.
	m = overviewAt(t, m, "run-stop")
	if mm, cmd := update(m, press("t")); cmd != nil || !strings.Contains(mm.status, "start the sandbox") {
		t.Errorf("stopped worker: status %q", mm.status)
	}
	if d.attachedID != "" {
		t.Error("a refusal attaches nothing")
	}
}

// SWI-14: on the Queue tab, T opens the worker's external terminal and t opens
// it in place; a failed attach returns to the Queue page with the error.
func TestJobsTerminalFromQueue(t *testing.T) {
	m, d := terminalJobs(t)
	m = m.WithTerminal("sleep 30")
	m.jobs.tab = jobsQueue
	rows, _, _, _ := m.queuePageRows()
	for i, r := range rows {
		if r.run.GetId() == "run-rev" {
			m.jobs.cursors[jobsQueue] = i
		}
	}
	if rows[m.jobs.cursors[jobsQueue]].run.GetId() != "run-rev" {
		t.Fatal("no run-rev row on the queue")
	}
	m, _ = update(m, press("T"))
	if _, ok := m.extTerm[extTermKey(m.activeHost, "w-rev")]; !ok || m.screen != screenJobs {
		t.Fatalf("T should open w-rev externally, status %q / %q", m.status, m.jobs.status)
	}
	d.attachErr = errors.New("boom")
	m, cmd := update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenJobs || m.jobs.tab != jobsQueue || !strings.Contains(m.status, "terminal: boom") {
		t.Errorf("a failed attach stays on the Queue: screen %v status %q", m.screen, m.status)
	}
	d.attachErr = nil
	m, cmd = update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	// The session dying (a send error) also returns to the Queue.
	m, _ = update(m, termClosedMsg{err: errors.New("gone"), name: m.term.name, returnTo: m.term.returnTo, seq: m.term.seq})
	if m.screen != screenJobs || !strings.Contains(m.status, "terminal: gone") {
		t.Errorf("closed session: screen %v status %q", m.screen, m.status)
	}
}

// The run's sandbox_host_id picks the host; without one, any host matches, and
// the active host's list is the fallback.
func TestRunSandboxResolution(t *testing.T) {
	m := terminalHosts(t)
	if _, host, ok := m.runSandbox(&pb.Run{SandboxId: "w-run", SandboxHostId: "hostb"}); !ok || host != "hostb" {
		t.Errorf("hostb's w-run resolves to %q %v", host, ok)
	}
	if _, host, ok := m.runSandbox(&pb.Run{SandboxId: "w-run", SandboxHostId: "hosta"}); !ok || host != "hosta" {
		t.Errorf("hosta's w-run resolves to %q %v", host, ok)
	}
	if _, _, ok := m.runSandbox(&pb.Run{SandboxId: "w-in", SandboxHostId: "hosta"}); ok {
		t.Error("w-in is not on hosta")
	}
	if _, _, ok := m.runSandbox(&pb.Run{}); ok {
		t.Error("a run with no worker has no sandbox")
	}
	// Without a sandbox_host_id only a sandbox on exactly one host matches.
	if _, host, ok := m.runSandbox(&pb.Run{SandboxId: "w-in"}); !ok || host != "hostb" {
		t.Errorf("w-in (hostb only) resolves to %q %v", host, ok)
	}
	if _, _, ok := m.runSandbox(&pb.Run{SandboxId: "w-run"}); ok {
		t.Error("w-run is on two hosts: ambiguous")
	}
	m.hostAgg = nil
	m.sandboxes = []*pb.Sandbox{{Id: "w-solo"}}
	if _, host, ok := m.runSandbox(&pb.Run{SandboxId: "w-solo"}); !ok || host != m.activeHost {
		t.Errorf("fallback resolves to %q %v", host, ok)
	}
	// A host that is known but not connected, or a connected host that is not
	// the active one, never falls back to another host's sandbox.
	m.sandboxes = append(m.sandboxes, runningWorker("w-run"))
	if _, _, ok := m.runSandbox(&pb.Run{SandboxId: "w-run", SandboxHostId: "hostc"}); ok {
		t.Error("a worker on a disconnected daemon is refused")
	}
	other := "hosta"
	if m.activeHost == "hosta" {
		other = "hostb"
	}
	if _, _, ok := m.runSandbox(&pb.Run{SandboxId: "w-run", SandboxHostId: other}); ok {
		t.Errorf("%s's worker must not resolve to the active host's list", other)
	}
	if got := m.lookupWorkerName(&pb.Run{SandboxId: "w-solo"}); got != "w-solo" {
		t.Errorf("an unnamed worker shows its id, got %q", got)
	}
}

// SWI-14: orchestrator reloads skipped while the terminal hid Jobs run when
// ctrl+q returns to it.
func TestJobsTerminalDetachReloadsStale(t *testing.T) {
	m, d := terminalJobs(t)
	m = overviewAt(t, m, "run-run")
	m, cmd := update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	runs := terminalRuns()
	runs.InFlight = runs.InFlight[1:]
	runs.History = append(runs.History, &pb.Run{Id: "run-run", State: pb.RunState_RUN_STATE_COMPLETED, EndedAt: at(0)})
	d.mu.Lock()
	d.orchRuns = runs
	d.mu.Unlock()
	tg := m.jobs.orchs[0].target
	if c := m.jobsReloadOrch(tg.host, tg.id); c != nil {
		t.Fatal("a reload while the terminal shows is deferred")
	}
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	m = feed(m, cmd)
	for _, it := range m.overviewItems() {
		if it.run != nil && it.run.run.GetId() == "run-run" && !runFinished(it.run.run) {
			t.Error("the deferred reload ran on return: run-run is finished")
		}
	}
}

// SWI-14: an attach that completes or fails after the developer left the
// screen it was started from neither pulls them into the terminal nor back.
func TestJobsTerminalAttachAfterLeaving(t *testing.T) {
	m, d := terminalJobs(t)
	m = overviewAt(t, m, "run-run")
	_, cmd := update(m, press("t"))
	opened := runCmd(cmd)
	m.screen = screenDaemons
	m, _ = update(m, opened)
	if m.screen != screenDaemons || m.term.session != nil || !d.termClosed {
		t.Errorf("a late attach detaches: screen %v", m.screen)
	}
	m, _ = update(m, termClosedMsg{err: errors.New("late"), name: "x", returnTo: screenJobs})
	if m.screen != screenDaemons || !strings.Contains(m.status, "terminal: late") {
		t.Errorf("a late failure only reports: screen %v status %q", m.screen, m.status)
	}
}

// SWI-14: only the latest attach opens, an overlay opened while it attached
// keeps it out, and a stale close never tears down the terminal showing.
func TestJobsTerminalAttachIdentity(t *testing.T) {
	m, _ := terminalJobs(t)
	m = overviewAt(t, m, "run-run")
	m1, first := update(m, press("t"))
	m2, second := update(m1, press("t"))
	firstOpened, secondOpened := runCmd(first), runCmd(second)
	m, _ = update(m2, firstOpened)
	if m.screen != screenJobs {
		t.Fatal("a superseded attach does not open")
	}
	m, _ = update(m, secondOpened)
	if m.screen != screenTerminal {
		t.Fatal("the latest attach opens")
	}
	m, _ = update(m, termClosedMsg{err: errors.New("old"), name: "x", returnTo: screenList, seq: m.term.seq - 1})
	if m.screen != screenTerminal || m.term.session == nil {
		t.Error("a close about an older attach leaves the terminal open")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})

	m, cmd := update(m, press("t"))
	m.jobs.prompt = &pb.Run{Id: "run-run"}
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenJobs || m.term.session != nil {
		t.Error("an overlay opened while attaching keeps the terminal out")
	}
}
