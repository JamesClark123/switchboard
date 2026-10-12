package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Feature 008: the daemon's attach-by-default servers reach a launching sandbox in
// the daemon's attach mode, are recorded, and never fail the launch (FR-081..084,
// FR-100).

func launchWithMcp(t *testing.T, m *Manager, dir string, att *McpAttach) (*pb.Sandbox, []string) {
	t.Helper()
	src := makeSource(t, dir, "proj-"+strings.ReplaceAll(t.Name(), "/", "-"))
	var logs []string
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "mcp-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))},
		Sources: []*pb.SourceRef{src},
		Mcp:     att,
	}, nil, func(l string) { logs = append(logs, l) })
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return sb, logs
}

func TestLaunchAdditiveLoadsAfterCreateBeforeRunning(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	sb, logs := launchWithMcp(t, m, dir, &McpAttach{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE, Servers: []string{"notion", "linear"}})
	if runner.launches != 1 || len(runner.lastStaticMcp) != 0 {
		t.Errorf("additive mode must not pass --static-mcp (launches %d, static %v)", runner.launches, runner.lastStaticMcp)
	}
	want := []string{sb.GetContainerRef() + " notion", sb.GetContainerRef() + " linear"}
	if strings.Join(runner.mcpLoads, ",") != strings.Join(want, ",") {
		t.Errorf("loads = %v, want %v (one per server, after create, against the assigned ref)", runner.mcpLoads, want)
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v", sb.GetState())
	}
	if got := sb.GetMcpServers(); len(got) != 2 || got[0] != "notion" || got[1] != "linear" {
		t.Errorf("record mcp_servers = %v", got)
	}
	if sb.GetMcpAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Errorf("record mode = %v", sb.GetMcpAttachMode())
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "mcp: attached notion") || !strings.Contains(joined, "mcp: attached linear") {
		t.Errorf("launch log should name each attached server; got:\n%s", joined)
	}
}

func TestLaunchAdditiveSkipsFailedLoadWithoutFailing(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	runner.failLoad = map[string]bool{"linear": true}
	sb, logs := launchWithMcp(t, m, dir, &McpAttach{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE, Servers: []string{"notion", "linear"}})
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("a failed load must not fail the launch; state = %v", sb.GetState())
	}
	if got := sb.GetMcpServers(); len(got) != 1 || got[0] != "notion" {
		t.Errorf("record should exclude the failed server; got %v", got)
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "mcp: skipped linear: load failed for linear") {
		t.Errorf("launch log should explain the skip; got:\n%s", joined)
	}
}

func TestLaunchExclusivePassesStaticSetAndNoLoads(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	sb, logs := launchWithMcp(t, m, dir, &McpAttach{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE, Servers: []string{"notion"}})
	if strings.Join(runner.lastStaticMcp, ",") != "notion" || len(runner.mcpLoads) != 0 {
		t.Errorf("exclusive mode should pre-load via --static-mcp only; static %v loads %v", runner.lastStaticMcp, runner.mcpLoads)
	}
	if sb.GetMcpAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE || strings.Join(sb.GetMcpServers(), ",") != "notion" {
		t.Errorf("record = %v / %v", sb.GetMcpAttachMode(), sb.GetMcpServers())
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "pre-loaded notion") {
		t.Errorf("launch log should say the set was pre-loaded; got:\n%s", joined)
	}
}

func TestLaunchWithoutMarksOrGatewayLaunchesAsBefore(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	sb, _ := launchWithMcp(t, m, dir, nil)
	if len(runner.mcpLoads) != 0 || len(runner.lastStaticMcp) != 0 || len(sb.GetMcpServers()) != 0 {
		t.Errorf("no gateway: expected no mcp activity; loads %v static %v record %v", runner.mcpLoads, runner.lastStaticMcp, sb.GetMcpServers())
	}
	// Marks resolved to an empty set (any mode) behave the same (FR-085).
	sb2, logs := launchWithMcp(t, m, dir, &McpAttach{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE, Skipped: []string{"gone"}})
	if len(runner.mcpLoads) != 0 || len(runner.lastStaticMcp) != 0 || len(sb2.GetMcpServers()) != 0 {
		t.Errorf("empty set: expected no mcp activity; loads %v static %v", runner.mcpLoads, runner.lastStaticMcp)
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "skipped stale mark gone") {
		t.Errorf("stale marks should be reported in the launch log; got:\n%s", joined)
	}
}

func TestRestartAndRefreshNeverReapplyMcp(t *testing.T) {
	m, reg, runner, dir := newTestManager(t)
	sb, _ := launchWithMcp(t, m, dir, &McpAttach{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE, Servers: []string{"notion"}})
	loads := len(runner.mcpLoads)
	if _, err := m.Stop(context.Background(), sb.GetId()); err != nil {
		t.Fatal(err)
	}
	runner.failStart = true // force the relaunch branch of bringUp
	if _, err := m.Restart(context.Background(), sb.GetId(), nil); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if _, err := m.Refresh(context.Background(), sb.GetId(), nil, nil); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(runner.mcpLoads) != loads || len(runner.lastStaticMcp) != 0 {
		t.Errorf("restart/refresh must not re-apply marks: loads %v static %v", runner.mcpLoads, runner.lastStaticMcp)
	}
	got, err := reg.Get(sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.GetMcpServers(), ",") != "notion" || got.GetMcpAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Errorf("record fields must persist unchanged; got %v / %v", got.GetMcpServers(), got.GetMcpAttachMode())
	}
}

