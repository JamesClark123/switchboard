package agent

import (
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// recSession records everything written to it; reads block forever.
type recSession struct {
	mu     sync.Mutex
	buf    strings.Builder
	closed bool
}

func (s *recSession) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Write(b)
	return len(b), nil
}
func (s *recSession) Read([]byte) (int, error)    { select {} }
func (s *recSession) Resize(uint16, uint16) error { return nil }
func (s *recSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *recSession) typed() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// agentState is a readable status side whose record the test moves.
type agentState struct {
	mu     sync.Mutex
	status pb.AgentStatus
	lastAt time.Time
}

func (a *agentState) SetAgentStatus(id string, s pb.AgentStatus, now time.Time) (*pb.Sandbox, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.lastAt = s, now
	return &pb.Sandbox{Id: id}, nil
}

func (a *agentState) Get(id string) (*pb.Sandbox, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &pb.Sandbox{Id: id, Agent: &pb.AgentSession{Status: a.status, LastEventAt: timestamppb.New(a.lastAt)}}, nil
}

// startedIdle starts the sandbox's session and then records its agent ready and
// IDLE with its last event in the past — an agent that finished starting
// (creating a session records it STARTING, SWI-15).
func startedIdle(t *testing.T, reg *Registry, state *agentState, sandboxID string) {
	t.Helper()
	if _, err := reg.Session(sandboxID, &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	reg.MarkReady(sandboxID)
	state.mu.Lock()
	state.status, state.lastAt = pb.AgentStatus_AGENT_STATUS_IDLE, time.Now().Add(-time.Minute)
	state.mu.Unlock()
}

func fastSubmit(t *testing.T) {
	t.Helper()
	d, c, n := promptSubmitDelay, submitCheckDelay, submitRetries
	promptSubmitDelay, submitCheckDelay, submitRetries = 0, 20*time.Millisecond, 3
	t.Cleanup(func() { promptSubmitDelay, submitCheckDelay, submitRetries = d, c, n })
}

// TestPromptRetriesEnterWhileUnsubmitted: a prompt typed into an idle agent
// that never reports anything gets its Enter pressed again — "\r" once more,
// then the keypad Enter a transport cannot rewrite — and each retry is logged.
func TestPromptRetriesEnterWhileUnsubmitted(t *testing.T) {
	fastSubmit(t)
	sess := &recSession{}
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return sess, nil })
	state := &agentState{status: pb.AgentStatus_AGENT_STATUS_IDLE, lastAt: time.Now().Add(-time.Minute)}
	reg.SetStatusUpdater(state)
	startedIdle(t, reg, state, "sb1")
	var logMu sync.Mutex
	var logged []string
	reg.SetLogger(func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, format)
	})
	if err := reg.Prompt("sb1", &pb.AgentSpec{}, "hello"); err != nil {
		t.Fatal(err)
	}
	want := Typed("hello") + submitKey + keypadEnter + keypadEnter
	waitFor(t, func() bool { return sess.typed() == want })
	time.Sleep(3 * submitCheckDelay)
	if got := sess.typed(); got != want {
		t.Errorf("typed = %q, want %q (no retries past submitRetries)", got, want)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if len(logged) != submitRetries {
		t.Errorf("logged %d retries, want %d", len(logged), submitRetries)
	}
}

// TestPromptStopsRetryingOnceReported: the agent's hook moving it to WORKING,
// or any hook event after the prompt, ends the retries.
func TestPromptStopsRetryingOnceReported(t *testing.T) {
	fastSubmit(t)
	for _, c := range []struct {
		name   string
		report func(a *agentState)
	}{
		{"working", func(a *agentState) { _, _ = a.SetAgentStatus("sb1", pb.AgentStatus_AGENT_STATUS_WORKING, time.Now()) }},
		// A fast turn: submitted, answered, idle again — the event time tells.
		{"event after the prompt", func(a *agentState) { _, _ = a.SetAgentStatus("sb1", pb.AgentStatus_AGENT_STATUS_IDLE, time.Now()) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			sess := &recSession{}
			reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return sess, nil })
			state := &agentState{status: pb.AgentStatus_AGENT_STATUS_IDLE, lastAt: time.Now().Add(-time.Minute)}
			reg.SetStatusUpdater(state)
			startedIdle(t, reg, state, "sb1")
			if err := reg.Prompt("sb1", &pb.AgentSpec{}, "hello"); err != nil {
				t.Fatal(err)
			}
			c.report(state)
			time.Sleep(4 * submitCheckDelay)
			if got := sess.typed(); got != Typed("hello") {
				t.Errorf("typed = %q, want just the prompt", got)
			}
		})
	}
}

// TestPromptRetriesOnlyForTheLiveSession: a closed (replaced) session is left
// alone, and an agent that was not idle when typed is not verified at all.
func TestPromptRetriesOnlyForTheLiveSession(t *testing.T) {
	fastSubmit(t)
	sess := &recSession{}
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return sess, nil })
	state := &agentState{status: pb.AgentStatus_AGENT_STATUS_IDLE, lastAt: time.Now().Add(-time.Minute)}
	reg.SetStatusUpdater(state)
	startedIdle(t, reg, state, "sb1")
	if err := reg.Prompt("sb1", &pb.AgentSpec{}, "hello"); err != nil {
		t.Fatal(err)
	}
	reg.Close("sb1")
	time.Sleep(4 * submitCheckDelay)
	if got := sess.typed(); got != Typed("hello") {
		t.Errorf("closed session typed = %q, want just the prompt", got)
	}

	busy := &recSession{}
	reg2 := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return busy, nil })
	reg2.SetStatusUpdater(&agentState{status: pb.AgentStatus_AGENT_STATUS_WORKING})
	if err := reg2.Prompt("sb2", &pb.AgentSpec{}, "more"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * submitCheckDelay)
	if got := busy.typed(); got != Typed("more") {
		t.Errorf("busy agent typed = %q, want just the prompt", got)
	}
}

// TestPromptIntoWaitingAgentIsVerified: an agent WAITING on background work
// (SWI-12) sits at its input box like an idle one, so a prompt typed into it is
// verified the same way — its Enter retried while nothing reports (a busy agent
// is never verified; see TestPromptRetriesOnlyForTheLiveSession).
func TestPromptIntoWaitingAgentIsVerified(t *testing.T) {
	fastSubmit(t)
	sess := &recSession{}
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return sess, nil })
	state := &agentState{}
	reg.SetStatusUpdater(state)
	startedIdle(t, reg, state, "sb1")
	state.mu.Lock()
	state.status = pb.AgentStatus_AGENT_STATUS_WAITING // a started agent waiting on background work (SWI-12)
	state.mu.Unlock()
	if err := reg.Prompt("sb1", &pb.AgentSpec{}, "hello"); err != nil {
		t.Fatal(err)
	}
	want := Typed("hello") + submitKey + keypadEnter + keypadEnter
	waitFor(t, func() bool { return sess.typed() == want })
	time.Sleep(3 * submitCheckDelay) // let the retry goroutine finish before fastSubmit restores its knobs
}
