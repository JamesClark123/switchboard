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
	// feature 009: the attached servers are named on the row's third line.
	if !strings.Contains(v, "attached pw") || !strings.Contains(v, "mcp: notion, pw") {
		t.Errorf("overlay should confirm and the row's third line should update; got:\n%s", v)
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

// runDetach runs the start command's batch (detach RPC + spinner tick) and returns
// the detach outcome it yields.
func runDetach(t *testing.T, cmd tea.Cmd) sandboxMcpDetachedMsg {
	t.Helper()
	msg := runCmd(cmd)
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if r, ok := runCmd(c).(sandboxMcpDetachedMsg); ok {
				return r
			}
		}
	}
	if r, ok := msg.(sandboxMcpDetachedMsg); ok {
		return r
	}
	t.Fatalf("expected sandboxMcpDetachedMsg, got %T", msg)
	return sandboxMcpDetachedMsg{}
}

func (f *fakeDaemon) DetachMcpServer(_ context.Context, id, name string) (*pb.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detachMcpErr != nil {
		return nil, f.detachMcpErr
	}
	if f.detachedMcp == nil {
		f.detachedMcp = map[string][]string{}
	}
	f.detachedMcp[id] = append(f.detachedMcp[id], name)
	for _, sb := range f.sandboxes {
		if sb.GetId() == id {
			var keep []string
			for _, n := range sb.McpServers {
				if n != name {
					keep = append(keep, n)
				}
			}
			sb.McpServers = keep
			return proto.Clone(sb).(*pb.Sandbox), nil
		}
	}
	return &pb.Sandbox{Id: id}, nil
}

// SWI-3: `d` asks for confirmation, then detaches the highlighted server; the
// record and the row update and the overlay returns.
func TestMcpServersDetachFlow(t *testing.T) {
	d := &fakeDaemon{}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, cmd := update(m, press("d"))
	if m.screen != screenConfirm || cmd != nil {
		t.Fatalf("d should open the confirm dialog; screen %v", m.screen)
	}
	if v := m.View(); !strings.Contains(v, "Detach notion?") || !strings.Contains(v, "stays registered") {
		t.Errorf("confirm should name the server and say it stays registered; got:\n%s", v)
	}
	// Cancelling returns to the overlay and detaches nothing.
	m2, _ := update(m, press("n"))
	if m2.screen != screenSandboxMcp || len(d.detachedMcp) != 0 {
		t.Fatalf("cancel: screen %v, detaches %v", m2.screen, d.detachedMcp)
	}

	m, cmd = update(m, press("y"))
	if m.screen != screenSandboxMcp || cmd == nil {
		t.Fatalf("accept should return to the overlay and dispatch; screen %v", m.screen)
	}
	m, cmd = update(m, runCmd(cmd)) // the confirmation announces the start
	if m.sandboxMcpView.busy != "notion" || m.sandboxMcpView.busyKind != "detach" || !strings.Contains(m.View(), "detaching notion") {
		t.Fatalf("detach should mark the overlay busy with a spinner line; busy %q kind %q", m.sandboxMcpView.busy, m.sandboxMcpView.busyKind)
	}
	m, _ = update(m, runDetach(t, cmd))
	if got := d.detachedMcp["sb-1"]; len(got) != 1 || got[0] != "notion" {
		t.Errorf("daemon detach calls = %v, want [notion]", got)
	}
	if len(m.sandboxMcpView.attached) != 0 {
		t.Errorf("overlay attached = %v, want none", m.sandboxMcpView.attached)
	}
	v := m.View()
	if !strings.Contains(v, "detached notion") || !strings.Contains(v, "none attached") {
		t.Errorf("overlay should confirm and show an empty list; got:\n%s", v)
	}
	// Nothing left to detach.
	m, _ = update(m, press("d"))
	if m.screen != screenSandboxMcp || !strings.Contains(m.sandboxMcpView.status, "nothing to detach") {
		t.Errorf("d on an empty list: screen %v status %q", m.screen, m.sandboxMcpView.status)
	}
}

func TestMcpServersDetachFailureKeepsRecordAndShowsDiagnostic(t *testing.T) {
	d := &fakeDaemon{detachMcpErr: errors.New("gateway unreachable")}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	m, _ = update(m, runDetach(t, cmd))
	if got := m.sandboxMcpView.attached; strings.Join(got, ",") != "notion" {
		t.Errorf("a failed detach must keep the record; attached = %v", got)
	}
	if v := m.View(); !strings.Contains(v, "detach notion failed: gateway unreachable") {
		t.Errorf("overlay should show the diagnostic; got:\n%s", v)
	}
}

