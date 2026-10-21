package ui

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// eventHost is an in-process daemon whose event stream a test can feed and
// whose acks it can observe (feature 009, research R13: per-host streams).
type eventHost struct {
	pb.UnimplementedSwitchboardServer
	id     string
	events chan *pb.Event
	mu     sync.Mutex
	acked  []string
	subs   int
}

func (s *eventHost) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: s.id}, nil
}

func (s *eventHost) ListSandboxes(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	return &pb.ListSandboxesResponse{Sandboxes: []*pb.Sandbox{{Id: s.id + "-sb", DisplayName: s.id + "-sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}}, nil
}

func (s *eventHost) Subscribe(_ *pb.SubscribeRequest, stream pb.Switchboard_SubscribeServer) error {
	s.mu.Lock()
	s.subs++
	s.mu.Unlock()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-s.events:
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

func (s *eventHost) AckNotification(_ context.Context, req *pb.AckNotificationRequest) (*pb.AckNotificationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, req.GetNotificationIds()...)
	return &pb.AckNotificationResponse{Acked: uint32(len(req.GetNotificationIds()))}, nil
}

func (s *eventHost) subscriptions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subs
}

func (s *eventHost) ackedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.acked...)
}

func startEventHost(t *testing.T, id string) (*eventHost, string) {
	t.Helper()
	srv := &eventHost{id: id, events: make(chan *pb.Event, 8)}
	sock := filepath.Join(t.TempDir(), id+".sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return srv, sock
}

// twoEventHosts wires a model to two live hosts (alpha = hosta, beta = hostb)
// and subscribes both through the list-data path.
func twoEventHosts(t *testing.T) (Model, *eventHost, *eventHost, *client.Manager) {
	t.Helper()
	m, a, b, mgr := twoEventHostsRaw(t)
	// listDataMsg → subscribeHostsCmd → subOpenedMsg ×2 (the receivers they arm
	// are run explicitly by recvFrom once an event is queued).
	m = feed2(m, m.listDataCmd())
	return m, a, b, mgr
}

// twoEventHostsRaw is twoEventHosts before the subscriptions are opened.
func twoEventHostsRaw(t *testing.T) (Model, *eventHost, *eventHost, *client.Manager) {
	t.Helper()
	srvA, sockA := startEventHost(t, "hosta")
	srvB, sockB := startEventHost(t, "hostb")
	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	for id, name := range map[string]string{"hosta": "alpha", "hostb": "beta"} {
		if _, err := hs.Save(store.KnownHost{ID: id, DisplayName: name, Kind: "ssh", SSHTarget: id}); err != nil {
			t.Fatal(err)
		}
	}
	socks := map[string]string{"hosta": sockA, "hostb": sockB}
	mgr := client.NewManager()
	mgr.SetDialFunc(func(ctx context.Context, e client.HostEntry) (*client.Conn, error) {
		return client.DialLocal(ctx, socks[e.ID])
	})
	for _, id := range []string{"hosta", "hostb"} {
		known, _ := hs.Get(id)
		mgr.Upsert(toEntry(*known))
		if err := mgr.Connect(context.Background(), id); err != nil {
			t.Fatalf("connect %s: %v", id, err)
		}
	}
	m := sized(New(&fakeDaemon{hostID: "hosta"}, "/work").WithHosts(mgr, hs, "hosta").WithNotifier(&recordNotifier{}))
	return m, srvA, srvB, mgr
}

// waitSubs waits until both hosts have a subscription open.
func waitSubs(t *testing.T, hosts ...*eventHost) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, h := range hosts {
			if h.subscriptions() == 0 {
				ok = false
			}
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subscriptions never opened")
}

// recvFrom runs the model's receiver for a host's stream once an event is
// queued, returning the produced message.
func recvFrom(t *testing.T, m Model, host string) eventMsg {
	t.Helper()
	msg := runCmd(m.recvCmd(m.subs[host]))
	ev, ok := msg.(eventMsg)
	if !ok {
		t.Fatalf("recv = %T %v", msg, msg)
	}
	return ev
}

func TestPerHostStreamsUpdateRemoteRowsLive(t *testing.T) {
	m, srvA, srvB, _ := twoEventHosts(t)
	if len(m.subs) != 2 || !m.subscribed["hosta"] || !m.subscribed["hostb"] {
		t.Fatalf("subs = %d subscribed = %v", len(m.subs), m.subscribed)
	}
	waitSubs(t, srvA, srvB)

	// A remote (beta) sandbox_changed arrives tagged with its host and updates
	// the remote row in place, with no reload.
	srvB.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: &pb.Sandbox{
		Id: "hostb-sb", DisplayName: "hostb-sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT},
	}}}
	ev := recvFrom(t, m, "hostb")
	if ev.hostID != "hostb" {
		t.Fatalf("event host = %q, want hostb", ev.hostID)
	}
	m, _ = update(m, ev)
	for _, hs := range m.hostAgg {
		if hs.Host.Entry.ID == "hostb" && hs.Sandboxes[0].GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT {
			t.Error("remote row should carry the live agent status")
		}
	}
	if !strings.Contains(m.View(), "needs input") {
		t.Error("the remote row should render the live status")
	}

	// A sandbox this client never listed (launched elsewhere) is inserted under
	// its host once past CREATING.
	srvA.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: &pb.Sandbox{Id: "new-on-a", DisplayName: "new-on-a", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}}
	m, _ = update(m, recvFrom(t, m, "hosta"))
	if !m.knowsSandbox("new-on-a") {
		t.Error("a RUNNING sandbox first seen on the stream should be inserted")
	}
	srvA.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: &pb.Sandbox{Id: "creating-on-a", State: pb.SandboxState_SANDBOX_STATE_CREATING}}}
	m, _ = update(m, recvFrom(t, m, "hosta"))
	if m.knowsSandbox("creating-on-a") {
		t.Error("a CREATING sandbox is not inserted (the launch flow owns its placeholder)")
	}
}

