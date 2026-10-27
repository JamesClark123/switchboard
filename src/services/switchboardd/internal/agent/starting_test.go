package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// notes records notifications.
type notes struct {
	mu   sync.Mutex
	msgs []string
}

func (n *notes) EmitNotification(id string, kind pb.NotificationKind, msg string, _ time.Time) *pb.NotificationEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, id+" "+kind.String()+" "+msg)
	return nil
}

func (n *notes) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.msgs...)
}

func shortStart(t *testing.T, d time.Duration) {
	t.Helper()
	old := agentStartTimeout
	agentStartTimeout = d
	t.Cleanup(func() { agentStartTimeout = old })
}

// A created session's agent is STARTING and not ready until it reports in;
// reusing the session records nothing more.
func TestSessionStartsAgentStarting(t *testing.T) {
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	rec := &recorder{}
	reg.SetStatusUpdater(rec)
	if reg.Live("sb1") || reg.Ready("sb1") {
		t.Fatal("no session yet")
	}
	if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	if !reg.Live("sb1") || reg.Ready("sb1") {
		t.Errorf("after Session: live %v ready %v; want live, not ready", reg.Live("sb1"), reg.Ready("sb1"))
	}
	if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(rec.snapshot()) >= 1 }) // written asynchronously
	time.Sleep(10 * time.Millisecond)
	if got := rec.snapshot(); len(got) != 1 || got[0] != pb.AgentStatus_AGENT_STATUS_STARTING {
		t.Errorf("status calls = %v, want one STARTING", got)
	}
	reg.MarkReady("sb1")
	reg.MarkReady("sb1") // idempotent
	reg.MarkReady("sb-none")
	if !reg.Ready("sb1") || reg.Ready("sb-none") {
		t.Error("MarkReady should mark only the live session ready")
	}
	// A new session after Close starts over: STARTING, not ready.
	reg.Close("sb1")
	if reg.Live("sb1") || reg.Ready("sb1") {
		t.Error("a closed session is neither live nor ready")
	}
	if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(rec.snapshot()) >= 2 })
	if reg.Ready("sb1") || len(rec.snapshot()) != 2 {
		t.Errorf("recreated session: ready %v, calls %v", reg.Ready("sb1"), rec.snapshot())
	}
}