// PR #3 review: confirm text is neutral (not a red "keeps running"), and a detach
// in progress blocks a second d, a, and the picker's enter.
func TestMcpServersDetachConfirmIsNeutralAndGuardsReentrancy(t *testing.T) {
	d := &fakeDaemon{}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, _ = update(m, press("d"))
	v := m.View()
	if strings.Contains(v, "keeps running") || strings.Contains(v, "cannot be undone") || !strings.Contains(v, "not restarted") {
		t.Errorf("confirm should carry the neutral notice only; got:\n%s", v)
	}
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	if cmd == nil || m.sandboxMcpView.busyKind != "detach" {
		t.Fatal("precondition: detach in flight")
	}
	// While it runs: d and a are refused with the right wording and dispatch nothing.
	for _, key := range []string{"d", "a"} {
		var c tea.Cmd
		m, c = update(m, press(key))
		if c != nil || m.screen != screenSandboxMcp || m.sandboxMcpView.status != "a detach is already in progress" {
			t.Errorf("%s while detaching: cmd %v screen %v status %q", key, c != nil, m.screen, m.sandboxMcpView.status)
		}
	}
	// A late second confirmation is also ignored.
	m, c := update(m, sandboxMcpDetachStartMsg{id: "sb-1", host: m.sandboxMcpView.host, name: "notion"})
	if c != nil || len(d.detachedMcp["sb-1"]) != 0 {
		t.Error("a second start while busy must not dispatch")
	}
	m, _ = update(m, runDetach(t, cmd))
	if m.sandboxMcpView.busy != "" || m.sandboxMcpView.busyKind != "" {
		t.Error("busy must clear when the reply arrives")
	}
	if len(d.detachedMcp["sb-1"]) != 1 {
		t.Errorf("exactly one detach expected; got %v", d.detachedMcp)
	}
}

// A reply for a server that is no longer registered says so instead of claiming
// it is still registered; a reply after the overlay closed lands on the footer.
func TestMcpServersDetachStatusVariants(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{{Name: "pw", Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Transport: "stdio"}}}
	m := openMcpOverlay(t, mcpOverlayModel(t, d)) // notion attached but NOT registered
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	m, reload := update(m, runDetach(t, cmd))
	m, _ = update(m, runCmd(reload)) // the wording waits for the refreshed list
	if st := m.sandboxMcpView.status; !strings.Contains(st, "no longer registered") || !strings.Contains(st, "may still have its tools loaded") || strings.Contains(st, "still registered") {
		t.Errorf("status = %q", st)
	}

	d2 := &fakeDaemon{}
	m2 := openMcpOverlay(t, mcpOverlayModel(t, d2))
	m2, _ = update(m2, press("d"))
	m2, cmd2 := update(m2, press("y"))
	m2, cmd2 = update(m2, runCmd(cmd2))
	m2, _ = update(m2, tea.KeyMsg{Type: tea.KeyEsc}) // close the overlay while the detach runs
	if m2.screen != screenList {
		t.Fatalf("overlay should have closed; screen %v", m2.screen)
	}
	m2, _ = update(m2, runDetach(t, cmd2))
	if !strings.Contains(m2.status, "detached notion from") {
		t.Errorf("late reply should land on the footer; status %q", m2.status)
	}
}

// Review (PR #3, run 34): in-flight calls live on the model, so closing and
// reopening the overlay mid-detach still shows it busy and refuses a second call.
func TestMcpServersReopenedOverlayStillShowsADetachInFlight(t *testing.T) {
	d := &fakeDaemon{}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd)) // detach dispatched, RPC not yet answered
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Fatalf("overlay should have closed; screen %v", m.screen)
	}
	m = openMcpOverlay(t, m)
	if m.sandboxMcpView.busy != "notion" || m.sandboxMcpView.busyKind != "detach" || !strings.Contains(m.View(), "detaching notion") {
		t.Fatalf("a reopened overlay must still show the detach; busy %q kind %q", m.sandboxMcpView.busy, m.sandboxMcpView.busyKind)
	}
	m, c := update(m, press("d"))
	if c != nil || m.screen != screenSandboxMcp || m.sandboxMcpView.status != "a detach is already in progress" {
		t.Errorf("d on a reopened busy overlay: cmd %v screen %v status %q", c != nil, m.screen, m.sandboxMcpView.status)
	}
	m, _ = update(m, runDetach(t, cmd))
	if m.sandboxMcpView.busy != "" || len(m.sandboxMcpOps) != 0 {
		t.Errorf("the reply must clear the busy state and the in-flight record; ops %v", m.sandboxMcpOps)
	}
}