// --- FR-102: attaching a registered server to an EXISTING sandbox ---

func TestAttachMcpLoadsAppendsRecordAndEmits(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	sb, _ := launchWithMcp(t, m, dir, nil) // launched with no marks: no servers recorded
	if len(sb.GetMcpServers()) != 0 || len(runner.mcpLoads) != 0 {
		t.Fatalf("precondition: launch attached %v / loads %v", sb.GetMcpServers(), runner.mcpLoads)
	}
	ctx := context.Background()
	out, err := m.AttachMcp(ctx, sb.GetId(), " notion ", nil)
	if err != nil {
		t.Fatalf("AttachMcp: %v", err)
	}
	if want := sb.GetContainerRef() + " notion"; len(runner.mcpLoads) != 1 || runner.mcpLoads[0] != want {
		t.Errorf("loads = %v, want [%s]", runner.mcpLoads, want)
	}
	if got := out.GetMcpServers(); len(got) != 1 || got[0] != "notion" {
		t.Errorf("record mcp_servers = %v, want [notion]", got)
	}
	if out.GetMcpAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Errorf("mode = %v, want ADDITIVE", out.GetMcpAttachMode())
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("an attach must not change the state; got %v", out.GetState())
	}
	persisted, _ := m.Get(sb.GetId())
	if got := persisted.GetMcpServers(); len(got) != 1 || got[0] != "notion" {
		t.Errorf("persisted mcp_servers = %v", got)
	}

	// A second server appends; the same name again is refused before any load.
	if _, err := m.AttachMcp(ctx, sb.GetId(), "linear", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AttachMcp(ctx, sb.GetId(), "notion", nil); !errors.Is(err, ErrMcpAlreadyAttached) {
		t.Errorf("duplicate attach err = %v, want ErrMcpAlreadyAttached", err)
	}
	if len(runner.mcpLoads) != 2 {
		t.Errorf("a refused duplicate must not load; loads = %v", runner.mcpLoads)
	}
	persisted, _ = m.Get(sb.GetId())
	if strings.Join(persisted.GetMcpServers(), ",") != "notion,linear" {
		t.Errorf("record = %v", persisted.GetMcpServers())
	}
}

func TestAttachMcpFailuresLeaveRecordUntouched(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	sb, _ := launchWithMcp(t, m, dir, nil)
	ctx := context.Background()

	runner.failLoad = map[string]bool{"ghost": true}
	if _, err := m.AttachMcp(ctx, sb.GetId(), "ghost", nil); err == nil {
		t.Fatal("a failed load must surface")
	}
	if _, err := m.AttachMcp(ctx, sb.GetId(), "", nil); err == nil {
		t.Error("an empty name must be refused")
	}
	if _, err := m.AttachMcp(ctx, "no-such-sandbox", "notion", nil); err == nil {
		t.Error("an unknown sandbox must be refused")
	}
	// Busy: another operation holds the latch.
	if err := m.ops.acquire(sb.GetId(), opRefresh); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AttachMcp(ctx, sb.GetId(), "notion", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("busy err = %v, want ErrBusy", err)
	}
	m.ops.release(sb.GetId())
	persisted, _ := m.Get(sb.GetId())
	if len(persisted.GetMcpServers()) != 0 {
		t.Errorf("failures must not touch the record; got %v", persisted.GetMcpServers())
	}

	// STOPPED is eligible (the runtime starts the sandbox on demand).
	if _, err := m.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}
	out, err := m.AttachMcp(ctx, sb.GetId(), "notion", nil)
	if err != nil {
		t.Fatalf("attach on a stopped sandbox: %v", err)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_STOPPED || len(out.GetMcpServers()) != 1 {
		t.Errorf("stopped attach: state %v servers %v", out.GetState(), out.GetMcpServers())
	}
}

func TestAttachableState(t *testing.T) {
	for st, ok := range map[pb.SandboxState]bool{
		pb.SandboxState_SANDBOX_STATE_RUNNING:  true,
		pb.SandboxState_SANDBOX_STATE_STOPPED:  true,
		pb.SandboxState_SANDBOX_STATE_CREATING: false,
		pb.SandboxState_SANDBOX_STATE_ERROR:    false,
	} {
		err := attachableState(st)
		if (err == nil) != ok {
			t.Errorf("attachableState(%v) = %v, want ok=%v", st, err, ok)
		}
		if err != nil && !errors.Is(err, ErrIneligibleState) {
			t.Errorf("%v: err should wrap ErrIneligibleState: %v", st, err)
		}
	}
}