// An agent that never reports ready is parked NEEDS_INPUT with a notification;
// one that reports in time, or whose session is gone, is left alone.
func TestAgentStartTimeout(t *testing.T) {
	shortStart(t, 30*time.Millisecond)
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	rec, n := &recorder{}, &notes{}
	reg.SetStatusUpdater(rec)
	reg.SetNotifier(n)
	var logged []string
	var logMu sync.Mutex
	reg.SetLogger(func(f string, _ ...any) { logMu.Lock(); logged = append(logged, f); logMu.Unlock() })

	if _, err := reg.Session("stuck", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Session("ok", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	reg.MarkReady("ok")
	if _, err := reg.Session("gone", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	reg.Close("gone")

	waitFor(t, func() bool { return len(n.snapshot()) == 1 })
	time.Sleep(60 * time.Millisecond) // the others' timers would have fired by now
	if got := n.snapshot(); len(got) != 1 || !strings.HasPrefix(got[0], "stuck NOTIFICATION_KIND_NEEDS_PROMPTING agent did not finish starting") {
		t.Errorf("notifications = %v", got)
	}
	rec.mu.Lock()
	var needs []string
	for i, st := range rec.calls {
		if st == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT {
			needs = append(needs, rec.ids[i])
		}
	}
	rec.mu.Unlock()
	if len(needs) != 1 || needs[0] != "stuck" {
		t.Errorf("NEEDS_INPUT set for %v, want only stuck", needs)
	}
	logMu.Lock()
	if len(logged) != 1 {
		t.Errorf("logged %v", logged)
	}
	logMu.Unlock()
	// Without a status side or notifier the timeout is harmless.
	bare := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	if _, err := bare.Session("x", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
}

// SessionStart marks the agent IDLE silently and tells the registry the agent
// is ready — except after a compaction, which can happen mid-turn.
func TestSessionStartHook(t *testing.T) {
	for _, c := range []struct {
		name, source string
		before       pb.AgentStatus
		want         pb.AgentStatus // UNSPECIFIED = no status write
	}{
		{"startup", "startup", pb.AgentStatus_AGENT_STATUS_STARTING, pb.AgentStatus_AGENT_STATUS_IDLE},
		{"resume", "resume", pb.AgentStatus_AGENT_STATUS_STARTING, pb.AgentStatus_AGENT_STATUS_IDLE},
		{"after the start timeout", "startup", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.AgentStatus_AGENT_STATUS_IDLE},
		{"clear at the prompt", "clear", pb.AgentStatus_AGENT_STATUS_IDLE, pb.AgentStatus_AGENT_STATUS_IDLE},
		{"compact mid-turn", "compact", pb.AgentStatus_AGENT_STATUS_WORKING, pb.AgentStatus_AGENT_STATUS_UNSPECIFIED},
		{"unknown source while starting", "", pb.AgentStatus_AGENT_STATUS_STARTING, pb.AgentStatus_AGENT_STATUS_IDLE},
		{"unknown source while parked", "later", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.AgentStatus_AGENT_STATUS_IDLE},
		{"unknown source while working", "", pb.AgentStatus_AGENT_STATUS_WORKING, pb.AgentStatus_AGENT_STATUS_UNSPECIFIED},
	} {
		t.Run(c.name, func(t *testing.T) {
			hub := NewHub("h")
			st := &agentState{status: c.before}
			hs := NewHookServer(hub, st)
			var ready []string
			hs.SetOnReady(func(id string) { ready = append(ready, id) })
			hs.dispatch(hookPayload{Event: "SessionStart", SandboxID: "sb1", Source: c.source})
			got := st.status
			if c.want == pb.AgentStatus_AGENT_STATUS_UNSPECIFIED {
				if got != c.before {
					t.Errorf("status changed to %v", got)
				}
			} else if got != c.want {
				t.Errorf("status = %v, want %v", got, c.want)
			}
			if len(hub.Undelivered()) != 0 {
				t.Error("SessionStart must not notify")
			}
			if wantReady := c.source != "compact"; (len(ready) == 1 && ready[0] == "sb1") != wantReady || len(ready) > 1 {
				t.Errorf("ready observer calls = %v (want ready: %v)", ready, wantReady)
			}
		})
	}
	// Without a readable status side an unknown source changes nothing.
	f := &fakeStatus{}
	NewHookServer(NewHub("h"), f).dispatch(hookPayload{Event: "SessionStart", SandboxID: "sb1"})
	if f.calls != 0 {
		t.Errorf("unreadable status: %d writes", f.calls)
	}
	// Other hooks do not: after a daemon restart they may come from an agent
	// that outlived the old client while the new one is still booting.
	hs := NewHookServer(NewHub("h"), &fakeStatus{})
	var ready []string
	hs.SetOnReady(func(id string) { ready = append(ready, id) })
	for _, ev := range []string{"UserPromptSubmit", "Stop", "PreToolUse", "Notification"} {
		hs.dispatch(hookPayload{Event: ev, SandboxID: "sb2"})
	}
	if len(ready) != 0 {
		t.Errorf("non-SessionStart ready calls = %v", ready)
	}
}

// The SessionStart hook's forwarded input carries the source (captured from
// Claude Code 2.1.311).
func TestHookHTTPSessionStartSource(t *testing.T) {
	st := &agentState{status: pb.AgentStatus_AGENT_STATUS_WORKING}
	hs := NewHookServer(NewHub("h"), st)
	srv := httptest.NewServer(http.HandlerFunc(hs.Handle))
	defer srv.Close()
	post := func(source string) {
		t.Helper()
		body := `{"session_id":"s","transcript_path":"/t.jsonl","cwd":"/w","hook_event_name":"SessionStart","source":"` + source + `","model":"claude-opus-5-5"}`
		resp, err := http.Post(srv.URL+"?event=SessionStart&sandbox_id=sb1", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	post("compact")
	if st.status != pb.AgentStatus_AGENT_STATUS_WORKING {
		t.Errorf("compact moved a working agent to %v", st.status)
	}
	post("startup")
	if st.status != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Errorf("startup: status %v, want IDLE", st.status)
	}
}

// lockingStatus takes a lock the Session caller holds, as the gRPC server's
// emit takes the terminal registry's lock while that registry is creating a
// broadcaster's session.
type lockingStatus struct {
	held *sync.Mutex
	rec  recorder
}

func (l *lockingStatus) SetAgentStatus(id string, st pb.AgentStatus, now time.Time) (*pb.Sandbox, error) {
	l.held.Lock()
	defer l.held.Unlock()
	return l.rec.SetAgentStatus(id, st, now)
}

// Session never writes the status on its caller's goroutine, so a caller that
// holds a lock the write's observers take does not deadlock.
func TestSessionStatusWriteDoesNotRunUnderTheCaller(t *testing.T) {
	var held sync.Mutex
	st := &lockingStatus{held: &held}
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	reg.SetStatusUpdater(st)
	done := make(chan struct{})
	go func() {
		held.Lock()
		defer held.Unlock()
		_, _ = reg.Session("sb1", &pb.AgentSpec{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Session deadlocked writing the status under its caller's lock")
	}
	waitFor(t, func() bool { return len(st.rec.snapshot()) == 1 })
}

// An agent that reports ready before the asynchronous STARTING write runs is
// never set back to STARTING (the ready hook's IDLE stays last).
func TestReadyBeforeStartingWriteSkipsIt(t *testing.T) {
	var held sync.Mutex
	st := &lockingStatus{held: &held}
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	reg.SetStatusUpdater(st)
	held.Lock() // hold the STARTING write back
	if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	marked := make(chan struct{})
	go func() { reg.MarkReady("sb1"); close(marked) }()
	time.Sleep(20 * time.Millisecond)
	held.Unlock()
	<-marked
	time.Sleep(20 * time.Millisecond)
	// Either STARTING was already in flight (and MarkReady waited behind it) or
	// it was skipped; never a STARTING after MarkReady returned.
	n := len(st.rec.snapshot())
	if _, err := st.SetAgentStatus("sb1", pb.AgentStatus_AGENT_STATUS_IDLE, time.Now()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := st.rec.snapshot(); len(got) != n+1 || got[len(got)-1] != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Errorf("status calls = %v, want IDLE last", got)
	}
}

// An agent kind that fires no hooks is ready at once: no STARTING, no timeout.
func TestNonClaudeAgentIsReadyAtOnce(t *testing.T) {
	shortStart(t, 20*time.Millisecond)
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	rec, n := &recorder{}, &notes{}
	reg.SetStatusUpdater(rec)
	reg.SetNotifier(n)
	if _, err := reg.Session("sb1", &pb.AgentSpec{Kind: "aider"}); err != nil {
		t.Fatal(err)
	}
	if !reg.Ready("sb1") {
		t.Error("a non-Claude agent should be ready at once")
	}
	time.Sleep(60 * time.Millisecond)
	if len(rec.snapshot()) != 0 || len(n.snapshot()) != 0 {
		t.Errorf("status %v notifications %v, want none", rec.snapshot(), n.snapshot())
	}
	reg.Close("sb1")
	time.Sleep(20 * time.Millisecond)
	if len(rec.snapshot()) != 0 {
		t.Errorf("closing a ready agent wrote %v", rec.snapshot())
	}
}

// Closing an agent that never reported ready clears the STARTING (or the start
// timeout's NEEDS_INPUT) it left behind, so the worker reads IDLE and the next
// delivery starts a fresh agent; a status set by other means is kept.
func TestCloseResetsAnUnreadyAgent(t *testing.T) {
	for _, c := range []struct {
		name       string
		set, want  pb.AgentStatus
		reopenLive bool
	}{
		{"starting", pb.AgentStatus_AGENT_STATUS_STARTING, pb.AgentStatus_AGENT_STATUS_IDLE, false},
		{"parked by the timeout", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.AgentStatus_AGENT_STATUS_IDLE, false},
		{"working", pb.AgentStatus_AGENT_STATUS_WORKING, pb.AgentStatus_AGENT_STATUS_WORKING, false},
		{"a new session already exists", pb.AgentStatus_AGENT_STATUS_STARTING, pb.AgentStatus_AGENT_STATUS_STARTING, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := &agentState{}
			reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
			reg.SetStatusUpdater(st)
			if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool {
				sb, _ := st.Get("sb1")
				return sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_STARTING
			})
			_, _ = st.SetAgentStatus("sb1", c.set, time.Now())
			reg.statusMu.Lock() // hold the reset back until the reopen (if any)
			reg.Close("sb1")
			if c.reopenLive {
				if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
					t.Fatal(err)
				}
			}
			reg.statusMu.Unlock()
			time.Sleep(30 * time.Millisecond)
			if sb, _ := st.Get("sb1"); sb.GetAgent().GetStatus() != c.want {
				t.Errorf("status = %v, want %v", sb.GetAgent().GetStatus(), c.want)
			}
		})
	}
	// A ready agent's close leaves its status alone; so does an unreadable status side.
	st := &agentState{}
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	reg.SetStatusUpdater(st)
	if _, err := reg.Session("sb1", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	reg.MarkReady("sb1")
	_, _ = st.SetAgentStatus("sb1", pb.AgentStatus_AGENT_STATUS_IDLE, time.Now())
	reg.Close("sb1")
	time.Sleep(20 * time.Millisecond)
	if sb, _ := st.Get("sb1"); sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Errorf("ready close: status %v", sb.GetAgent().GetStatus())
	}
	rec := &recorder{}
	reg2 := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	reg2.SetStatusUpdater(rec)
	if _, err := reg2.Session("sb1", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(rec.snapshot()) == 1 })
	reg2.Close("sb1")
	time.Sleep(20 * time.Millisecond)
	if got := rec.snapshot(); len(got) != 1 {
		t.Errorf("unreadable status side: %v", got)
	}
}

// ReassertUnready puts a live, unready agent's status back over a stray IDLE:
// STARTING before its start timeout, NEEDS_INPUT after; a ready agent or no
// session is left alone.
func TestReassertUnready(t *testing.T) {
	shortStart(t, 150*time.Millisecond)
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	rec := &recorder{}
	reg.SetStatusUpdater(rec)
	last := func() pb.AgentStatus {
		got := rec.snapshot()
		return got[len(got)-1]
	}
	if _, err := reg.Session("sb", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(rec.snapshot()) == 1 }) // STARTING
	reg.ReassertUnready("sb", pb.AgentStatus_AGENT_STATUS_IDLE)
	if n := len(rec.snapshot()); n != 2 || last() != pb.AgentStatus_AGENT_STATUS_STARTING {
		t.Errorf("before the timeout: %v, want STARTING again", rec.snapshot())
	}
	waitFor(t, func() bool { return len(rec.snapshot()) == 3 }) // the timeout's NEEDS_INPUT
	reg.ReassertUnready("sb", pb.AgentStatus_AGENT_STATUS_IDLE)
	if n := len(rec.snapshot()); n != 4 || last() != pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT {
		t.Errorf("after the timeout: %v, want NEEDS_INPUT again", rec.snapshot())
	}
	reg.MarkReady("sb")
	reg.ReassertUnready("sb", pb.AgentStatus_AGENT_STATUS_IDLE)
	reg.ReassertUnready("sb-none", pb.AgentStatus_AGENT_STATUS_IDLE)
	if n := len(rec.snapshot()); n != 4 {
		t.Errorf("a ready agent or no session got a write: %v", rec.snapshot())
	}
}

// statusStore keeps one status per sandbox, readable back like the registry.
type statusStore struct {
	mu sync.Mutex
	st map[string]pb.AgentStatus
}

func (s *statusStore) SetAgentStatus(id string, st pb.AgentStatus, _ time.Time) (*pb.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st[id] = st
	return &pb.Sandbox{Id: id}, nil
}

func (s *statusStore) Get(id string) (*pb.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &pb.Sandbox{Id: id, Agent: &pb.AgentSession{Status: s.st[id]}}, nil
}

func (s *statusStore) get(id string) pb.AgentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st[id]
}

// SWI-15 review: ReassertUnready rewrites only the stray status the refused
// delivery saw. A hook that landed since (the WORKING of the developer's next
// prompt) is left as it is; a rewrite is logged.
func TestReassertUnreadyOnlyOverTheStatusSeen(t *testing.T) {
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	store := &statusStore{st: map[string]pb.AgentStatus{}}
	reg.SetStatusUpdater(store)
	var logs []string
	var logMu sync.Mutex
	reg.SetLogger(func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	if _, err := reg.Session("sb", &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return store.get("sb") == pb.AgentStatus_AGENT_STATUS_STARTING })

	_, _ = store.SetAgentStatus("sb", pb.AgentStatus_AGENT_STATUS_WORKING, time.Now()) // the next prompt's hook
	reg.ReassertUnready("sb", pb.AgentStatus_AGENT_STATUS_IDLE)
	if got := store.get("sb"); got != pb.AgentStatus_AGENT_STATUS_WORKING {
		t.Errorf("a WORKING that landed since was overwritten with %v", got)
	}
	_, _ = store.SetAgentStatus("sb", pb.AgentStatus_AGENT_STATUS_IDLE, time.Now()) // its Stop
	reg.ReassertUnready("sb", pb.AgentStatus_AGENT_STATUS_IDLE)
	if got := store.get("sb"); got != pb.AgentStatus_AGENT_STATUS_STARTING {
		t.Errorf("the stray IDLE = %v after reasserting, want STARTING", got)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "sb: agent never reported ready; status starting put back over a stray idle") {
		t.Errorf("logs = %q, want the one rewrite", logs)
	}
}