// After a detach the daemon's list is refreshed, and nothing is claimed about
// registration when that list was never loaded.
func TestMcpServersDetachRefreshesTheListAndStaysNeutralWhenItIsUnknown(t *testing.T) {
	d := &fakeDaemon{}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	m, reload := update(m, runDetach(t, cmd))
	if reload == nil || !m.sandboxMcpView.loading {
		t.Error("a detach should refresh the daemon's list")
	}

	m2 := openMcpOverlay(t, mcpOverlayModel(t, &fakeDaemon{}))
	m2.sandboxMcpView.registered = nil // the list never loaded
	m2, _ = update(m2, press("d"))
	m2, cmd2 := update(m2, press("y"))
	m2, cmd2 = update(m2, runCmd(cmd2))
	m2, _ = update(m2, runDetach(t, cmd2))
	if st := m2.sandboxMcpView.status; st != "detached notion" {
		t.Errorf("with an unknown list the status must claim nothing about registration; got %q", st)
	}
}

// Review (PR #3, run 42): the registration claim comes from the list refreshed
// AFTER the detach, not the one loaded before it.
func TestMcpServersDetachStatusUsesTheRefreshedList(t *testing.T) {
	d := &fakeDaemon{} // notion registered when the overlay opens
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	m, reload := update(m, runDetach(t, cmd))
	if st := m.sandboxMcpView.status; st != "detached notion" {
		t.Errorf("before the refreshed list the status must claim nothing; got %q", st)
	}
	d.mcpServers = d.mcpServers[1:] // notion was removed from the host meanwhile
	m, _ = update(m, runCmd(reload))
	if st := m.sandboxMcpView.status; !strings.Contains(st, "no longer registered") {
		t.Errorf("status = %q, want the refreshed list's answer", st)
	}

	m2 := openMcpOverlay(t, mcpOverlayModel(t, &fakeDaemon{}))
	m2, _ = update(m2, press("d"))
	m2, cmd2 := update(m2, press("y"))
	m2, cmd2 = update(m2, runCmd(cmd2))
	m2, reload2 := update(m2, runDetach(t, cmd2))
	m2, _ = update(m2, runCmd(reload2))
	if st := m2.sandboxMcpView.status; !strings.Contains(st, "still registered on the daemon") {
		t.Errorf("status = %q, want still registered", st)
	}
}

// Review (PR #3, run 42): an attach/detach in flight is named on the sandbox's
// row, so a closed overlay does not hide why the daemon refuses other actions.
func TestMcpOpInFlightIsNamedOnTheSandboxRow(t *testing.T) {
	d := &fakeDaemon{}
	m := openMcpOverlay(t, mcpOverlayModel(t, d))
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if v := m.View(); !strings.Contains(v, "mcp detaching notion…") {
		t.Errorf("the row should name the detach in flight; got:\n%s", v)
	}
	m, _ = update(m, runDetach(t, cmd))
	if v := m.View(); strings.Contains(v, "mcp detaching") {
		t.Errorf("the marker must clear with the reply; got:\n%s", v)
	}
}

// Review (PR #3, run 42): the in-flight map is per sandbox — one sandbox's reply
// never clears another's call — and setMcpOp creates the map on first use.
func TestMcpOpsArePerSandbox(t *testing.T) {
	m := openMcpOverlay(t, mcpOverlayModel(t, &fakeDaemon{}))
	m.sandboxMcpOps = nil
	m.setMcpOp("sb-2", sandboxMcpOp{kind: "attach", name: "pw"})
	m, _ = update(m, sandboxMcpDetachedMsg{id: "sb-1", name: "notion", err: errors.New("gateway unreachable")})
	if op, ok := m.sandboxMcpOps["sb-2"]; !ok || op.name != "pw" {
		t.Errorf("sb-2's attach must survive sb-1's failed detach; ops %v", m.sandboxMcpOps)
	}
}

// Review (PR #3, run 50): a list that lands while the detach confirm is up is
// not dropped, so `loading` clears and the post-detach wording resolves.
func TestMcpServersListLandingWhileConfirmIsOpenIsKept(t *testing.T) {
	d := &fakeDaemon{}
	m := mcpOverlayModel(t, d)
	d.sandboxes[0].McpServers = []string{"notion", "pw"}
	m = openMcpOverlay(t, withSandboxes(m, d.sandboxes))
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	m, reload := update(m, runDetach(t, cmd))
	m, _ = update(m, press("d")) // confirm for pw opens before the refreshed list lands
	if m.screen != screenConfirm {
		t.Fatalf("screen = %v, want the confirm", m.screen)
	}
	m, _ = update(m, runCmd(reload))
	m, _ = update(m, press("n"))
	v := m.sandboxMcpView
	if v.loading || v.detached != "" || !strings.Contains(v.status, "detached notion — still registered") {
		t.Errorf("loading %v detached %q status %q, want the list applied", v.loading, v.detached, v.status)
	}
	if _, c := update(m, press("r")); c == nil {
		t.Error("r must reload again once the list landed")
	}
}
