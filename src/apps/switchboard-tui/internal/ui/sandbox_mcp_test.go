package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

func (f *fakeDaemon) AttachMcpServer(_ context.Context, id, name string) (*pb.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attachMcpErr != nil {
		return nil, f.attachMcpErr
	}
	if f.attachedMcp == nil {
		f.attachedMcp = map[string][]string{}
	}
	f.attachedMcp[id] = append(f.attachedMcp[id], name)
	for _, sb := range f.sandboxes {
		if sb.GetId() == id {
			sb.McpServers = append(sb.McpServers, name)
			if sb.McpAttachMode == pb.McpAttachMode_MCP_ATTACH_MODE_UNSPECIFIED {
				sb.McpAttachMode = pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE
			}
			return proto.Clone(sb).(*pb.Sandbox), nil
		}
	}
	return &pb.Sandbox{Id: id, McpServers: []string{name}}, nil
}

// runAttach runs the enter command (a batch with the spinner tick) and returns
// the attach outcome it yields.
func runAttach(t *testing.T, cmd tea.Cmd) sandboxMcpAttachedMsg {
	t.Helper()
	msg := runCmd(cmd)
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if r, ok := runCmd(c).(sandboxMcpAttachedMsg); ok {
				return r
			}
		}
	}
	if r, ok := msg.(sandboxMcpAttachedMsg); ok {
		return r
	}
	t.Fatalf("expected sandboxMcpAttachedMsg, got %T", msg)
	return sandboxMcpAttachedMsg{}
}

// mcpOverlayModel is a sized list with one sandbox that has `notion` attached,
// on a daemon that registers notion (unauthorized) and pw (local).
func mcpOverlayModel(t *testing.T, d *fakeDaemon) Model {
	t.Helper()
	d.mcpAvail = true
	if d.mcpServers == nil {
		d.mcpServers = []*pb.McpServer{notionServer(), {Name: "pw", Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Transport: "stdio", AuthState: pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE}}
	}
	m, _ := sourcesModel(t, d)
	d.sandboxes[0].McpServers = []string{"notion"}
	d.sandboxes[0].McpAttachMode = pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE
	return withSandboxes(m, d.sandboxes)
}

// openMcpOverlay presses M and drains the daemon's list reply.
func openMcpOverlay(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(m, press("M"))
	if m.screen != screenSandboxMcp {
		t.Fatalf("screen = %v, want screenSandboxMcp", m.screen)
	}
	if cmd != nil {
		if msg := runCmd(cmd); msg != nil {
			m, _ = update(m, msg)
		}
	}
	return m
}

// `M` opens the overlay listing the attached servers with the daemon's kind and
// auth state; esc closes it and the list stays visible behind it.
func TestMcpServersKeyOpensOverlayListingAttached(t *testing.T) {
	d := &fakeDaemon{}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	v := m.View()
	for _, want := range []string{"MCP servers · feature-work", "additive", "notion", "remote", "unauthorized", "needs authorization", "Switchboard"} {
		if !strings.Contains(v, want) {
			t.Errorf("overlay should show %q; got:\n%s", want, v)
		}
	}
	if strings.Contains(v, "none attached") {
		t.Error("a sandbox with a server attached must not say none attached")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should close the overlay")
	}
}