func TestNotificationsAckOnOriginatingHost(t *testing.T) {
	m, srvA, srvB, _ := twoEventHosts(t)
	waitSubs(t, srvA, srvB)
	srvB.events <- notif("nb-1", "hostb-sb", pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING)
	m, _ = update(m, recvFrom(t, m, "hostb"))
	srvA.events <- notif("na-1", "hosta-sb", pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE)
	m, _ = update(m, recvFrom(t, m, "hosta"))
	if len(m.inbox) != 2 || m.inboxHosts["nb-1"] != "hostb" || m.inboxHosts["na-1"] != "hosta" {
		t.Fatalf("inbox = %d hosts = %v", len(m.inbox), m.inboxHosts)
	}
	// Opening the inbox acks each notification on the daemon it came from.
	m, cmd := update(m, press("i"))
	msg := runCmd(cmd)
	if st, ok := msg.(statusMsg); !ok || !strings.Contains(string(st), "2 notifications") {
		t.Fatalf("ack status = %v", msg)
	}
	if a := srvA.ackedIDs(); len(a) != 1 || a[0] != "na-1" {
		t.Errorf("alpha acked %v, want [na-1]", a)
	}
	if b := srvB.ackedIDs(); len(b) != 1 || b[0] != "nb-1" {
		t.Errorf("beta acked %v, want [nb-1]", b)
	}
	// enter lands on the notification's sandbox row across hosts.
	m, _ = update(m, press("enter"))
	if m.screen != screenList {
		t.Error("enter should return to the list")
	}
}

func TestReconnectResubscribes(t *testing.T) {
	m, srvA, srvB, mgr := twoEventHosts(t)
	waitSubs(t, srvA, srvB)
	// The beta stream drops: the host is forgotten so the next snapshot
	// re-subscribes it; the alpha stream is untouched.
	m, _ = update(m, eventErrMsg{hostID: "hostb", err: context.Canceled})
	if m.subscribed["hostb"] || m.subs["hostb"] != nil || !m.subscribed["hosta"] {
		t.Fatalf("after drop: subscribed = %v", m.subscribed)
	}
	// Disconnect + reconnect beta through the manager, then a hostsMsg (what the
	// daemon screen's connect produces) re-subscribes exactly once.
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	if m.subscribed["hostb"] {
		t.Error("a disconnected host must not be subscribed")
	}
	if err := mgr.Connect(context.Background(), "hostb"); err != nil {
		t.Fatal(err)
	}
	before := srvB.subscriptions()
	m = feed2(m, m.hostsCmd())
	if !m.subscribed["hostb"] || m.subs["hostb"] == nil {
		t.Fatalf("reconnect should re-subscribe: %v", m.subscribed)
	}
	deadline := time.Now().Add(3 * time.Second)
	for srvB.subscriptions() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srvB.subscriptions() != before+1 {
		t.Errorf("beta subscriptions = %d, want %d", srvB.subscriptions(), before+1)
	}
	// A second snapshot does not subscribe again.
	m = feed2(m, m.hostsCmd())
	time.Sleep(50 * time.Millisecond)
	if srvB.subscriptions() != before+1 {
		t.Errorf("a host already subscribed must not be subscribed twice: %d", srvB.subscriptions())
	}
	// The active stream drop clears m.sub too.
	m.subs["hosta"] = m.sub
	m, _ = update(m, eventErrMsg{hostID: "hosta", err: context.Canceled})
	if m.sub != nil {
		t.Error("dropping the active host's stream should clear m.sub")
	}
}
