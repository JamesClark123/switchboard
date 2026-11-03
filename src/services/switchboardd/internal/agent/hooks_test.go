package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// evidenceStatus is a full status side (SWI-17): status, evidence, hold
// release and listing over in-memory records, counting evidence writes.
type evidenceStatus struct {
	mu       sync.Mutex
	sbs      map[string]*pb.Sandbox
	evidence int
	released int
}

func newEvidenceStatus(sbs ...*pb.Sandbox) *evidenceStatus {
	e := &evidenceStatus{sbs: map[string]*pb.Sandbox{}}
	for _, sb := range sbs {
		if sb.Agent == nil {
			sb.Agent = &pb.AgentSession{}
		}
		e.sbs[sb.GetId()] = sb
	}
	return e
}

func (e *evidenceStatus) Get(id string) (*pb.Sandbox, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sb, ok := e.sbs[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return proto.Clone(sb).(*pb.Sandbox), nil
}

func (e *evidenceStatus) List() ([]*pb.Sandbox, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*pb.Sandbox
	for _, sb := range e.sbs {
		out = append(out, proto.Clone(sb).(*pb.Sandbox))
	}
	return out, nil
}

func (e *evidenceStatus) SetAgentStatus(id string, st pb.AgentStatus, _ time.Time) (*pb.Sandbox, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sb, ok := e.sbs[id]
	if !ok {
		return nil, errors.New("not found")
	}
	sb.Agent.Status = st
	return proto.Clone(sb).(*pb.Sandbox), nil
}

func (e *evidenceStatus) SetAgentEvidence(id string, fn func(*pb.AgentSession)) (*pb.Sandbox, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sb, ok := e.sbs[id]
	if !ok {
		return nil, errors.New("not found")
	}
	fn(sb.Agent)
	e.evidence++
	return proto.Clone(sb).(*pb.Sandbox), nil
}

func (e *evidenceStatus) ClearHeldRun(id string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sb := e.sbs[id]
	if sb.GetController().GetRun().GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		return false, nil
	}
	sb.Controller.Run = nil
	e.released++
	return true, nil
}

func (e *evidenceStatus) agent(id string) *pb.AgentSession {
	sb, _ := e.Get(id)
	return sb.GetAgent()
}

func (e *evidenceStatus) writes() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.evidence
}

// tickingNow returns a clock that advances a second per reading.
func tickingNow() func() time.Time {
	var mu sync.Mutex
	t := time.Unix(1_700_000_000, 0)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(time.Second)
		return t
	}
}

