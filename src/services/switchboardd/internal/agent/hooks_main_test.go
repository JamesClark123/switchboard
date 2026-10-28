package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// mainPID is the main agent process the test servers bind.
const mainPID = 42

// mainServer is a hook server whose sandboxes "sb" and "other" each have a
// generation-1 agent with its main process bound to mainPID: events built
// with m() are the main agent's.
func mainServer(t *testing.T, st StatusUpdater) *HookServer {
	t.Helper()
	s, _ := tokenServer(t, st)
	for _, id := range []string{"sb", "other"} {
		if id != "sb" {
			if _, err := s.tokens.Issue(id); err != nil {
				t.Fatal(err)
			}
		}
		s.tokens.BindMain(id, 1, mainPID)
	}
	return s
}

// tokenServer is a hook server with a token index in which "sb" has one
// generation, its main process not bound yet.
func tokenServer(t *testing.T, st StatusUpdater) (*HookServer, Spawn) {
	t.Helper()
	tokens, reg := openTokens(t, t.TempDir())
	t.Cleanup(func() { _ = reg.Close() })
	sp, err := tokens.Issue("sb")
	if err != nil {
		t.Fatal(err)
	}
	s := NewHookServer(NewHub("h"), st)
	s.SetTokens(tokens)
	return s, sp
}

// m marks a payload as the main agent's: generation 1's verified token, from
// mainPID unless it names another process.
func m(p hookPayload) hookPayload {
	p.Tokened, p.Gen = true, 1
	if p.PID == 0 {
		p.PID = mainPID
	}
	return p
}

// waitReleased waits for an asynchronous hold release.
func waitReleased(t *testing.T, st *evidenceStatus) { waitReleasedFor(t, st, 3*time.Second) }

func waitReleasedFor(t *testing.T, st *evidenceStatus, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		n := st.released
		st.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// post sends one hook callback over HTTP with the given headers.
func post(s *HookServer, event, sandboxID, body string, headers map[string]string) int {
	req := httptest.NewRequest(http.MethodPost, "/hook?event="+event+"&sandbox_id="+sandboxID, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handle(rec, req)
	return rec.Code
}

func TestBuildSettingsCarriesNoSecret(t *testing.T) {
	set := BuildSettings("sb-1", "http://host.docker.internal:8765/hook")
	for event, ms := range set.Hooks {
		cmd := ms[0].Hooks[0].Command
		for _, want := range []string{
			`-H "X-Sxb-Token: $SXB_HOOK_TOKEN"`, `-H "X-Sxb-Gen: $SXB_AGENT_GEN"`, `-H "X-Sxb-Pid: $PPID"`,
			"--data-binary @-", "event=" + event + "&sandbox_id=sb-1",
		} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s: %q missing %q", event, cmd, want)
			}
		}
	}
}

// Research R13: a token the sandbox does not have is dropped; no token counts
// for status display only.
func TestHookTokenCheck(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"}, &pb.Sandbox{Id: "other"})
	s, sp := tokenServer(t, st)
	otherSp, _ := s.tokens.Issue("other")
	start := `{"hook_event_name":"SessionStart","source":"startup","session_id":"s1"}`
	hdr := func(gen, tok, pid string) map[string]string {
		return map[string]string{headerGen: gen, headerToken: tok, headerPID: pid}
	}
	for name, h := range map[string]map[string]string{
		"wrong token":       hdr("1", "nope", "10"),
		"another sandbox's": hdr("1", otherSp.Token, "10"),
		"wrong generation":  hdr("2", sp.Token, "10"),
	} {
		if code := post(s, "SessionStart", "sb", start, h); code != http.StatusForbidden {
			t.Errorf("%s: code %d, want 403", name, code)
		}
	}
	if st.agent("sb").GetStatus() != pb.AgentStatus_AGENT_STATUS_UNSPECIFIED {
		t.Error("a dropped call applies nothing")
	}
	// Tokenless: status, but no evidence, no main binding, no ready.
	ready := 0
	s.SetOnReady(func(string) { ready++ })
	if code := post(s, "SessionStart", "sb", start, nil); code != http.StatusNoContent {
		t.Fatalf("tokenless code %d", code)
	}
	if a := st.agent("sb"); a.GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE || a.GetLastSessionStartAt() != nil || a.GetSessionId() != "" {
		t.Errorf("tokenless = %+v; want status only", a)
	}
	if ready != 1 {
		t.Error("a tokenless start keeps today's readiness")
	}
	if _, _, ok := s.tokens.MainPID("sb", 1); ok {
		t.Error("a tokenless call binds nothing")
	}
	// The first hook of the new agent: accepted, binds, counts as main.
	if code := post(s, "SessionStart", "sb", start, hdr("1", sp.Token, "10")); code != http.StatusNoContent {
		t.Fatalf("own token code %d", code)
	}
	if a := st.agent("sb"); a.GetSessionId() != "s1" || a.GetLastSessionStartAt() == nil {
		t.Errorf("the main agent's start is evidence: %+v", a)
	}
	// The legacy body form is tokenless too.
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(`{"event":"UserPromptSubmit","sandbox_id":"sb"}`))
	rec := httptest.NewRecorder()
	s.Handle(rec, req)
	if rec.Code != http.StatusNoContent || st.agent("sb").GetLastSubmitAt() != nil {
		t.Errorf("legacy = %d, evidence %v", rec.Code, st.agent("sb").GetLastSubmitAt())
	}
}

