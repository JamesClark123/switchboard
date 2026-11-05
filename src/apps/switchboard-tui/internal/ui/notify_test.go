package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

type recordNotifier struct{ titles, msgs []string }

func (r *recordNotifier) Notify(title, msg string) {
	r.titles = append(r.titles, title)
	r.msgs = append(r.msgs, msg)
}

func notif(id, sandbox string, kind pb.NotificationKind) *pb.Event {
	return &pb.Event{Event: &pb.Event_Notification{Notification: &pb.NotificationEvent{
		Id: id, SandboxId: sandbox, Kind: kind, Message: "msg-" + id,
	}}}
}

func TestNotificationEventUpdatesInboxBadgeAndDesktop(t *testing.T) {
	rn := &recordNotifier{}
	m := sized(New(&fakeDaemon{}, "/work").WithNotifier(rn))
	// Pretend a subscription is open so handleEvent re-arms safely.
	m.sub = &fakeStream{ctx: context.Background(), ch: nil}

	m, _ = update(m, eventMsg{ev: notif("evt-1", "sb1", pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE)})
	m, _ = update(m, eventMsg{ev: notif("evt-2", "sb2", pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING)})

	if len(m.inbox) != 2 || m.unread != 2 {
		t.Fatalf("inbox=%d unread=%d, want 2/2", len(m.inbox), m.unread)
	}
	// Newest first.
	if m.inbox[0].GetId() != "evt-2" {
		t.Errorf("inbox order wrong: %s", m.inbox[0].GetId())
	}
	// Desktop notifications were raised (FR-026a).
	if len(rn.titles) != 2 {
		t.Errorf("expected 2 desktop notifications, got %d", len(rn.titles))
	}
	if !strings.Contains(m.View(), "🔔 2") {
		t.Error("header should show the unread badge")
	}

	// Other event kinds don't add to the inbox.
	m, _ = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: &pb.Sandbox{Id: "sb1"}}}})
	m, _ = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: "sb1"}}}})
	if len(m.inbox) != 2 {
		t.Errorf("non-notification events should not grow inbox: %d", len(m.inbox))
	}
}

func TestNotificationsScreenAckAndNavigate(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work").WithNotifier(&recordNotifier{}),
		[]*pb.Sandbox{{Id: "sbA"}, {Id: "sbB"}, {Id: "sbC"}})
	m.inbox = []*pb.NotificationEvent{{Id: "n1", SandboxId: "sbC", Kind: pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE}}
	m.unread = 1

	// Open inbox -> marks read + acks.
	m, cmd := update(m, press("i"))
	if m.screen != screenNotifications || m.unread != 0 {
		t.Fatalf("inbox open: screen=%v unread=%d", m.screen, m.unread)
	}
	_ = runCmd(cmd) // ackVisibleCmd
	if len(d.ackedIDs) != 1 || d.ackedIDs[0] != "n1" {
		t.Errorf("expected ack of n1, got %v", d.ackedIDs)
	}
	if !strings.Contains(m.viewNotifications(), "task complete") {
		t.Error("notifications view should render the entry")
	}

	// Navigate to the notification's sandbox (sbC -> index 2).
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList || m.list.Index() != 2 {
		t.Errorf("navigate failed: screen=%v index=%d", m.screen, m.list.Index())
	}
}