// `a` offers only the registered servers not yet attached; enter attaches the
// highlighted one, the record and the row update, and the overlay says so.
func TestMcpServersAttachFlow(t *testing.T) {
	d := &fakeDaemon{}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, _ = update(m, press("a"))
	if !m.sandboxMcpView.picking {
		t.Fatal("a should open the picker")
	}
	cands := m.sandboxMcpView.mcpCandidates()
	if len(cands) != 1 || cands[0].GetName() != "pw" {
		t.Fatalf("candidates = %v, want only pw (notion is already attached)", cands)
	}
	if v := m.View(); !strings.Contains(v, "Attach a registered server") || !strings.Contains(v, "stdio") {
		t.Errorf("picker should list pw with its transport; got:\n%s", v)
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.sandboxMcpView.busy != "pw" {
		t.Fatal("enter should dispatch the attach and mark it busy")
	}
	m, _ = update(m, runAttach(t, cmd))
	if got := d.attachedMcp["sb-1"]; len(got) != 1 || got[0] != "pw" {
		t.Errorf("daemon attach calls = %v, want [pw]", got)
	}
	if m.sandboxMcpView.busy != "" || m.sandboxMcpView.picking {
		t.Error("the overlay should return to the list after the attach")
	}
	if got := m.sandboxMcpView.attached; strings.Join(got, ",") != "notion,pw" {
		t.Errorf("overlay attached = %v", got)
	}
	v := m.View()
	if !strings.Contains(v, "attached pw") || !strings.Contains(v, "mcp: notion, pw (additive)") {
		t.Errorf("overlay should confirm and the row badge should update; got:\n%s", v)
	}
	// Everything registered is attached now: a says so instead of opening a picker.
	m, _ = update(m, press("a"))
	if m.sandboxMcpView.picking || !strings.Contains(m.sandboxMcpView.status, "already attached") {
		t.Errorf("a with no candidates: picking=%v status=%q", m.sandboxMcpView.picking, m.sandboxMcpView.status)
	}
}

// A failed attach keeps the record and names the daemon's diagnostic.
func TestMcpServersAttachFailureIsSurfaced(t *testing.T) {
	d := &fakeDaemon{attachMcpErr: errors.New("operation in progress for this sandbox")}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, _ = update(m, press("a"))
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, runAttach(t, cmd))
	if got := m.sandboxMcpView.attached; strings.Join(got, ",") != "notion" {
		t.Errorf("a failure must not touch the overlay's record: %v", got)
	}
	if v := m.View(); !strings.Contains(v, "attach pw failed: operation in progress") {
		t.Errorf("the diagnostic should be shown; got:\n%s", v)
	}
}

// Without a manageable gateway the overlay still shows the record, says why no
// attach is possible, and refuses a.
func TestMcpServersOverlayWithoutGateway(t *testing.T) {
	d := &fakeDaemon{mcpReason: "sbx mcp is not usable here"}
	m := mcpOverlayModel(t, d)
	d.mcpAvail = false
	m, cmd := update(m, press("M"))
	if cmd != nil {
		t.Error("no gateway: nothing to list")
	}
	v := m.View()
	if !strings.Contains(v, "notion") || !strings.Contains(v, "not usable") {
		t.Errorf("overlay should show the record and the reason; got:\n%s", v)
	}
	m, _ = update(m, press("a"))
	if m.sandboxMcpView.picking || !strings.HasPrefix(m.sandboxMcpView.status, "cannot attach") {
		t.Errorf("a should be refused: picking=%v status=%q", m.sandboxMcpView.picking, m.sandboxMcpView.status)
	}
}

// A list failure is shown inline, r retries, and a live sandbox update from the
// daemon reaches the open overlay.
func TestMcpServersOverlayListErrorAndSync(t *testing.T) {
	d := &fakeDaemon{mcpListErr: errors.New("boom")}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	if v := m.View(); !strings.Contains(v, "could not list the daemon's servers: boom") {
		t.Errorf("list error should be inline; got:\n%s", v)
	}
	m, _ = update(m, press("a"))
	if m.sandboxMcpView.picking {
		t.Error("a must not open the picker without a list")
	}
	d.mu.Lock()
	d.mcpListErr = nil
	d.mu.Unlock()
	m, cmd := update(m, press("r"))
	if cmd == nil {
		t.Fatal("r should reload")
	}
	m, _ = update(m, runCmd(cmd))
	if m.sandboxMcpView.listErr != "" || len(m.sandboxMcpView.registered) != 2 {
		t.Errorf("reload should land the list: err=%q registered=%d", m.sandboxMcpView.listErr, len(m.sandboxMcpView.registered))
	}
	// An attach made by another client arrives as a sandbox change.
	sb := proto.Clone(d.sandboxes[0]).(*pb.Sandbox)
	sb.McpServers = []string{"notion", "pw"}
	m.syncSandboxMcpOverlay(sb)
	if strings.Join(m.sandboxMcpView.attached, ",") != "notion,pw" {
		t.Errorf("sync should refresh the overlay: %v", m.sandboxMcpView.attached)
	}
	// A sync for another sandbox is ignored.
	other := &pb.Sandbox{Id: "sb-other", McpServers: []string{"x"}}
	m.syncSandboxMcpOverlay(other)
	if strings.Join(m.sandboxMcpView.attached, ",") != "notion,pw" {
		t.Error("a change on another sandbox must not touch the overlay")
	}
}

