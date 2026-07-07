package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
)

func agentSb(id string, st pb.SandboxState, status pb.AgentStatus) *pb.Sandbox {
	sb := &pb.Sandbox{Id: id, DisplayName: id, State: st}
	if status != pb.AgentStatus_AGENT_STATUS_UNSPECIFIED {
		sb.Agent = &pb.AgentSession{Status: status}
	}
	return sb
}

func changed(sb *pb.Sandbox) tea.Msg {
	return eventMsg{ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: sb}}}
}

func TestAgentBadge(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	R := pb.SandboxState_SANDBOX_STATE_RUNNING

	cases := []struct {
		name string
		sb   *pb.Sandbox
		want string // "" means expect an empty badge
	}{
		{"working", agentSb("a", R, pb.AgentStatus_AGENT_STATUS_WORKING), "working"},
		{"needs-input", agentSb("a", R, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT), "needs input"},
		{"idle", agentSb("a", R, pb.AgentStatus_AGENT_STATUS_IDLE), "idle"},
		{"exited", agentSb("a", R, pb.AgentStatus_AGENT_STATUS_EXITED), ""},
		{"no-agent", agentSb("a", R, pb.AgentStatus_AGENT_STATUS_UNSPECIFIED), ""},
		{"stopped-with-agent", agentSb("a", pb.SandboxState_SANDBOX_STATE_STOPPED, pb.AgentStatus_AGENT_STATUS_WORKING), ""},
	}
	for _, c := range cases {
		got := m.agentBadge(c.sb)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: agentBadge = %q, want empty", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: agentBadge = %q, want it to contain %q", c.name, got, c.want)
		}
	}
}

// TestAgentStatusLiveInList drives the event stream and asserts the row's agent
// indicator updates live as the daemon reports working -> needs input -> idle.
func TestAgentStatusLiveInList(t *testing.T) {
	sb := agentSb("sb1", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE)
	m := withSandboxes(New(&fakeDaemon{}, "/work"), []*pb.Sandbox{sb})

	if !strings.Contains(m.View(), "idle") {
		t.Fatalf("initial view should show the idle agent:\n%s", m.View())
	}

	// Agent starts working.
	m, _ = update(m, changed(agentSb("sb1", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_WORKING)))
	if !strings.Contains(m.View(), "working") {
		t.Errorf("view should show 'working' after the event:\n%s", m.View())
	}
	if !m.anyAgentWorking() {
		t.Error("anyAgentWorking should be true while an agent works")
	}

	// Agent needs a prompt.
	m, _ = update(m, changed(agentSb("sb1", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT)))
	if !strings.Contains(m.View(), "needs input") {
		t.Errorf("view should show 'needs input':\n%s", m.View())
	}
	if m.anyAgentWorking() {
		t.Error("anyAgentWorking should be false once the agent waits")
	}

	// A spinner tick while an agent works must not panic and keeps the row.
	m, _ = update(m, changed(agentSb("sb1", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_WORKING)))
	m, _ = update(m, spinner.TickMsg{})
	if !strings.Contains(m.View(), "working") {
		t.Errorf("view should still show 'working' after a tick:\n%s", m.View())
	}
}

func TestMergeSandboxUpdateNoOpAndUnknown(t *testing.T) {
	sb := agentSb("sb1", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_WORKING)
	m := withSandboxes(New(&fakeDaemon{}, "/work"), []*pb.Sandbox{sb})

	// Same status again → no rendered change.
	if m.mergeSandboxUpdate(agentSb("sb1", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_WORKING)) {
		t.Error("identical update should report no change")
	}
	// Unknown id and empty id → no change.
	if m.mergeSandboxUpdate(agentSb("nope", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE)) {
		t.Error("update for an unknown sandbox should report no change")
	}
	if m.mergeSandboxUpdate(&pb.Sandbox{}) {
		t.Error("empty-id update should report no change")
	}
}

// TestAgentStatusAcrossHosts covers the multi-host aggregate merge/remove paths.
// Critically, the client's host key ("local") differs from the sandbox's
// daemon-assigned HostId ("my-box") — the realistic case. The live merge MUST
// match by sandbox id regardless, or the rendered list (which reads m.hostAgg)
// never updates until a manual reload.
func TestAgentStatusAcrossHosts(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.hostAgg = []client.HostSandboxes{{
		Host:      client.HostConn{Entry: client.HostEntry{ID: "local", DisplayName: "localhost"}, State: client.HostConnected},
		Sandboxes: []*pb.Sandbox{{Id: "s1", HostId: "my-box", DisplayName: "s1", State: pb.SandboxState_SANDBOX_STATE_RUNNING, Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE}}},
	}}
	m.rebuildTabs()
	m.refreshListItems()
	if strings.Contains(m.View(), "working") {
		t.Fatal("precondition: should not show working yet")
	}

	// A live event whose HostId ("my-box") does NOT equal the client host key.
	upd := &pb.Sandbox{Id: "s1", HostId: "my-box", DisplayName: "s1", State: pb.SandboxState_SANDBOX_STATE_RUNNING, Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_WORKING}}
	m, _ = update(m, changed(upd))
	if !strings.Contains(m.View(), "working") {
		t.Fatalf("aggregate row should update live despite host-id mismatch:\n%s", m.View())
	}
	if got := m.hostAgg[0].Sandboxes[0].GetAgent().GetStatus(); got != pb.AgentStatus_AGENT_STATUS_WORKING {
		t.Errorf("aggregate sandbox status = %v, want WORKING", got)
	}

	// Remove it across the aggregate.
	if !m.removeSandbox("s1") {
		t.Error("expected removeSandbox to report a removal")
	}
	if len(m.hostAgg[0].Sandboxes) != 0 {
		t.Errorf("sandbox should be gone from the aggregate, have %d", len(m.hostAgg[0].Sandboxes))
	}
	// Removing again / empty id → no-op.
	if m.removeSandbox("s1") || m.removeSandbox("") {
		t.Error("removing an absent/empty id should be a no-op")
	}
}