func TestNotificationsNavAndEmpty(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m, cmd := update(m, press("i"))
	if m.screen != screenNotifications {
		t.Fatal("i should open notifications")
	}
	if runCmd(cmd) != nil {
		t.Error("empty inbox ack should be a no-op")
	}
	// j/k are safe on an empty inbox.
	m, _ = update(m, press("j"))
	m, _ = update(m, press("k"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should return to list")
	}
}

func TestSubscribeFlowAndError(t *testing.T) {
	// Buffered events so Recv returns immediately.
	d := &fakeDaemon{events: make(chan *pb.Event, 4)}
	d.events <- notif("e1", "sb1", pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE)
	m := New(d, "/work").WithNotifier(&recordNotifier{})

	cmd := m.subscribeCmd()
	opened, ok := runCmd(cmd).(subOpenedMsg)
	if !ok {
		t.Fatal("expected subOpenedMsg")
	}
	m, recv := update(m, opened)
	if m.sub == nil {
		t.Fatal("subscription stream not stored")
	}
	ev := runCmd(recv).(eventMsg)
	m, _ = update(m, ev)
	if len(m.inbox) != 1 {
		t.Errorf("expected 1 inbox item from the stream, got %d", len(m.inbox))
	}

	// Subscribe error path.
	d2 := &fakeDaemon{subscribeErr: context.DeadlineExceeded}
	m2 := New(d2, "/work")
	if _, isErr := runCmd(m2.subscribeCmd()).(eventErrMsg); !isErr {
		t.Error("expected eventErrMsg on subscribe failure")
	}
	// eventErrMsg is handled without crashing.
	m2, _ = update(m2, eventErrMsg{err: context.DeadlineExceeded})
	if m2.subErr == nil {
		t.Error("subErr should be recorded")
	}
}

// SWI-17 (FR-223, research R10/R14): a step not accepted reads like a failed
// run; a detached agent is its own warning. Both reach the inbox and desktop.
func TestStepAndDetachedNotifications(t *testing.T) {
	rn := &recordNotifier{}
	m := sized(New(&fakeDaemon{}, "/work").WithNotifier(rn))
	m.sub = &fakeStream{ctx: context.Background(), ch: nil}
	m, _ = update(m, eventMsg{ev: notif("n-1", "sb1", pb.NotificationKind_NOTIFICATION_KIND_RUN_STEP_NOT_ACCEPTED)})
	m, _ = update(m, eventMsg{ev: notif("n-2", "sb2", pb.NotificationKind_NOTIFICATION_KIND_AGENT_DETACHED)})
	m, _ = update(m, eventMsg{ev: notif("n-3", "sb3", pb.NotificationKind_NOTIFICATION_KIND_AGENT_CHECK_FAILED)})
	if len(m.inbox) != 3 || len(rn.titles) != 3 {
		t.Fatalf("inbox %d desktop %d", len(m.inbox), len(rn.titles))
	}
	titles := strings.Join(rn.titles, "|")
	if !strings.Contains(titles, "step not accepted") || !strings.Contains(titles, "agent detached") || !strings.Contains(titles, "can't check agent") {
		t.Errorf("titles = %v", rn.titles)
	}
	if notifIcon(pb.NotificationKind_NOTIFICATION_KIND_RUN_STEP_NOT_ACCEPTED) != notifIcon(pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED) {
		t.Error("not accepted is shown like a failed run")
	}
	// SWI-25 A3: a failing check is an error, and enter goes to the sandbox.
	if notifIcon(pb.NotificationKind_NOTIFICATION_KIND_AGENT_CHECK_FAILED) != notifIcon(pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED) {
		t.Error("a failing check is shown like a failed run")
	}
	out, _ := m.enterNotifications()
	m = out.(Model)
	if v := m.View(); !strings.Contains(v, "step not accepted") || !strings.Contains(v, "agent detached") || !strings.Contains(v, "can't check agent") {
		t.Errorf("inbox:\n%s", v)
	}
}

// A start-up "agents detached" notice (SWI-25 A2) has no sandbox row: enter
// opens the one orchestrator's Workers view, a picker when the workers span
// several, or the list on the first sandbox when none is a worker.
func TestDetachedAtStartNoticeRoutesToWorkers(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot"), orchSummary("o-b", "beta-bot")}}
	d.orchDetail = &pb.OrchestratorDetail{Summary: d.orchestrators[0]}
	rn := &recordNotifier{}
	m := withSandboxes(sized(New(d, "/work").WithNotifier(rn)), []*pb.Sandbox{{Id: "sa"}, {Id: "sb"}, {Id: "plain"}})
	m.sub = &fakeStream{ctx: context.Background(), ch: nil}
	host := d.HostID()
	one := &pb.NotificationEvent{Id: "n-1", Kind: pb.NotificationKind_NOTIFICATION_KIND_AGENTS_DETACHED_AT_START, Message: "2 agents detached by a daemon restart — enter to review",
		Affected: []*pb.NotificationEvent_AffectedSandbox{
			{SandboxId: "sa", OrchestratorId: "o-a", OrchestratorName: "alpha-bot", OrchestratorHostId: host},
			{SandboxId: "sb", OrchestratorId: "o-a", OrchestratorName: "alpha-bot", OrchestratorHostId: host},
		}}
	m, _ = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_Notification{Notification: one}}})
	if len(rn.titles) != 1 || !strings.Contains(rn.titles[0], "agents detached") {
		t.Fatalf("desktop titles = %v", rn.titles)
	}
	out, _ := m.enterNotifications()
	m = out.(Model)
	if v := m.View(); !strings.Contains(v, "agents detached") || !strings.Contains(v, "2 sandboxes") {
		t.Errorf("inbox:\n%s", v)
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = feed(m, cmd)
	if m.screen != screenOrchDetail || m.orchDetail.id != "o-a" || m.orchDetail.view != viewWorkers {
		t.Fatalf("one orchestrator: screen %v detail %q view %v status %q", m.screen, m.orchDetail.id, m.orchDetail.view, m.status)
	}

	// Two orchestrators: the picker, then the chosen one's Workers view.
	two := &pb.NotificationEvent{Id: "n-2", Kind: pb.NotificationKind_NOTIFICATION_KIND_AGENTS_DETACHED_AT_START, Message: "3 agents detached by a daemon restart — enter to review",
		Affected: []*pb.NotificationEvent_AffectedSandbox{
			{SandboxId: "sa", OrchestratorId: "o-a", OrchestratorName: "alpha-bot", OrchestratorHostId: host},
			{SandboxId: "sb", OrchestratorId: "o-b", OrchestratorName: "beta-bot", OrchestratorHostId: host},
			{SandboxId: "plain"},
		}}
	m, _ = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_Notification{Notification: two}}})
	out, _ = m.enterNotifications()
	m = out.(Model)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // newest first: n-2 is selected
	if m.screen != screenDetachedPick || len(m.detachedPick.choices) != 2 {
		t.Fatalf("two orchestrators: screen %v choices %d", m.screen, len(m.detachedPick.choices))
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "alpha-bot") || !strings.Contains(v, "beta-bot") || !strings.Contains(v, "1 worker") || !strings.Contains(v, "Review which?") {
		t.Errorf("picker:\n%s", v)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenNotifications {
		t.Fatalf("esc returns to the inbox: %v", m.screen)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, press("j"))
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = feed(m, cmd)
	if m.screen != screenOrchDetail || m.orchDetail.id != "o-b" || m.orchDetail.view != viewWorkers {
		t.Fatalf("picked: screen %v detail %q view %v", m.screen, m.orchDetail.id, m.orchDetail.view)
	}

	// No worker among them: the list, on the first sandbox.
	none := &pb.NotificationEvent{Id: "n-3", Kind: pb.NotificationKind_NOTIFICATION_KIND_AGENTS_DETACHED_AT_START, Message: "2 agents detached",
		Affected: []*pb.NotificationEvent_AffectedSandbox{{SandboxId: "plain"}, {SandboxId: "sb"}}}
	m, _ = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_Notification{Notification: none}}})
	out, _ = m.enterNotifications()
	m = out.(Model)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList || m.actionable().GetId() != "plain" {
		t.Errorf("no worker: screen %v selected %q", m.screen, m.actionable().GetId())
	}

	// An orchestrator on a daemon the TUI isn't connected to.
	elsewhere := &pb.NotificationEvent{Id: "n-4", Kind: pb.NotificationKind_NOTIFICATION_KIND_AGENTS_DETACHED_AT_START, Message: "2 agents detached",
		Affected: []*pb.NotificationEvent_AffectedSandbox{{SandboxId: "sa", OrchestratorId: "o-z", OrchestratorName: "zeta", OrchestratorHostId: "nowhere"}, {SandboxId: "sb", OrchestratorId: "o-z", OrchestratorHostId: "nowhere"}}}
	m, _ = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_Notification{Notification: elsewhere}}})
	out, _ = m.enterNotifications()
	m = out.(Model)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenNotifications || !strings.Contains(m.status, "zeta's daemon is not connected") {
		t.Errorf("unconnected: screen %v status %q", m.screen, m.status)
	}
	if got := detachedOrchestrators(two); len(got) != 2 || got[0].name != "alpha-bot" || got[1].count != 1 {
		t.Errorf("detachedOrchestrators = %+v", got)
	}
}