func TestHookTapOrderAndCancel(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"}, &pb.Sandbox{Id: "other"})
	s := mainServer(t, st)
	ch, cancel := s.Tap("sb")
	s.handle(m(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb", SessionID: "s-1", PID: 42}))
	s.handle(m(hookPayload{Event: "PreToolUse", SandboxID: "sb"}))
	s.handle(m(hookPayload{Event: "UserPromptSubmit", SandboxID: "other"})) // another sandbox's
	s.handle(m(hookPayload{Event: "SessionStart", Source: "compact", SandboxID: "sb"}))
	s.handle(m(hookPayload{Event: "Notification", NotificationType: "permission_prompt", SandboxID: "sb"}))
	s.handle(m(hookPayload{Event: "Stop", SandboxID: "sb", PendingBackground: 2}))
	var got []HookEvent
	for len(ch) > 0 {
		got = append(got, <-ch)
	}
	want := []string{"UserPromptSubmit", "PreToolUse", "Notification", "Stop"}
	if len(got) != len(want) {
		t.Fatalf("tap got %+v, want %v", got, want)
	}
	for i, ev := range got {
		if ev.Event != want[i] {
			t.Errorf("event %d = %s, want %s", i, ev.Event, want[i])
		}
	}
	if got[0].SessionID != "s-1" || got[0].PID != 42 || got[0].At.IsZero() {
		t.Errorf("identity not carried: %+v", got[0])
	}
	if !got[2].NeedsInput || got[2].NotificationType != "permission_prompt" {
		t.Errorf("needs-input not marked: %+v", got[2])
	}
	if got[3].BackgroundTasks != 2 {
		t.Errorf("background tasks = %d", got[3].BackgroundTasks)
	}
	cancel()
	cancel() // idempotent
	s.handle(m(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb"}))
	if len(ch) != 0 {
		t.Error("a cancelled tap must receive nothing")
	}
}

func TestHookTapIsBestEffort(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"})
	s := mainServer(t, st)
	ch, cancel := s.Tap("sb")
	defer cancel()
	for range tapBuffer + 5 {
		s.handle(m(hookPayload{Event: "PreToolUse", SandboxID: "sb"}))
	}
	if len(ch) != tapBuffer {
		t.Errorf("a full tap must drop, not block: %d buffered", len(ch))
	}
}

// SWI-28: a turn's first tool call stands in for a lost submit, and the rest of
// its tool calls write nothing.
func TestHookPreToolUseRecordsALostTurnStart(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"})
	s := mainServer(t, st)
	s.now = tickingNow()
	s.afterFunc = func(time.Duration, func()) stopper { return &fakeTimer{} }

	s.handle(m(hookPayload{Event: "PreToolUse", SandboxID: "sb"}))
	first := st.agent("sb").GetLastSubmitAt()
	if first == nil {
		t.Fatal("a tool call with no submit must record the turn start")
	}
	s.handle(m(hookPayload{Event: "PreToolUse", SandboxID: "sb"}))
	if got := st.agent("sb").GetLastSubmitAt(); !got.AsTime().Equal(first.AsTime()) {
		t.Error("a turn's later tool calls must write nothing")
	}
	s.handle(m(hookPayload{Event: "Stop", SandboxID: "sb"}))
	s.handle(m(hookPayload{Event: "PreToolUse", SandboxID: "sb"}))
	if got := st.agent("sb").GetLastSubmitAt(); !got.AsTime().After(first.AsTime()) {
		t.Error("the next turn's first tool call is a new turn start")
	}
	// A submit that arrived: its tool calls add nothing.
	s.handle(m(hookPayload{Event: "Stop", SandboxID: "sb"}))
	s.handle(m(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb"}))
	submit := st.agent("sb").GetLastSubmitAt()
	s.handle(m(hookPayload{Event: "PreToolUse", SandboxID: "sb"}))
	if got := st.agent("sb").GetLastSubmitAt(); !got.AsTime().Equal(submit.AsTime()) {
		t.Error("a tool call after a recorded submit must write nothing")
	}
}

// SWI-28: an older turn start with no recorded end (a turn interrupted with Esc)
// doesn't hide the first tool call after a step is typed.
func TestHookPreToolUseAfterAnUnendedTurn(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"})
	s := mainServer(t, st)
	s.now = tickingNow()
	s.afterFunc = func(time.Duration, func()) stopper { return &fakeTimer{} }

	s.handle(m(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb"}))
	old := st.agent("sb").GetLastSubmitAt()
	st.mu.Lock()
	st.sbs["sb"].Controller = &pb.SandboxController{Run: &pb.WorkerRun{Id: "r", StepSentAt: timestamppb.New(old.AsTime().Add(time.Millisecond))}}
	st.mu.Unlock()
	s.handle(m(hookPayload{Event: "PreToolUse", SandboxID: "sb"}))
	first := st.agent("sb").GetLastSubmitAt()
	if !first.AsTime().After(old.AsTime()) {
		t.Fatal("the first tool call after a send must record the turn start")
	}
	s.handle(m(hookPayload{Event: "PreToolUse", SandboxID: "sb"}))
	if got := st.agent("sb").GetLastSubmitAt(); !got.AsTime().Equal(first.AsTime()) {
		t.Error("a turn's later tool calls must write nothing")
	}
}

// SWI-28: a submit that isn't the step typed is marked as another prompt; with
// nothing to compare, it counts as before.
func TestHookOtherPrompt(t *testing.T) {
	const step = "[frame]\n\tdo\tthe  thing"
	sent := &pb.SandboxController{Run: &pb.WorkerRun{Id: "r", StepSentAt: timestamppb.New(time.Unix(1, 0))}}
	for _, c := range []struct {
		name   string
		ctrl   *pb.SandboxController
		typed  string
		prompt string
		other  bool
	}{
		{"the step", sent, step, step, false},
		{"the step as reported", sent, step, "\n\n<pasted_content id=\"614b\">\n[frame]\n  do  the  thing\n</pasted_content id=\"614b\">\n", false},
		{"the developer's", sent, step, "what is going on?", true},
		{"no prompt", sent, step, "", false},
		{"nothing typed", sent, "", "what is going on?", false},
		{"no run", nil, step, "what is going on?", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := newEvidenceStatus(&pb.Sandbox{Id: "sb", Controller: c.ctrl})
			s := mainServer(t, st)
			s.now = tickingNow()
			s.SetTypedStep(func(string) string { return c.typed })
			s.handle(m(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb", Prompt: c.prompt}))
			a := st.agent("sb")
			if a.GetLastSubmitAt() == nil {
				t.Error("every submit records last_submit_at")
			}
			if got := a.GetLastOtherSubmitAt() != nil; got != c.other {
				t.Errorf("other = %v, want %v", got, c.other)
			}
		})
	}
	t.Run("not wired", func(t *testing.T) {
		st := newEvidenceStatus(&pb.Sandbox{Id: "sb", Controller: sent})
		s := mainServer(t, st)
		s.handle(m(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb", Prompt: "hi"}))
		if st.agent("sb").GetLastOtherSubmitAt() != nil {
			t.Error("with no typed text to compare, a submit is not marked")
		}
	})
}

// SWI-28: what a submit does to last_other_submit_at depends on what this send
// already has on record: a later prompt can't erase the step's turn start, and the
// step's own submit outweighs a prompt before it.
func TestHookOtherPromptOrder(t *testing.T) {
	const step, other = "do the thing", "what is going on?"
	submit := func(prompt string) hookPayload {
		return hookPayload{Event: "UserPromptSubmit", SandboxID: "sb", Prompt: prompt}
	}
	tool := hookPayload{Event: "PreToolUse", SandboxID: "sb"}
	sent := time.Unix(1, 0)
	ctrl := &pb.SandboxController{Run: &pb.WorkerRun{Id: "r", StepSentAt: timestamppb.New(sent)}}
	for _, c := range []struct {
		name   string
		events []hookPayload
		other  bool
	}{
		{"the step, then another", []hookPayload{submit(step), submit(other), submit(other)}, false},
		{"a tool call with the submit lost, then another", []hookPayload{tool, submit(other)}, false},
		{"can't tell, then another", []hookPayload{submit(""), submit(other)}, false},
		{"another, then the step", []hookPayload{submit(other), submit(step)}, false},
		{"another, the step, another", []hookPayload{submit(other), submit(step), submit(other)}, false},
		{"another", []hookPayload{submit(other)}, true},
		{"another, then a tool call", []hookPayload{submit(other), tool}, true},
		{"another, then can't tell", []hookPayload{submit(other), submit("")}, true},
		{"another, can't tell, another", []hookPayload{submit(other), submit(""), submit(other)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := newEvidenceStatus(&pb.Sandbox{Id: "sb", Controller: ctrl})
			s := mainServer(t, st)
			s.now = tickingNow()
			s.SetTypedStep(func(string) string { return step })
			for _, ev := range c.events {
				s.handle(m(ev))
			}
			a := st.agent("sb")
			if !a.GetLastSubmitAt().AsTime().After(sent) {
				t.Errorf("last_submit_at = %v, sent %v", a.GetLastSubmitAt(), sent)
			}
			if got := a.GetLastOtherSubmitAt() != nil; got != c.other {
				t.Errorf("other = %v, want %v", got, c.other)
			}
		})
	}
	// A start from before this send is no start of this step's.
	t.Run("a start before the send", func(t *testing.T) {
		st := newEvidenceStatus(&pb.Sandbox{Id: "sb", Controller: ctrl, Agent: &pb.AgentSession{LastSubmitAt: timestamppb.New(sent.Add(-time.Second))}})
		s := mainServer(t, st)
		s.now = tickingNow()
		s.SetTypedStep(func(string) string { return step })
		s.handle(m(submit(other)))
		if st.agent("sb").GetLastOtherSubmitAt() == nil {
			t.Error("a start before the send must not hide another prompt")
		}
	})
}

func TestHookEvidencePerEvent(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"})
	s := mainServer(t, st)
	s.now = tickingNow()
	s.afterFunc = func(time.Duration, func()) stopper { return &fakeTimer{} }

	s.handle(m(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb"}))
	if st.agent("sb").GetLastSubmitAt() == nil {
		t.Error("UserPromptSubmit must record last_submit_at")
	}
	s.handle(m(hookPayload{Event: "Stop", SandboxID: "sb", PendingBackground: 1}))
	if st.agent("sb").GetLastTurnEndAt() != nil {
		t.Error("a Stop with background work pending is not a turn end")
	}
	s.handle(m(hookPayload{Event: "Stop", SandboxID: "sb"}))
	if st.agent("sb").GetLastTurnEndAt() == nil {
		t.Error("a Stop with nothing pending records last_turn_end_at")
	}
	s.handle(m(hookPayload{Event: "Notification", NotificationType: notifyIdle, SandboxID: "sb"}))
	if st.agent("sb").GetLastNeedsInputAt() != nil {
		t.Error("the idle nudge is not a request for input")
	}
	s.handle(m(hookPayload{Event: "Notification", NotificationType: "elicitation_dialog", SandboxID: "sb"}))
	if st.agent("sb").GetLastNeedsInputAt() == nil {
		t.Error("a needs-input Notification records last_needs_input_at")
	}
	s.handle(m(hookPayload{Event: "SessionStart", Source: "compact", SandboxID: "sb", SessionID: "x"}))
	if st.agent("sb").GetLastSessionStartAt() != nil || st.agent("sb").GetSessionId() != "" {
		t.Error("a compaction is not a session start")
	}
	s.handle(m(hookPayload{Event: "SessionStart", Source: "clear", SandboxID: "sb", SessionID: "s-2"}))
	a := st.agent("sb")
	if a.GetLastSessionStartAt() == nil || a.GetLastSessionSource() != "clear" || a.GetSessionId() != "s-2" {
		t.Errorf("session start evidence = %+v", a)
	}
	s.handle(m(hookPayload{Event: "SessionStart", Source: "resume", SandboxID: "sb"}))
	if a := st.agent("sb"); a.GetLastSessionSource() != "resume" || a.GetSessionId() != "s-2" {
		t.Errorf("a start with no session id keeps the recorded one: %+v", a)
	}
	// PostToolUse records nothing new.
	n := st.writes()
	s.handle(m(hookPayload{Event: "PostToolUse", SandboxID: "sb"}))
	if st.writes() != n {
		t.Error("PostToolUse is no evidence")
	}
}

func TestBackstopExpiryRecordsTurnEnd(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s := mainServer(t, st)
	var fire func()
	s.afterFunc = func(_ time.Duration, f func()) stopper { fire = f; return &fakeTimer{} }
	s.handle(m(hookPayload{Event: "Stop", SandboxID: "sb", PendingBackground: 1}))
	fire()
	if st.agent("sb").GetLastTurnEndAt() == nil {
		t.Error("an expired backstop is a turn end")
	}
}

func heldSandbox(id string, hold pb.WorkerHold) *pb.Sandbox {
	return &pb.Sandbox{Id: id, Controller: &pb.SandboxController{OrchestratorId: "o", Run: &pb.WorkerRun{Id: "run-1", Hold: hold}}}
}

func TestHookEndsAHoldOnAFreshSession(t *testing.T) {
	for _, c := range []struct {
		source string
		hold   pb.WorkerHold
		ends   bool
	}{
		{"startup", pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, true},
		{"clear", pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, true},
		{"resume", pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, false},
		{"compact", pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, false},
		{"clear", pb.WorkerHold_WORKER_HOLD_CLEANING_UP, false},
		{"startup", pb.WorkerHold_WORKER_HOLD_NONE, false},
	} {
		st := newEvidenceStatus(heldSandbox("sb", c.hold))
		s := mainServer(t, st)
		s.SetScreen(screenOf(loadFixture(t, "clear-banner"), false, true))
		s.handle(m(hookPayload{Event: "SessionStart", Source: c.source, SandboxID: "sb", SessionID: "new"}))
		if c.ends {
			waitReleased(t, st)
		}
		sb, _ := st.Get("sb")
		if ended := sb.GetController().GetRun() == nil; ended != c.ends {
			t.Errorf("%s under %v: hold ended = %v, want %v", c.source, c.hold, ended, c.ends)
		}
	}
	// No run at all: nothing to clear, nothing written.
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", Controller: &pb.SandboxController{}})
	s := mainServer(t, st)
	s.handle(m(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb"}))
	if st.released != 0 {
		t.Error("no hold: nothing is released")
	}
	// A status side that cannot release leaves the hold alone.
	held := heldSandbox("sb", pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER)
	ro := &readableStatus{fakeStatus: fakeStatus{controller: held.GetController()}}
	NewHookServer(NewHub("h"), ro).handle(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb"})
	if held.GetController().GetRun() == nil {
		t.Error("without a HoldReleaser nothing is cleared")
	}
}

// screenOf returns a ScreenFunc serving the given lines.
func screenOf(lines []termview.Line, stale, ok bool) ScreenFunc {
	return func(string) ([]termview.Line, bool, bool) { return lines, stale, ok }
}

func TestInputIdleSinceSetAndUnset(t *testing.T) {
	run := &pb.WorkerRun{Id: "run-1"}
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", Controller: &pb.SandboxController{Run: run},
		Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE}})
	s := NewHookServer(NewHub("h"), st)
	s.now = tickingNow()
	empty := loadFixture(t, "empty-after-command")
	var mu sync.Mutex
	lines, stale, ok := empty, false, true
	s.SetScreen(func(string) ([]termview.Line, bool, bool) {
		mu.Lock()
		defer mu.Unlock()
		return lines, stale, ok
	})
	set := func(l []termview.Line, st, o bool) { mu.Lock(); lines, stale, ok = l, st, o; mu.Unlock() }

	s.sampleIdle("sb")
	since := st.agent("sb").GetInputIdleSince()
	if since == nil {
		t.Fatal("an empty, ready box with the agent idle sets input_idle_since")
	}
	n := st.writes()
	s.sampleIdle("sb")
	s.sampleAll()
	if st.writes() != n || !proto.Equal(st.agent("sb").GetInputIdleSince(), since) {
		t.Error("repeated samples in the same state write nothing")
	}
	// Unknown screens change nothing.
	set(empty, true, true)
	s.sampleIdle("sb")
	set(nil, false, false)
	s.sampleIdle("sb")
	if st.writes() != n {
		t.Error("a stale or missing screen tells nothing")
	}
	// Text in the box unsets it.
	set(loadFixture(t, "text-left-in-box"), false, true)
	s.sampleIdle("sb")
	if st.agent("sb").GetInputIdleSince() != nil {
		t.Error("a box with text is not idle")
	}
	n = st.writes()
	s.sampleIdle("sb")
	if st.writes() != n {
		t.Error("an unset value is not rewritten")
	}
	// A picker is not idle; an empty box with the agent WORKING is not either.
	set(loadFixture(t, "model-picker"), false, true)
	s.sampleIdle("sb")
	set(empty, false, true)
	_, _ = st.SetAgentStatus("sb", pb.AgentStatus_AGENT_STATUS_WORKING, time.Time{})
	s.sampleIdle("sb")
	if st.agent("sb").GetInputIdleSince() != nil || st.writes() != n {
		t.Error("a modal or a working agent is not idle")
	}
	// Each hook samples too.
	_, _ = st.SetAgentStatus("sb", pb.AgentStatus_AGENT_STATUS_IDLE, time.Time{})
	s.handle(hookPayload{Event: "Notification", NotificationType: notifyIdle, SandboxID: "sb"})
	if st.agent("sb").GetInputIdleSince() == nil {
		t.Error("a hook samples input_idle_since")
	}
	// The run is gone: the value is unset, by the periodic sample.
	st.mu.Lock()
	st.sbs["sb"].Controller.Run = nil
	st.mu.Unlock()
	s.sampleAll()
	if st.agent("sb").GetInputIdleSince() != nil {
		t.Error("a sandbox with no run has no input_idle_since")
	}
}

func TestInputIdleSampleNeedsAScreenAndARecord(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", Controller: &pb.SandboxController{Run: &pb.WorkerRun{Id: "r"}}})
	s := NewHookServer(NewHub("h"), st)
	s.sampleIdle("sb") // no screen registered
	s.sampleIdle("missing")
	if st.writes() != 0 {
		t.Error("nothing to sample with")
	}
	// A status side with no reader or lister samples nothing.
	plain := NewHookServer(NewHub("h"), &fakeStatus{})
	plain.SetScreen(screenOf(nil, false, true))
	plain.sampleIdle("sb")
	plain.sampleAll()
}

func TestSampleIdleRunsUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s := NewHookServer(NewHub("h"), newEvidenceStatus())
	go func() { s.SampleIdle(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SampleIdle must return when its context ends")
	}
}

func TestHookHTTPCarriesSessionAndPID(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"})
	s, tok := tokenServer(t, st)
	ch, cancel := s.Tap("sb")
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/hook?event=SessionStart&sandbox_id=sb",
		strings.NewReader(`{"hook_event_name":"SessionStart","source":"startup","session_id":"abc"}`))
	req.Header.Set("X-Sxb-Pid", "1234")
	req.Header.Set("X-Sxb-Token", tok.Token)
	req.Header.Set("X-Sxb-Gen", "1")
	rec := httptest.NewRecorder()
	s.Handle(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
	ev := <-ch
	if ev.SessionID != "abc" || ev.PID != 1234 || ev.Source != "startup" {
		t.Errorf("event = %+v", ev)
	}
	if st.agent("sb").GetSessionId() != "abc" {
		t.Error("the session id is recorded")
	}
}
