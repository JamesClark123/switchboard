package sandbox

import (
	"context"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
)

// SWI-21 FR-425: a marked server being replaced is skipped at launch and
// recorded in Sandbox.mcp_skipped; the end of the replace sweeps the entry.

func TestLaunchRecordsServersSkippedBeingReplaced(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	states := map[string]mcp.ReplaceStatus{"gh": mcp.BeingReplaced, "ended": mcp.EndedRegistered, "gone": mcp.EndedRemoved}
	sb, logs := launchWithMcp(t, m, dir, &McpAttach{
		Mode: pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE, Servers: []string{"notion"},
		BeingReplaced: []string{"gh", "ended", "gone"},
		ReplaceState:  func(n string) mcp.ReplaceStatus { return states[n] },
	})
	if strings.Join(runner.mcpLoads, ",") != sb.GetContainerRef()+" notion" {
		t.Errorf("loads = %v", runner.mcpLoads)
	}
	got := sb.GetMcpSkipped()
	if len(got) != 2 || got[0].GetName() != "gh" || got[0].GetState() != pb.McpSkipState_MCP_SKIP_STATE_SKIPPED_BEING_REPLACED ||
		got[1].GetName() != "ended" || got[1].GetState() != pb.McpSkipState_MCP_SKIP_STATE_NOT_ATTACHED_AT_LAUNCH {
		t.Errorf("mcp_skipped = %v", got)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "mcp: gh skipped: being replaced") {
		t.Errorf("logs = %v", logs)
	}

	// The replace ends registered: the entry becomes NOT_ATTACHED_AT_LAUNCH.
	m.SweepMcpSkipped("gh", true)
	p, _ := m.Get(sb.GetId())
	if p.GetMcpSkipped()[0].GetState() != pb.McpSkipState_MCP_SKIP_STATE_NOT_ATTACHED_AT_LAUNCH {
		t.Errorf("after sweep = %v", p.GetMcpSkipped())
	}
	// A name with no entry changes nothing; a removal ending clears the entry.
	m.SweepMcpSkipped("other", false)
	m.SweepMcpSkipped("ended", false)
	p, _ = m.Get(sb.GetId())
	if len(p.GetMcpSkipped()) != 1 || p.GetMcpSkipped()[0].GetName() != "gh" {
		t.Errorf("after removal sweep = %v", p.GetMcpSkipped())
	}
	// Attaching the skipped server clears its entry.
	if _, err := m.AttachMcp(context.Background(), sb.GetId(), "gh", nil); err != nil {
		t.Fatal(err)
	}
	p, _ = m.Get(sb.GetId())
	if len(p.GetMcpSkipped()) != 0 {
		t.Errorf("after attach = %v", p.GetMcpSkipped())
	}
}

func TestSkippedEntriesWithoutReplaceState(t *testing.T) {
	if skippedEntries(nil) != nil {
		t.Error("nil attach")
	}
	got := skippedEntries(&McpAttach{BeingReplaced: []string{"a"}})
	if len(got) != 1 || got[0].GetState() != pb.McpSkipState_MCP_SKIP_STATE_SKIPPED_BEING_REPLACED {
		t.Errorf("got %v", got)
	}
}
