package agent

import (
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// waitSession is a memSession whose child "exits" when exit() is called.
type waitSession struct {
	*memSession
	exited chan struct{}
	once   sync.Once
}

func newWaitSession() *waitSession {
	return &waitSession{memSession: newMemSession(), exited: make(chan struct{})}
}

func (w *waitSession) Wait() error { <-w.exited; return nil }
func (w *waitSession) exit()       { w.once.Do(func() { close(w.exited) }) }

// recorder captures SetAgentStatus calls.
type recorder struct {
	mu    sync.Mutex
	calls []pb.AgentStatus
	ids   []string
}

func (r *recorder) SetAgentStatus(id string, st pb.AgentStatus, _ time.Time) (*pb.Sandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, st)
	r.ids = append(r.ids, id)
	return &pb.Sandbox{Id: id}, nil
}

func (r *recorder) snapshot() []pb.AgentStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pb.AgentStatus(nil), r.calls...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestSpontaneousExitReportsExitedOnceAndRecreates(t *testing.T) {
	defer func(d time.Duration) { promptSubmitDelay = d }(promptSubmitDelay)
	promptSubmitDelay = 0

	var mu sync.Mutex
	var created []*waitSession
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) {
		mu.Lock()
		defer mu.Unlock()
		s := newWaitSession()
		created = append(created, s)
		return s, nil
	})
	rec := &recorder{}
	reg.SetStatusUpdater(rec)

	if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(rec.snapshot()) == 1 }) // STARTING is written asynchronously
	mu.Lock()
	first := created[0]
	mu.Unlock()
	first.exit()

	// Creating the session recorded STARTING (SWI-15); the exit adds EXITED.
	waitFor(t, func() bool { return len(rec.snapshot()) == 2 })
	if got := rec.snapshot(); got[0] != pb.AgentStatus_AGENT_STATUS_STARTING || got[1] != pb.AgentStatus_AGENT_STATUS_EXITED || rec.ids[1] != "sb1" {
		t.Fatalf("status calls = %v for %v", got, rec.ids)
	}
	// The dead session is forgotten: a later Prompt starts a fresh agent.
	if err := reg.Prompt("sb1", &pb.AgentSpec{}, "again"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(created)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("sessions created = %d, want 2 (recreated after exit)", n)
	}
	// Exiting the first session again (idempotent) reports nothing more.
	first.exit()
	waitFor(t, func() bool { return len(rec.snapshot()) >= 3 })
	time.Sleep(20 * time.Millisecond)
	if got := rec.snapshot(); len(got) != 3 || got[2] != pb.AgentStatus_AGENT_STATUS_STARTING {
		t.Errorf("EXITED must be reported once (then the new session's STARTING), got %v", got)
	}
}

func TestDeliberateCloseIsNotAnExit(t *testing.T) {
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newWaitSession(), nil })
	rec := &recorder{}
	reg.SetStatusUpdater(rec)
	s, _ := reg.Session("sb1", &pb.AgentSpec{})
	reg.Close("sb1")
	s.(*waitSession).exit() // the killed child's Wait returns after Close
	time.Sleep(30 * time.Millisecond)
	// (STARTING, written asynchronously, is skipped once the session is gone.)
	for _, st := range rec.snapshot() {
		if st != pb.AgentStatus_AGENT_STATUS_STARTING {
			t.Errorf("a registry-initiated close must not report EXITED, got %v", rec.snapshot())
		}
	}
	// Without a status updater an exit is simply forgotten.
	reg2 := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newWaitSession(), nil })
	s2, _ := reg2.Session("sb1", &pb.AgentSpec{})
	s2.(*waitSession).exit()
	waitFor(t, func() bool {
		reg2.mu.Lock()
		defer reg2.mu.Unlock()
		_, ok := reg2.sessions["sb1"]
		return !ok
	})
}

// A session that is not a Waiter (older fakes) is never watched.
func TestNonWaiterSessionsAreNotWatched(t *testing.T) {
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	rec := &recorder{}
	reg.SetStatusUpdater(rec)
	if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if got := rec.snapshot(); len(got) != 1 || got[0] != pb.AgentStatus_AGENT_STATUS_STARTING {
		t.Errorf("no exit should be reported for a plain session, got %v", got)
	}
}

// Publish must take the hub mutex: concurrent Subscribe/Unsubscribe mutate the
// subscriber map it iterates (run under -race).
func TestHubPublishUnderConcurrentSubscribe(t *testing.T) {
	h := NewHub("host-1")
	h.Publish(nil) // nil-safe
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			id, ch, _ := h.Subscribe(false)
			go func() {
				for range ch {
				}
			}()
			h.Unsubscribe(id)
		}
	}()
	for i := 0; i < 200; i++ {
		h.Publish(&pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: "host-1", OrchestratorId: "o"}}})
	}
	<-done
	// A live subscriber still receives a published event.
	_, ch, _ := h.Subscribe(false)
	h.Publish(&pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{OrchestratorId: "last"}}})
	if ev := recv(t, ch); ev.GetOrchestratorChanged().GetOrchestratorId() != "last" {
		t.Errorf("published event not received: %v", ev)
	}
}
