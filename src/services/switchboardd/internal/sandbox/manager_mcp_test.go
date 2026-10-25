package sandbox

import (
	"context"
	"errors"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
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

// --- SWI-3: detaching a server from an EXISTING sandbox ---

func attachedSandbox(t *testing.T, names ...string) (*Manager, *fakeRunner, *pb.Sandbox) {
	t.Helper()
	m, _, runner, dir := newTestManager(t)
	sb, _ := launchWithMcp(t, m, dir, nil)
	for _, n := range names {
		if _, err := m.AttachMcp(context.Background(), sb.GetId(), n, nil); err != nil {
			t.Fatal(err)
		}
	}
	return m, runner, sb
}

func TestDetachMcpUnloadsDropsRecordAndEmits(t *testing.T) {
	m, runner, sb := attachedSandbox(t, "notion", "linear")
	out, err := m.DetachMcp(context.Background(), sb.GetId(), " notion ", nil)
	if err != nil {
		t.Fatalf("DetachMcp: %v", err)
	}
	if want := sb.GetContainerRef() + " notion"; len(runner.mcpUnloads) != 1 || runner.mcpUnloads[0] != want {
		t.Errorf("unloads = %v, want [%s]", runner.mcpUnloads, want)
	}
	if got := strings.Join(out.GetMcpServers(), ","); got != "linear" {
		t.Errorf("record = %q, want linear", got)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("a detach must not change the state; got %v", out.GetState())
	}
	persisted, _ := m.Get(sb.GetId())
	if got := strings.Join(persisted.GetMcpServers(), ","); got != "linear" {
		t.Errorf("persisted = %q", got)
	}
	// Re-attachable afterwards.
	if _, err := m.AttachMcp(context.Background(), sb.GetId(), "notion", nil); err != nil {
		t.Errorf("re-attach after detach: %v", err)
	}
}

func TestDetachMcpRefusals(t *testing.T) {
	m, runner, sb := attachedSandbox(t, "notion")
	ctx := context.Background()
	if _, err := m.DetachMcp(ctx, sb.GetId(), "", nil); err == nil {
		t.Error("an empty name must be refused")
	}
	if _, err := m.DetachMcp(ctx, sb.GetId(), "ghost", nil); !errors.Is(err, ErrMcpNotAttached) {
		t.Errorf("unrecorded name err = %v, want ErrMcpNotAttached", err)
	}
	if _, err := m.DetachMcp(ctx, sb.GetId(), mcp.WorkerServerPrefix+"abcd1234", nil); !errors.Is(err, ErrMcpReserved) {
		t.Errorf("orchestrator server err = %v, want ErrMcpReserved", err)
	}
	if _, err := m.DetachMcp(ctx, "no-such-sandbox", "notion", nil); err == nil {
		t.Error("an unknown sandbox must be refused")
	}
	if err := m.ops.acquire(sb.GetId(), opRefresh); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DetachMcp(ctx, sb.GetId(), "notion", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("busy err = %v, want ErrBusy", err)
	}
	m.ops.release(sb.GetId())
	if len(runner.mcpUnloads) != 0 {
		t.Errorf("refusals must not call the CLI; unloads = %v", runner.mcpUnloads)
	}
	persisted, _ := m.Get(sb.GetId())
	if got := strings.Join(persisted.GetMcpServers(), ","); got != "notion" {
		t.Errorf("refusals must not touch the record; got %q", got)
	}
}

func TestDetachMcpFailureKeepsRecordButNotLoadedDropsIt(t *testing.T) {
	m, runner, sb := attachedSandbox(t, "notion", "linear")
	ctx := context.Background()

	runner.unloadErr = map[string]string{"notion": "gateway unreachable"}
	if _, err := m.DetachMcp(ctx, sb.GetId(), "notion", nil); err == nil || !strings.Contains(err.Error(), "gateway unreachable") {
		t.Fatalf("a failed unload must surface the runtime text; err = %v", err)
	}
	persisted, _ := m.Get(sb.GetId())
	if got := strings.Join(persisted.GetMcpServers(), ","); got != "notion,linear" {
		t.Errorf("a failed unload must keep the record; got %q", got)
	}

	// The runtime no longer has it loaded: the record follows the runtime.
	runner.unloadErr = map[string]string{"notion": `MCP server "notion" is not loaded in sandbox "x"`}
	var logs []string
	out, err := m.DetachMcp(ctx, sb.GetId(), "notion", func(l string) { logs = append(logs, l) })
	if err != nil {
		t.Fatalf("not-loaded should be tolerated: %v", err)
	}
	if got := strings.Join(out.GetMcpServers(), ","); got != "linear" {
		t.Errorf("record = %q, want linear", got)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "was not loaded") {
		t.Errorf("tolerated case should be logged; got %v", logs)
	}
}

func TestDetachMcpRefusedWhenSandboxIsNotRunningOrStopped(t *testing.T) {
	m, runner, sb := attachedSandbox(t, "notion")
	if _, err := m.store.Update(sb.GetId(), func(s *pb.Sandbox) error {
		s.State = pb.SandboxState_SANDBOX_STATE_ERROR
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DetachMcp(context.Background(), sb.GetId(), "notion", nil); !errors.Is(err, ErrIneligibleState) {
		t.Errorf("err = %v, want ErrIneligibleState", err)
	}
	if len(runner.mcpUnloads) != 0 {
		t.Error("an ineligible sandbox must not reach the CLI")
	}
}

// PR #3 review: the tolerated cases are narrow, visible in the log, and the
// sandbox may be stopped.
func TestDetachMcpUnregisteredIsDroppedWithAWarning(t *testing.T) {
	m, runner, sb := attachedSandbox(t, "notion")
	runner.unloadErr = map[string]string{"notion": `MCP server "notion" is not registered (run 'sbx mcp add notion ...')`}
	var logs []string
	out, err := m.DetachMcp(context.Background(), sb.GetId(), "notion", func(l string) { logs = append(logs, l) })
	if err != nil {
		t.Fatalf("a server removed from the host must be detachable from the record: %v", err)
	}
	if len(out.GetMcpServers()) != 0 {
		t.Errorf("record = %v", out.GetMcpServers())
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "no longer registered on this host") || !strings.Contains(joined, "may still have it loaded") {
		t.Errorf("the unregistered case must be warned about; logs:\n%s", joined)
	}
}

func TestDetachMcpOtherErrorsAreNotTolerated(t *testing.T) {
	m, runner, sb := attachedSandbox(t, "notion")
	// "unknown"/"not found" wording is NOT a not-loaded signal.
	for _, msg := range []string{"unknown flag: --sandbox", "sandbox not found", "gateway unreachable"} {
		runner.unloadErr = map[string]string{"notion": msg}
		if _, err := m.DetachMcp(context.Background(), sb.GetId(), "notion", nil); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q must surface; err = %v", msg, err)
		}
	}
	persisted, _ := m.Get(sb.GetId())
	if got := strings.Join(persisted.GetMcpServers(), ","); got != "notion" {
		t.Errorf("failed detaches must keep the record; got %q", got)
	}
}

func TestDetachMcpWorksOnAStoppedSandbox(t *testing.T) {
	m, runner, sb := attachedSandbox(t, "notion")
	if _, err := m.Stop(context.Background(), sb.GetId()); err != nil {
		t.Fatal(err)
	}
	out, err := m.DetachMcp(context.Background(), sb.GetId(), "notion", nil)
	if err != nil {
		t.Fatalf("detach on a STOPPED sandbox: %v", err)
	}
	if len(out.GetMcpServers()) != 0 || out.GetState() != pb.SandboxState_SANDBOX_STATE_STOPPED || len(runner.mcpUnloads) != 1 {
		t.Errorf("record %v state %v unloads %v", out.GetMcpServers(), out.GetState(), runner.mcpUnloads)
	}
}

func TestAttachAndDetachRefuseNamesThatCouldBeFlags(t *testing.T) {
	m, runner, sb := attachedSandbox(t, "notion")
	for _, bad := range []string{"--help", "-x", "a b", "a;b"} {
		if _, err := m.AttachMcp(context.Background(), sb.GetId(), bad, nil); !errors.Is(err, ErrMcpInvalidName) {
			t.Errorf("attach %q err = %v, want ErrMcpInvalidName", bad, err)
		}
		if _, err := m.DetachMcp(context.Background(), sb.GetId(), bad, nil); !errors.Is(err, ErrMcpInvalidName) {
			t.Errorf("detach %q err = %v, want ErrMcpInvalidName", bad, err)
		}
	}
	if len(runner.mcpUnloads) != 0 {
		t.Errorf("invalid names must not reach the CLI; unloads = %v", runner.mcpUnloads)
	}
}