// Edge paths: picker navigation, a late list after the overlay closed, a result
// landing after the overlay closed, a busy attach, and the decoration of a server
// the host no longer registers.
func TestMcpServersOverlayEdgePaths(t *testing.T) {
	d := &fakeDaemon{}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))

	// Picker navigation clamps at both ends and esc returns to the list.
	m, _ = update(m, press("a"))
	m, _ = update(m, press("k"))
	m, _ = update(m, press("j"))
	m, _ = update(m, press("j"))
	if m.sandboxMcpView.pickCursor != 0 {
		t.Errorf("one candidate: pickCursor = %d, want 0", m.sandboxMcpView.pickCursor)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.sandboxMcpView.picking || m.screen != screenSandboxMcp {
		t.Error("esc in the picker should return to the overlay list")
	}
	// List navigation clamps too; a and r while loading are refused.
	m, _ = update(m, press("k"))
	m, _ = update(m, press("j"))
	if m.sandboxMcpView.cursor != 0 {
		t.Errorf("one attached: cursor = %d", m.sandboxMcpView.cursor)
	}
	m.sandboxMcpView.loading = true
	m, cmd := update(m, press("r"))
	if cmd != nil {
		t.Error("r while loading must not reload again")
	}
	m, _ = update(m, press("a"))
	if m.sandboxMcpView.picking || !strings.Contains(m.sandboxMcpView.status, "still loading") {
		t.Errorf("a while loading: %q", m.sandboxMcpView.status)
	}
	if !strings.Contains(m.View(), "loading the daemon") {
		t.Error("the loading line should render")
	}
	m.sandboxMcpView.loading = false

	// A busy attach refuses a second one and renders its spinner line.
	m.sandboxMcpView.busy = "pw"
	m, _ = update(m, press("a"))
	if !strings.Contains(m.sandboxMcpView.status, "already in progress") {
		t.Errorf("a while busy: %q", m.sandboxMcpView.status)
	}
	m.sandboxMcpView.picking = true
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter while busy must not dispatch")
	}
	if !strings.Contains(m.View(), "attaching pw") {
		t.Error("the busy line should render")
	}
	m.sandboxMcpView.busy = ""
	m.sandboxMcpView.picking = false

	// A server the host no longer registers is flagged on its row.
	m.sandboxMcpView.attached = []string{"notion", "gone"}
	if v := m.View(); !strings.Contains(v, "no longer registered") {
		t.Errorf("unregistered attached server should be flagged; got:\n%s", v)
	}
	// Before the list lands, rows show a placeholder.
	m.sandboxMcpView.registered = nil
	if v := m.View(); !strings.Contains(v, "…") {
		t.Error("rows should show a placeholder until the list lands")
	}

	// q closes; a late list or result for the closed overlay is dropped / goes
	// to the main status line without touching the (now empty) overlay state.
	m, _ = update(m, press("q"))
	if m.screen != screenList {
		t.Fatal("q should close the overlay")
	}
	m, _ = update(m, sandboxMcpListMsg{id: "sb-1", servers: d.mcpServers})
	if m.sandboxMcpView.registered != nil {
		t.Error("a late list must be dropped once the overlay is closed")
	}
	m, _ = update(m, sandboxMcpAttachedMsg{id: "sb-1", name: "pw", err: errors.New("late failure")})
	if !strings.Contains(m.status, "attach pw to feature-work failed: late failure") {
		t.Errorf("late failure should reach the main status: %q", m.status)
	}
	sb := proto.Clone(d.sandboxes[0]).(*pb.Sandbox)
	sb.McpServers = []string{"notion", "pw"}
	m, _ = update(m, sandboxMcpAttachedMsg{id: "sb-1", name: "pw", sb: sb})
	if !strings.Contains(m.status, "attached pw to feature-work") || !strings.Contains(m.View(), "mcp: notion, pw") {
		t.Errorf("late success should update the row and the main status: %q", m.status)
	}
	// Exclusive sandboxes render their mode and a daemon with no host label falls
	// back to the daemon's own id.
	d.sandboxes[0].McpAttachMode = pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE
	m = withSandboxes(m, d.sandboxes)
	m = openMcpOverlay(t, m)
	if !strings.Contains(m.View(), "exclusive") {
		t.Error("the overlay should show the exclusive mode")
	}
}