// Research R13: the main agent is matched by process; nested sessions and
// processes left by an earlier generation change nothing.
func TestMainAgentByProcess(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"})
	s, _ := tokenServer(t, st)
	ch, cancel := s.Tap("sb")
	defer cancel()
	ready := 0
	s.SetOnReady(func(string) { ready++ })
	at := func(p hookPayload, pid int) hookPayload { p.Tokened, p.Gen, p.PID = true, 1, pid; return p }

	// Before the PID is bound, events count for status only.
	s.handle(at(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb"}, 100))
	if st.agent("sb").GetLastSubmitAt() != nil || len(ch) != 0 {
		t.Error("nothing is main before the bind")
	}
	// A --continue agent's first SessionStart is a resume: it binds.
	s.handle(at(hookPayload{Event: "SessionStart", Source: "resume", SandboxID: "sb", SessionID: "s1"}, 100))
	if st.agent("sb").GetSessionId() != "s1" || ready != 1 {
		t.Errorf("a resume binds the main session: %+v (ready %d)", st.agent("sb"), ready)
	}
	<-ch
	// A nested `claude -c -p`: same token, another PID.
	s.handle(at(hookPayload{Event: "SessionStart", Source: "resume", SandboxID: "sb", SessionID: "nested"}, 200))
	s.handle(at(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb", SessionID: "nested"}, 200))
	s.handle(at(hookPayload{Event: "Stop", SandboxID: "sb", SessionID: "nested"}, 200))
	if a := st.agent("sb"); a.GetSessionId() != "s1" || a.GetLastSubmitAt() != nil || a.GetLastTurnEndAt() != nil || len(ch) != 0 {
		t.Errorf("a nested session changed the record or the tap: %+v (%d)", a, len(ch))
	}
	if ready != 1 {
		t.Error("a nested session's start does not report the agent ready")
	}
	// The developer's /resume from the main process moves the session; the
	// main agent's next Stop still reaches the tap.
	s.handle(at(hookPayload{Event: "SessionStart", Source: "resume", SandboxID: "sb", SessionID: "s2"}, 100))
	s.handle(at(hookPayload{Event: "Stop", SandboxID: "sb"}, 100))
	if a := st.agent("sb"); a.GetSessionId() != "s2" || a.GetLastTurnEndAt() == nil {
		t.Errorf("main = %+v", a)
	}
	if len(ch) != 2 {
		t.Errorf("tap = %d events, want the resume and the Stop", len(ch))
	}
	// A newer generation: the old one's processes are no longer main, and a
	// SessionStart left by the old generation binds nothing.
	if _, err := s.tokens.Issue("sb"); err != nil {
		t.Fatal(err)
	}
	s.handle(at(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", SessionID: "old"}, 300))
	if _, _, ok := s.tokens.MainPID("sb", 2); ok || st.agent("sb").GetSessionId() != "s2" {
		t.Error("a process of the previous generation must not bind")
	}
	s.handle(at(hookPayload{Event: "Stop", SandboxID: "sb"}, 100))
	if len(ch) != 2 {
		t.Error("the previous generation's main agent is not main any more")
	}
}

func heldGen(id string, holdGen int64) *pb.Sandbox {
	sb := heldSandbox(id, pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER)
	sb.Controller.Run.HoldGen = holdGen
	sb.Agent = &pb.AgentSession{SessionId: "s-old"}
	return sb
}

// A startup ends a hold only from a generation spawned after it began.
func TestStartupEndsAHoldOnlyFromALaterGeneration(t *testing.T) {
	st := newEvidenceStatus(heldGen("sb", 1))
	s := mainServer(t, st) // generation 1, main PID bound
	s.handle(m(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", SessionID: "x"}))
	if sb, _ := st.Get("sb"); sb.GetController().GetRun() == nil {
		t.Fatal("a startup from the hold's own generation ends nothing")
	}
	// A `claude -p` nested in generation 1 (another PID) ends nothing either.
	s.handle(m(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", PID: 7}))
	if sb, _ := st.Get("sb"); sb.GetController().GetRun() == nil {
		t.Fatal("a nested startup ends nothing")
	}
	// The developer opens the terminal: generation 2.
	sp, _ := s.tokens.Issue("sb")
	s.handle(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", Tokened: true, Gen: sp.Gen, PID: 99})
	if sb, _ := st.Get("sb"); sb.GetController().GetRun() != nil {
		t.Error("a later generation's startup ends the hold")
	}
}

// A clear ends a hold only with a new session id and the daemon's screen
// showing the post-clear state, polled for clearSettle after the hook.
func TestClearEndsAHoldOnlyWhenCorroborated(t *testing.T) {
	defer func(d time.Duration) { clearSettle = d }(clearSettle)
	clearSettle = 600 * time.Millisecond
	cleared, conversation := loadFixture(t, "clear-banner"), loadFixture(t, "model-set-opus")
	run := func(sessionID string, lines func(since time.Duration) ([]termview.Line, bool)) *evidenceStatus {
		st := newEvidenceStatus(heldGen("sb", 0))
		s := mainServer(t, st)
		begin := time.Now()
		var mu sync.Mutex
		repaints := 0
		s.SetRepaint(func(string) bool { mu.Lock(); repaints++; mu.Unlock(); return true })
		s.SetScreen(func(string) ([]termview.Line, bool, bool) {
			l, stale := lines(time.Since(begin))
			return l, stale, true
		})
		s.handle(m(hookPayload{Event: "SessionStart", Source: "clear", SandboxID: "sb", SessionID: sessionID}))
		return st
	}
	ended := func(st *evidenceStatus) bool {
		waitReleasedFor(t, st, clearSettle+300*time.Millisecond)
		sb, _ := st.Get("sb")
		return sb.GetController().GetRun() == nil
	}
	// Forged: the old session id.
	if ended(run("s-old", func(time.Duration) ([]termview.Line, bool) { return cleared, false })) {
		t.Error("a clear naming the recorded session ends nothing")
	}
	// The conversation still on screen after clearSettle.
	if st := run("s-new", func(time.Duration) ([]termview.Line, bool) { return conversation, false }); ended(st) {
		t.Error("a clear with the conversation still on screen ends nothing")
	} else if st.agent("sb").GetSessionId() != "s-new" {
		t.Error("the main agent's clear still updates the session id")
	}
	// A real clear whose redraw lands 300 ms after the hook (stale meanwhile).
	if !ended(run("s-new", func(d time.Duration) ([]termview.Line, bool) {
		if d < 300*time.Millisecond {
			return conversation, true
		}
		return cleared, false
	})) {
		t.Error("a real clear ends the hold")
	}
	// No screen at all: nothing is corroborated.
	st := newEvidenceStatus(heldGen("sb", 0))
	mainServer(t, st).handle(m(hookPayload{Event: "SessionStart", Source: "clear", SandboxID: "sb", SessionID: "s-new"}))
	if ended(st) {
		t.Error("without a screen a clear ends nothing")
	}
}

// A developer's /clear while the screen is stale still updates the session id.
func TestClearOnAStaleScreenUpdatesTheSession(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", Agent: &pb.AgentSession{SessionId: "a"}})
	s := mainServer(t, st)
	s.SetScreen(screenOf(nil, true, true))
	s.handle(m(hookPayload{Event: "SessionStart", Source: "clear", SandboxID: "sb", SessionID: "b"}))
	if st.agent("sb").GetSessionId() != "b" {
		t.Error("session id not updated")
	}
}

// FR-223 (research R10): no task-complete for a setup or cleanup turn.
func TestNoTaskCompleteForSetupOrCleanup(t *testing.T) {
	for _, c := range []struct {
		role pb.StepRole
		want int
	}{
		{pb.StepRole_STEP_ROLE_SETUP, 0},
		{pb.StepRole_STEP_ROLE_CLEANUP, 0},
		{pb.StepRole_STEP_ROLE_ACTION, 1},
		{pb.StepRole_STEP_ROLE_UNSPECIFIED, 1},
	} {
		sb := &pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
			Controller: &pb.SandboxController{Run: &pb.WorkerRun{Id: "r", StepRole: c.role}}}
		st := newEvidenceStatus(sb)
		hub := NewHub("h")
		s := NewHookServer(hub, st)
		var fire func()
		s.afterFunc = func(_ time.Duration, f func()) stopper { fire = f; return &fakeTimer{} }
		s.handle(hookPayload{Event: "Stop", SandboxID: "sb"})
		s.handle(hookPayload{Event: "Stop", SandboxID: "sb", PendingBackground: 1})
		fire()
		n := 0
		for _, ev := range hub.Undelivered() {
			if ev.GetKind() == pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE {
				n++
			}
		}
		if n != 2*c.want {
			t.Errorf("%v: %d task-complete notifications, want %d", c.role, n, 2*c.want)
		}
	}
}

func TestReadHookInputStreams(t *testing.T) {
	// A 5 MiB PreToolUse body: read, its session id found after the big value.
	var b bytes.Buffer
	b.WriteString(`{"hook_event_name":"PreToolUse","tool_input":{"content":"`)
	b.WriteString(strings.Repeat(`x\"y`, 5<<20/4))
	b.WriteString(`","n":[1,2,{"a":null}]},"flag":true,"num":-1.5e3,"session_id":"big-one"}`)
	in := readHookInput(&b)
	if in.HookEventName != "PreToolUse" || in.SessionID != "big-one" {
		t.Errorf("5 MiB body = %+v", in)
	}
	for body, want := range map[string]hookInput{
		`{"source":"clear","session_id":"a\"b"}`: {Source: "clear", SessionID: `a"b`},
		`{"session_id":12,"source":"startup"}`:   {Source: "startup"},
		`{"session_id":"x"`:                      {SessionID: "x"}, // ends early
		`[1]`:                                    {},
		`{"a" 1}`:                                {},
		`{"a":1;}`:                               {},
		`{,}`:                                    {},
		`{"a":}`:                                 {},
		`{"a":"unterminated`:                     {},
		`{"a":{"b":"c`:                           {},
		`{"a":"\`:                                {},
		`{"session_id":"` + strings.Repeat("s", maxHookField) + `","source":"x"}`: {Source: "x"},
	} {
		got := readHookInput(strings.NewReader(body))
		if got.Source != want.Source || got.SessionID != want.SessionID {
			t.Errorf("%.40q = %+v, want %+v", body, got, want)
		}
	}
	tasks, _ := json.Marshal(map[string]any{"background_tasks": []backgroundTask{{ID: "1", Status: "running"}, {ID: "2", Status: "completed"}}})
	if in := readHookInput(bytes.NewReader(tasks)); pendingBackground(in.BackgroundTasks) != 1 {
		t.Errorf("tasks = %+v", in.BackgroundTasks)
	}
	many := `{"background_tasks":[` + strings.Repeat(`{"id":"1","status":"completed"},`, maxHookField/30) + `{}]}`
	if in := readHookInput(strings.NewReader(many)); pendingBackground(in.BackgroundTasks) == 0 {
		t.Error("a task list too long to read counts as pending")
	}
	if in := readHookInput(strings.NewReader(`{"background_tasks":[`)); len(in.BackgroundTasks) != 0 {
		t.Error("a truncated list is no list")
	}
}

// An orphan whose exec client was killed keeps a working token: a client
// exit never retires it (research R13/R14).
func TestClientExitKeepsTheToken(t *testing.T) {
	tokens, reg := openTokens(t, t.TempDir())
	defer func() { _ = reg.Close() }()
	sess := newWaitSession()
	var sp Spawn
	r := NewSpawnRegistry(func(_ string, _ *pb.AgentSpec, s Spawn) (Session, error) { sp = s; return sess, nil })
	r.SetHookTokens(tokens)
	if _, err := r.Session("sb", &pb.AgentSpec{Kind: "shell"}); err != nil {
		t.Fatal(err)
	}
	sess.exit()
	deadline := time.Now().Add(2 * time.Second)
	for r.Live("sb") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := tokens.Verify("sb", "1", sp.Token); !ok {
		t.Error("the token survives the client's exit")
	}
}
