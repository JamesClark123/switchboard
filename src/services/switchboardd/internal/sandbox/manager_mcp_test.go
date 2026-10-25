package sandbox

import (
	"context"
	"errors"
	"fmt"
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

// SWI-2: the runtime's flat tool namespace refuses a load whose tools clash with
// a loaded server. Launch and attach turn that into an actionable message and
// keep the raw runtime text.
const toolCollisionText = `tool "list_issues" from server "linear" collides with an existing tool already registered for server "github"
tool "list_releases" from server "linear" collides with an existing tool already registered for server "github"`

func TestLaunchLogsActionableToolCollisionHint(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	runner.loadErr = map[string]string{"linear": toolCollisionText}
	sb, logs := launchWithMcp(t, m, dir, &McpAttach{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE, Servers: []string{"github", "linear"}})
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("a collision must not fail the launch; state = %v", sb.GetState())
	}
	if got := sb.GetMcpServers(); len(got) != 1 || got[0] != "github" {
		t.Errorf("record = %v, want [github]", got)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"mcp: skipped linear: tool name collision with github (list_issues, list_releases)", "README › MCP tool collisions", "mcp: runtime said:", `collides with an existing tool`} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q; got:\n%s", want, joined)
		}
	}
}

func TestAttachMcpToolCollisionIsAFailedPreconditionWithHint(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	sb, _ := launchWithMcp(t, m, dir, nil)
	runner.loadErr = map[string]string{"linear": toolCollisionText}
	_, err := m.AttachMcp(context.Background(), sb.GetId(), "linear", nil)
	if !errors.Is(err, ErrMcpToolCollision) {
		t.Fatalf("err = %v, want ErrMcpToolCollision", err)
	}
	for _, want := range []string{"collision with github (list_issues, list_releases)", "runtime:", "collides with an existing tool"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	persisted, _ := m.Get(sb.GetId())
	if len(persisted.GetMcpServers()) != 0 {
		t.Errorf("a collision must leave the record untouched; got %v", persisted.GetMcpServers())
	}
}

// manyCollisions is the diagnostic for n clashing tools — long enough that the
// runner's error keeps only its tail (outputTail), as for large toolsets.
func manyCollisions(n int) (string, []string) {
	var lines, tools []string
	for i := 0; i < n; i++ {
		tool := fmt.Sprintf("tool_%02d", i)
		tools = append(tools, tool)
		lines = append(lines, `tool "`+tool+`" from server "gitlab" collides with an existing tool already registered for server "github"`)
	}
	return strings.Join(lines, "\n"), tools
}

// SWI-2 review: the hint is built from the full streamed output, not the
// truncated error, so every clashing tool is named.
func TestToolCollisionHintNamesEveryToolDespiteTruncatedError(t *testing.T) {
	text, tools := manyCollisions(8)
	m, _, runner, dir := newTestManager(t)
	runner.loadErr = map[string]string{"gitlab": text}
	_, logs := launchWithMcp(t, m, dir, &McpAttach{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE, Servers: []string{"github", "gitlab"}})
	joined := strings.Join(logs, "\n")
	want := "tool name collision with github (" + strings.Join(tools, ", ") + ")"
	if !strings.Contains(joined, "mcp: skipped gitlab: "+want) {
		t.Errorf("launch log missing %q; got:\n%s", want, joined)
	}

	sb, _ := launchWithMcp(t, m, dir, nil)
	_, err := m.AttachMcp(context.Background(), sb.GetId(), "gitlab", nil)
	if !errors.Is(err, ErrMcpToolCollision) || !strings.Contains(err.Error(), want) {
		t.Errorf("attach err = %v, want ErrMcpToolCollision naming %q", err, want)
	}
}

// SWI-2 review: a collision on the first handle is reported as is; the next
// handle is not tried (its unrelated failure would mask the collision).
func TestAttachMcpToolCollisionIsNotMaskedByTheNextRef(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	sb, _ := launchWithMcp(t, m, dir, nil)
	refs := sbxRefs(sb)
	if len(refs) != 2 {
		t.Fatalf("want a sandbox with a display name and an id; refs = %v", refs)
	}
	runner.loadErrRef = map[string]string{
		refs[0] + " linear": toolCollisionText,
		refs[1] + " linear": "no such sandbox " + refs[1],
	}
	runner.mcpLoads = nil
	_, err := m.AttachMcp(context.Background(), sb.GetId(), "linear", nil)
	if !errors.Is(err, ErrMcpToolCollision) || !strings.Contains(err.Error(), "collision with github (list_issues, list_releases)") {
		t.Fatalf("err = %v, want ErrMcpToolCollision with the hint", err)
	}
	if len(runner.mcpLoads) != 1 {
		t.Errorf("loads = %v, want only the first ref tried", runner.mcpLoads)
	}
}

// SWI-2 review: in exclusive mode the clash fails `sbx create`; the launch
// error and log name it and the remedy.
func TestLaunchExclusiveToolCollisionIsNamed(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	runner.failNext = fmt.Errorf("sbx create: exit status 1%s", outputTail([]byte(toolCollisionText)))
	runner.launchOut = toolCollisionText
	src := makeSource(t, dir, "proj-exclusive")
	var logs []string
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "mcp-exclusive"},
		Sources: []*pb.SourceRef{src},
		Mcp:     &McpAttach{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE, Servers: []string{"github", "linear"}},
	}, nil, func(l string) { logs = append(logs, l) })
	if !errors.Is(err, ErrMcpToolCollision) {
		t.Fatalf("err = %v, want ErrMcpToolCollision", err)
	}
	for _, want := range []string{"collision with github (list_issues, list_releases)", "additive mode skips the clashing server instead", "runtime:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_ERROR || !strings.Contains(sb.GetError(), "collision with github") {
		t.Errorf("record = %v / %q, want ERROR carrying the hint", sb.GetState(), sb.GetError())
	}
	if !strings.Contains(strings.Join(logs, "\n"), "mcp: MCP tool name collision: tool name collision with github") {
		t.Errorf("launch log missing the hint; got:\n%s", strings.Join(logs, "\n"))
	}
}
