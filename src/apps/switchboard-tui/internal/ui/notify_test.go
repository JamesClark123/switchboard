package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	if len(m.inbox) != 2 || len(rn.titles) != 2 {
		t.Fatalf("inbox %d desktop %d", len(m.inbox), len(rn.titles))
	}
	if !strings.Contains(strings.Join(rn.titles, "|"), "step not accepted") || !strings.Contains(strings.Join(rn.titles, "|"), "agent detached") {
		t.Errorf("titles = %v", rn.titles)
	}
	if notifIcon(pb.NotificationKind_NOTIFICATION_KIND_RUN_STEP_NOT_ACCEPTED) != notifIcon(pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED) {
		t.Error("not accepted is shown like a failed run")
	}
	out, _ := m.enterNotifications()
	m = out.(Model)
	if v := m.View(); !strings.Contains(v, "step not accepted") || !strings.Contains(v, "agent detached") {
		t.Errorf("inbox:\n%s", v)
	}
}
