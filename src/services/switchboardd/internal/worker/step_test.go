package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- screens: the agent package's claudeui fixtures (research R1) ---

// fixture reads one claudeui fixture screen (text + faint sidecar).
func fixture(t *testing.T, name string) []termview.Line {
	t.Helper()
	dir := filepath.Join("..", "agent", "testdata", "claudeui")
	text, err := os.ReadFile(filepath.Join(dir, name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	faint, _ := os.ReadFile(filepath.Join(dir, name+".faint"))
	rows := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
	marks := strings.Split(strings.TrimSuffix(string(faint), "\n"), "\n")
	out := make([]termview.Line, len(rows))
	for i, r := range rows {
		mask := make([]bool, len([]rune(r)))
		if i < len(marks) {
			for j, c := range []rune(marks[i]) {
				if j < len(mask) && c == '~' {
					mask[j] = true
				}
			}
		}
		out[i] = termview.Line{Text: r, Faint: mask}
	}
	return out
}

// withRows returns lines with rows replacing the first len(rows) (blank) rows:
// a transcript drawn above an empty box.
func withRows(lines []termview.Line, rows ...string) []termview.Line {
	out := append([]termview.Line(nil), lines...)
	for i, r := range rows {
		out[i] = termview.Line{Text: r, Faint: make([]bool, len([]rune(r)))}
	}
	return out
}

// blankBox is an empty, ready input box on an otherwise blank screen: the
// empty-after-command fixture with its transcript wiped.
func blankBox(t *testing.T) []termview.Line {
	t.Helper()
	lines := fixture(t, "empty-after-command")
	box := -1
	for i, l := range lines {
		if strings.HasPrefix(l.Text, "❯\u00a0") {
			box = i
		}
	}
	if box < 1 {
		t.Fatal("no input box in empty-after-command")
	}
	out := append([]termview.Line(nil), lines...)
	for i := range box - 1 {
		out[i] = termview.Line{}
	}
	return out
}

const textLeft = "please summarise the open pull requests" // text-left-in-box's text

// --- fakes ---

// fakeScreen is a scripted agent screen.
type fakeScreen struct {
	mu        sync.Mutex
	lines     []termview.Line
	stale     bool
	ok        bool
	ensured   int
	repaints  int
	ensureErr error
	// freshAfter: a stale screen turns fresh after this many repaints (0 = never).
	freshAfter int
}

func (f *fakeScreen) Screen(string) ([]termview.Line, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lines, f.stale, f.ok
}
func (f *fakeScreen) EnsureScreen(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured++
	return f.ensureErr
}
func (f *fakeScreen) RequestRepaint(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repaints++
	if f.freshAfter > 0 && f.repaints >= f.freshAfter {
		f.stale = false
	}
	return true
}
func (f *fakeScreen) set(lines []termview.Line, stale, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines, f.stale, f.ok = lines, stale, ok
}

// fakeTap is a hook tap the test pushes events into.
type fakeTap struct {
	ch     chan agent.HookEvent
	opened int
}

func (f *fakeTap) Tap(string) (<-chan agent.HookEvent, func()) { f.opened++; return f.ch, func() {} }

// fakeStepClock is a virtual clock: After advances it at once and runs what
// was scheduled up to then.
type fakeStepClock struct {
	mu    sync.Mutex
	now   time.Time
	start time.Time
	due   []scheduled
}

type scheduled struct {
	at time.Time
	fn func()
}

func (c *fakeStepClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeStepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var run []func()
	keep := c.due[:0]
	for _, s := range c.due {
		if !s.at.After(now) {
			run = append(run, s.fn)
		} else {
			keep = append(keep, s)
		}
	}
	c.due = keep
	c.mu.Unlock()
	for _, f := range run {
		f()
	}
	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}

// in schedules fn d after the clock's current time.
func (c *fakeStepClock) in(d time.Duration, fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.due = append(c.due, scheduled{at: c.now.Add(d), fn: fn})
}

func (c *fakeStepClock) elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now.Sub(c.start)
}

// fakeTyper records what is typed and lets the test script the agent's reply.
type fakeTyper struct {
	mu      sync.Mutex
	clock   *fakeStepClock
	typed   []string
	presses []int
	err     error
	onType  func(text string)
	onPress func(n int)
}

func (f *fakeTyper) TypeStep(_ string, _ *pb.AgentSpec, text string) (time.Time, error) {
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return time.Time{}, f.err
	}
	f.typed = append(f.typed, text)
	cb := f.onType
	f.mu.Unlock()
	if cb != nil {
		cb(text)
	}
	return f.clock.Now(), nil
}
func (f *fakeTyper) RePress(_ string, n int) error {
	f.mu.Lock()
	f.presses = append(f.presses, n)
	cb := f.onPress
	f.mu.Unlock()
	if cb != nil {
		cb(n)
	}
	return nil
}
func (f *fakeTyper) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.typed) }

// --- harness ---

type stepHarness struct {
	e       *env
	scr     *fakeScreen
	tap     *fakeTap
	clock   *fakeStepClock
	typer   *fakeTyper
	created *int
	id      string
}

const stepSandbox = "dddddddd-5"

// newStepHarness is a RUNNING worker of orch-1 whose ready, idle agent shows an
// empty input box, with every step dependency faked.
func newStepHarness(t *testing.T) *stepHarness {
	t.Helper()
	e := newEnv(t, "")
	created := 0
	sess := newRecSession()
	e.svc.agents = agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) { created++; return sess, nil })
	sb := e.sandbox(t, stepSandbox, "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	attachReady(t, e, sb.GetId())
	start := time.Unix(1_700_000_100, 0)
	clock := &fakeStepClock{now: start, start: start}
	h := &stepHarness{
		e: e, scr: &fakeScreen{lines: blankBox(t), ok: true},
		tap: &fakeTap{ch: make(chan agent.HookEvent, 16)}, clock: clock, created: &created, id: sb.GetId(),
	}
	h.typer = &fakeTyper{clock: clock}
	e.svc.screen, e.svc.typer, e.svc.clock = h.scr, h.typer, clock
	e.svc.SetHookTap(h.tap)
	return h
}

// event pushes a hook event stamped now.
func (h *stepHarness) event(ev agent.HookEvent) {
	ev.At = h.clock.Now()
	h.tap.ch <- ev
}

func (h *stepHarness) mutate(t *testing.T, fn func(*pb.Sandbox)) {
	t.Helper()
	if _, err := h.e.reg.Update(h.id, func(x *pb.Sandbox) error { fn(x); return nil }); err != nil {
		t.Fatal(err)
	}
}

func (h *stepHarness) run(t *testing.T) *pb.WorkerRun {
	t.Helper()
	sb, err := h.e.reg.Get(h.id)
	if err != nil {
		t.Fatal(err)
	}
	return sb.GetController().GetRun()
}

func stepReq(kind pb.StepKind, text, expect string, idx int32) *pb.DeliverWorkerStepRequest {
	role := pb.StepRole_STEP_ROLE_SETUP
	if kind == pb.StepKind_STEP_KIND_ACTION_PROMPT {
		role = pb.StepRole_STEP_ROLE_ACTION
	}
	return &pb.DeliverWorkerStepRequest{
		SandboxId: stepSandbox, OrchestratorId: "orch-1", RunId: "run-1", Action: "review-pr",
		Step: &pb.RunStep{Role: role, Kind: kind, Text: text, Expect: expect}, StepIndex: idx, First: idx == 1,
	}
}

func custom(text string, idx int32) *pb.DeliverWorkerStepRequest {
	return stepReq(pb.StepKind_STEP_KIND_CUSTOM, text, "", idx)
}

func deliverStep(t *testing.T, h *stepHarness, req *pb.DeliverWorkerStepRequest) *pb.DeliverWorkerStepResponse {
	t.Helper()
	resp, err := h.e.svc.DeliverStep(bg, req)
	if err != nil {
		t.Fatalf("DeliverStep: %v", err)
	}
	return resp
}

// --- §3: the submit check ---

func TestStepModelTurnOnUserPromptSubmit(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) {
		h.clock.in(20*time.Millisecond, func() { h.event(agent.HookEvent{Event: "UserPromptSubmit"}) })
	}
	resp := deliverStep(t, h, custom("please summarise", 1))
	if resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN || resp.GetSubmitAt() == nil || resp.GetSentAt() == nil {
		t.Fatalf("resp = %+v", resp)
	}
	if h.typer.typed[0] != "please summarise" || len(h.typer.presses) != 0 || h.tap.opened != 1 {
		t.Errorf("typed %q presses %v", h.typer.typed, h.typer.presses)
	}
	run := h.run(t)
	if run.GetId() != "run-1" || run.GetAction() != "review-pr" || run.GetStepIndex() != 1 || run.GetStepSentAt() == nil ||
		run.GetStepRole() != pb.StepRole_STEP_ROLE_SETUP {
		t.Errorf("recorded run = %+v", run)
	}
	if h.e.mgr.emitted() == 0 {
		t.Error("the guard's record is published")
	}
	// PreToolUse is a turn start too.
	h.typer.onType = func(string) { h.event(agent.HookEvent{Event: "PreToolUse"}) }
	if resp := deliverStep(t, h, custom("next", 2)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN || resp.GetSubmitAt() != nil {
		t.Errorf("PreToolUse = %+v", resp)
	}
}

func TestStepInputOnlyOnNeedsInput(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) {
		h.event(agent.HookEvent{Event: "Notification", NotificationType: "permission_prompt", NeedsInput: true})
	}
	if resp := deliverStep(t, h, custom("go", 1)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN_INPUT_ONLY {
		t.Errorf("needs-input = %+v", resp)
	}
	// With a turn start in the same batch it is a model turn.
	h.typer.onType = func(string) {
		h.event(agent.HookEvent{Event: "Notification", NeedsInput: true})
		h.event(agent.HookEvent{Event: "UserPromptSubmit"})
	}
	if resp := deliverStep(t, h, custom("go", 2)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN {
		t.Errorf("needs-input + submit = %+v", resp)
	}
	// The idle nudge is no input request: the window ends agent-local.
	h.typer.onType = func(string) { h.event(agent.HookEvent{Event: "Notification", NotificationType: "idle_prompt"}) }
	if resp := deliverStep(t, h, custom("go", 3)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("idle nudge = %+v", resp)
	}
}

func TestStepSessionStartIsNotATurnStart(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) {
		h.event(agent.HookEvent{Event: "SessionStart", Source: "startup"})
		h.event(agent.HookEvent{Event: "SessionEnd"})
	}
	resp := deliverStep(t, h, custom("!git status", 1))
	if resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("SessionStart must not classify a turn: %+v", resp)
	}
	if h.clock.elapsed() < submitWindow {
		t.Errorf("agent-local only once the window ends (%s)", h.clock.elapsed())
	}
}

func TestStepReportFromBeforeTheEnterIsIgnored(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) {
		h.tap.ch <- agent.HookEvent{Event: "UserPromptSubmit", At: h.clock.Now().Add(-time.Second)}
	}
	if resp := deliverStep(t, h, custom("x", 1)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("a stale report must not count: %+v", resp)
	}
}

func TestStepPickerIsNotAcceptedWithoutEnter(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) { h.scr.set(fixture(t, "model-picker"), false, true) }
	resp := deliverStep(t, h, custom("/model", 1))
	if resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED || resp.GetNote() != "picker opened" {
		t.Fatalf("picker = %+v", resp)
	}
	if len(h.typer.presses) != 0 {
		t.Error("no Enter on a modal")
	}
	if e := h.clock.elapsed(); e < modalGrace || e > modalGrace+2*stepPoll {
		t.Errorf("decided after %s, want modalGrace", e)
	}
}

func TestStepSkillDialogWithinGraceIsInputOnly(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) {
		h.scr.set(fixture(t, "auto-mode-dialog"), false, true)
		h.clock.in(600*time.Millisecond, func() { h.event(agent.HookEvent{Event: "Notification", NeedsInput: true}) })
	}
	if resp := deliverStep(t, h, custom("/skill-x", 1)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN_INPUT_ONLY {
		t.Errorf("dialog + needs-input = %+v", resp)
	}
}

func TestStepModalThatClosesResetsTheGrace(t *testing.T) {
	h := newStepHarness(t)
	empty := h.scr.lines
	h.typer.onType = func(string) {
		h.scr.set(fixture(t, "model-picker"), false, true)
		h.clock.in(500*time.Millisecond, func() { h.scr.set(empty, false, true) })
	}
	if resp := deliverStep(t, h, custom("/model", 1)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("a dismissed picker then an empty box = %+v", resp)
	}
}

func TestStepTextInBoxRePressedThenNotAccepted(t *testing.T) {
	for _, c := range []struct {
		name, fix string
		req       *pb.DeliverWorkerStepRequest
	}{
		{"wrapped text", "text-left-in-box", custom(textLeft, 1)},
		{"framed paste", "framed-paste-in-box", stepReq(pb.StepKind_STEP_KIND_ACTION_PROMPT, "Review pull request 412.", "", 1)},
	} {
		h := newStepHarness(t)
		h.typer.onType = func(string) { h.scr.set(fixture(t, c.fix), false, true) }
		resp := deliverStep(t, h, c.req)
		if resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED || resp.GetNote() != "text still in the input box" {
			t.Errorf("%s: %+v", c.name, resp)
		}
		if got := h.typer.presses; len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
			t.Errorf("%s: re-presses %v, want 0,1,2", c.name, got)
		}
		if h.typer.count() != 1 {
			t.Errorf("%s: typed %d times", c.name, h.typer.count())
		}
	}
}

func TestStepRePressSubmits(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) { h.scr.set(fixture(t, "text-left-in-box"), false, true) }
	h.typer.onPress = func(int) { h.event(agent.HookEvent{Event: "UserPromptSubmit"}) }
	if resp := deliverStep(t, h, custom(textLeft, 1)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN || len(h.typer.presses) != 1 {
		t.Errorf("re-press then submit = %+v presses %v", resp, h.typer.presses)
	}
}

func TestStepActionPromptIsFramed(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) { h.event(agent.HookEvent{Event: "UserPromptSubmit"}) }
	deliverStep(t, h, stepReq(pb.StepKind_STEP_KIND_ACTION_PROMPT, "Review it.", "", 1))
	if want := Frame("review-bot", "run-1", "review-pr", "Review it."); h.typer.typed[0] != want {
		t.Errorf("typed %q, want the framed prompt", h.typer.typed[0])
	}
	if h.run(t).GetStepRole() != pb.StepRole_STEP_ROLE_ACTION {
		t.Error("step_role follows the step")
	}
}

func TestStepBuiltinTextIsDerived(t *testing.T) {
	h := newStepHarness(t)
	for i, c := range []struct {
		kind         pb.StepKind
		text, expect string
		want         string
	}{
		{pb.StepKind_STEP_KIND_MODEL, "!rm -rf /", "Opus", "/model opus"},
		{pb.StepKind_STEP_KIND_EFFORT, "/anything", " Medium ", "/effort medium"},
		{pb.StepKind_STEP_KIND_CLEAR, "/model haiku", "", "/clear"},
	} {
		_, _ = h.e.svc.DeliverStep(bg, stepReq(c.kind, c.text, c.expect, int32(i+1)))
		if got := h.typer.typed[i]; got != c.want {
			t.Errorf("%v typed %q, want %q", c.kind, got, c.want)
		}
	}
	// An expect outside the fixed sets: INVALID_ARGUMENT, nothing recorded or typed.
	n, idx := h.typer.count(), h.run(t).GetStepIndex()
	for _, req := range []*pb.DeliverWorkerStepRequest{
		stepReq(pb.StepKind_STEP_KIND_MODEL, "", "gpt", 9),
		stepReq(pb.StepKind_STEP_KIND_EFFORT, "", "ultracode", 9),
		stepReq(pb.StepKind_STEP_KIND_UNSPECIFIED, "x", "", 9),
		custom("  ", 9),
	} {
		if _, err := h.e.svc.DeliverStep(bg, req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v %q = %v, want ErrInvalid", req.GetStep().GetKind(), req.GetStep().GetExpect(), err)
		}
	}
	if h.typer.count() != n || h.run(t).GetStepIndex() != idx {
		t.Error("an invalid step records and types nothing")
	}
}

func TestStepReadBack(t *testing.T) {
	empty := blankBox(t)
	for _, c := range []struct {
		name          string
		kind          pb.StepKind
		expect        string
		before, after []termview.Line
		outcome       pb.StepOutcome
		note          string
	}{
		{"model set", pb.StepKind_STEP_KIND_MODEL, "opus", empty, fixture(t, "model-set-opus"), pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL, ""},
		{"older identical echo", pb.StepKind_STEP_KIND_MODEL, "opus", fixture(t, "model-set-opus"), fixture(t, "model-set-opus"), pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED, "no confirmation"},
		{"model not found", pb.StepKind_STEP_KIND_MODEL, "fable", empty,
			withRows(empty, "❯ /model fable", "  ⎿  Model 'fable' not found"), pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED, "Model 'fable' not found"},
		{"echo with no result", pb.StepKind_STEP_KIND_MODEL, "haiku", empty, withRows(empty, "❯ /model haiku"), pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED, "no confirmation"},
		{"effort set", pb.StepKind_STEP_KIND_EFFORT, "medium", empty, fixture(t, "effort-set-medium"), pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL, ""},
		{"effort auto", pb.StepKind_STEP_KIND_EFFORT, "auto", empty, fixture(t, "effort-auto"), pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL, ""},
		{"effort wrong level", pb.StepKind_STEP_KIND_EFFORT, "high", empty,
			withRows(empty, "❯ /effort high", "  ⎿  Set effort level to low"), pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED, "Set effort level to low"},
	} {
		h := newStepHarness(t)
		h.scr.set(c.before, false, true)
		h.typer.onType = func(string) { h.scr.set(c.after, false, true) }
		resp := deliverStep(t, h, stepReq(c.kind, "", c.expect, 1))
		if resp.GetOutcome() != c.outcome || resp.GetNote() != c.note {
			t.Errorf("%s: %v %q, want %v %q", c.name, resp.GetOutcome(), resp.GetNote(), c.outcome, c.note)
		}
	}
}

func TestStepClearReadBack(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) {
		h.scr.set(fixture(t, "clear-banner"), false, true)
		h.event(agent.HookEvent{Event: "SessionStart", Source: "clear", SessionID: "new"})
	}
	clear := stepReq(pb.StepKind_STEP_KIND_CLEAR, "", "", 1)
	clear.Step.Role = pb.StepRole_STEP_ROLE_CLEANUP
	if resp := deliverStep(t, h, clear); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("clear with SessionStart clear = %+v", resp)
	}
	h.typer.onType = func(string) { h.event(agent.HookEvent{Event: "SessionStart", Source: "startup"}) }
	clear.StepIndex, clear.First = 2, false
	if resp := deliverStep(t, h, clear); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED || resp.GetNote() != "no new session after /clear" {
		t.Errorf("clear without SessionStart clear = %+v", resp)
	}
	// Research R13: a forged clear naming the session recorded before typing,
	// or one with the conversation still on screen, does not pass.
	h.mutate(t, func(x *pb.Sandbox) { x.Agent.SessionId = "s-old" })
	h.typer.onType = func(string) {
		h.scr.set(fixture(t, "clear-banner"), false, true)
		h.event(agent.HookEvent{Event: "SessionStart", Source: "clear", SessionID: "s-old"})
	}
	clear.StepIndex = 3
	if resp := deliverStep(t, h, clear); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED {
		t.Errorf("clear with the old session id = %+v", resp)
	}
	h.typer.onType = func(string) {
		h.scr.set(fixture(t, "empty-after-command"), false, true)
		h.event(agent.HookEvent{Event: "SessionStart", Source: "clear", SessionID: "s-new"})
	}
	clear.StepIndex = 4
	if resp := deliverStep(t, h, clear); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED {
		t.Errorf("clear with the conversation on screen = %+v", resp)
	}
}

func TestStepWindowEndOutcomes(t *testing.T) {
	// The screen goes away: not at an empty box.
	h := newStepHarness(t)
	h.typer.onType = func(string) { h.scr.set(nil, false, false) }
	if resp := deliverStep(t, h, custom("x", 1)); resp.GetNote() != "not at an empty input box" {
		t.Errorf("no screen = %+v", resp)
	}
	// The status moved with no turn-start report.
	h = newStepHarness(t)
	h.typer.onType = func(string) {
		h.mutate(t, func(x *pb.Sandbox) { x.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING })
	}
	if resp := deliverStep(t, h, custom("x", 1)); resp.GetNote() != "not at an empty input box" {
		t.Errorf("status changed = %+v", resp)
	}
	// Developer typing replaced the step: box not empty, not the step.
	h = newStepHarness(t)
	h.typer.onType = func(string) { h.scr.set(fixture(t, "text-left-in-box"), false, true) }
	if resp := deliverStep(t, h, custom("something else", 1)); resp.GetNote() != "not at an empty input box" || len(h.typer.presses) != 0 {
		t.Errorf("foreign text = %+v presses %v", resp, h.typer.presses)
	}
}

func TestStepStaleScreenIsUnconfirmed(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) { h.scr.set(h.scr.lines, true, true) }
	resp := deliverStep(t, h, custom("x", 1))
	if resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED || resp.GetNote() != "screen resyncing" || h.scr.repaints == 0 {
		t.Errorf("stale = %+v (repaints %d)", resp, h.scr.repaints)
	}
	if e := h.clock.elapsed(); e > submitWindow+stepExtension+2*stepPoll {
		t.Errorf("stale extension too long: %s", e)
	}
	// A stale screen that recovers within the extension decides normally.
	h = newStepHarness(t)
	h.typer.onType = func(string) {
		h.scr.set(h.scr.lines, true, true)
		h.clock.in(submitWindow+300*time.Millisecond, func() { h.scr.set(h.scr.lines, false, true) })
	}
	if resp := deliverStep(t, h, custom("x", 1)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("recovered = %+v", resp)
	}
}

func TestStepTimingBound(t *testing.T) {
	h := newStepHarness(t)
	// The worst case: the text stays, each re-press extends the window, then a
	// picker, then a stale screen.
	h.typer.onType = func(string) { h.scr.set(fixture(t, "text-left-in-box"), false, true) }
	deliverStep(t, h, custom(textLeft, 1))
	bound := submitWindow*time.Duration(1+submitRePresses) + modalGrace + stepExtension
	if e := h.clock.elapsed(); e > bound || bound > 13*time.Second+500*time.Millisecond {
		t.Errorf("check took %s; bound %s must stay under 13.5 s", e, bound)
	}
}

// --- §1: the guard ---

func TestStepGuardRefusalsTypeNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*stepHarness)
		req  func() *pb.DeliverWorkerStepRequest
		want string
	}{
		{"stopped", func(h *stepHarness) {
			h.mutate(t, func(x *pb.Sandbox) { x.State = pb.SandboxState_SANDBOX_STATE_STOPPED })
		}, nil, "sandbox is stopped"},
		{"other orchestrator", nil, func() *pb.DeliverWorkerStepRequest {
			r := custom("x", 1)
			r.OrchestratorId = "orch-2"
			return r
		}, "not this orchestrator's worker"},
		{"another run on first", func(h *stepHarness) {
			h.mutate(t, func(x *pb.Sandbox) { x.Controller.Run = &pb.WorkerRun{Id: "run-0"} })
		}, nil, "run run-0 is already on this sandbox"},
		{"run moved", nil, func() *pb.DeliverWorkerStepRequest { return custom("x", 2) }, "run moved"},
		{"restart", nil, func() *pb.DeliverWorkerStepRequest { r := custom("x", 1); r.Restart = true; return r }, "not a restart halt"},
		{"agent working", func(h *stepHarness) {
			h.mutate(t, func(x *pb.Sandbox) { x.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING })
		}, nil, "agent is working"},
		{"agent waiting", func(h *stepHarness) {
			h.mutate(t, func(x *pb.Sandbox) { x.Agent.Status = pb.AgentStatus_AGENT_STATUS_WAITING })
		}, nil, "agent is waiting"},
		{"box not empty", func(h *stepHarness) { h.scr.set(fixture(t, "text-left-in-box"), false, true) }, nil, "input box not empty"},
		{"picker", func(h *stepHarness) { h.scr.set(fixture(t, "model-picker"), false, true) }, nil, "not at an input box"},
		{"no screen", func(h *stepHarness) { h.scr.set(nil, false, false) }, nil, "not at an input box"},
		{"stale", func(h *stepHarness) { h.scr.set(h.scr.lines, true, true) }, nil, "screen resyncing"},
	} {
		h := newStepHarness(t)
		if c.mut != nil {
			c.mut(h)
		}
		req := custom("x", 1)
		if c.req != nil {
			req = c.req()
		}
		before := h.run(t)
		_, err := h.e.svc.DeliverStep(bg, req)
		if !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if h.typer.count() != 0 || h.tap.opened != 0 {
			t.Errorf("%s: a refusal types nothing", c.name)
		}
		if after := h.run(t); after.GetStepSentAt() != nil || (before == nil) != (after == nil) {
			t.Errorf("%s: a refusal records nothing: %+v", c.name, after)
		}
		if c.name == "stale" && h.scr.repaints == 0 {
			t.Error("a stale screen asks for a repaint")
		}
	}
	h := newStepHarness(t)
	if _, err := h.e.svc.DeliverStep(bg, &pb.DeliverWorkerStepRequest{SandboxId: "nope", Step: &pb.RunStep{Kind: pb.StepKind_STEP_KIND_CLEAR}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown sandbox = %v", err)
	}
	h.e.svc.agents = nil
	if _, err := h.e.svc.DeliverStep(bg, custom("x", 1)); !errors.Is(err, ErrPrecondition) {
		t.Errorf("no agent registry = %v", err)
	}
}

func TestStepNotReadyAgent(t *testing.T) {
	h := newStepHarness(t)
	h.e.svc.agents.Close(h.id)
	if _, err := h.e.svc.agents.Session(h.id, &pb.AgentSpec{}); err != nil { // live, never reported ready
		t.Fatal(err)
	}
	if _, err := h.e.svc.DeliverStep(bg, custom("x", 1)); err == nil || !strings.Contains(err.Error(), "agent is starting") {
		t.Errorf("unready = %v", err)
	}
	if h.typer.count() != 0 {
		t.Error("nothing typed into an unready agent")
	}
}

func TestStepNoLiveAgent(t *testing.T) {
	h := newStepHarness(t)
	h.e.svc.agents.Close(h.id)
	base := *h.created
	// A later step, a cleanup step or a held sandbox: nothing is started.
	h.mutate(t, func(x *pb.Sandbox) {
		x.Controller.Run = &pb.WorkerRun{Id: "run-1", StepIndex: 1, StepSentAt: timestamppb.Now()}
	})
	if _, err := h.e.svc.DeliverStep(bg, custom("x", 2)); err == nil || !strings.Contains(err.Error(), "no live agent") {
		t.Errorf("later step = %v", err)
	}
	held := stepReq(pb.StepKind_STEP_KIND_CLEAR, "", "", 1)
	held.Retry = true
	h.mutate(t, func(x *pb.Sandbox) { x.Controller.Run.Hold = pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER })
	if _, err := h.e.svc.DeliverStep(bg, held); err == nil || !strings.Contains(err.Error(), "no live agent") {
		t.Errorf("held first = %v", err)
	}
	if *h.created != base || h.e.svc.agents.Live(h.id) || h.scr.ensured != 0 {
		t.Fatalf("the factory must never be called (created %d→%d), nor a screen ensured", base, *h.created)
	}
	// The first step of an unheld sandbox starts one, and is refused meanwhile.
	h.mutate(t, func(x *pb.Sandbox) { x.Controller.Run = nil })
	if _, err := h.e.svc.DeliverStep(bg, custom("x", 1)); err == nil || !strings.Contains(err.Error(), "agent is starting") {
		t.Errorf("first = %v", err)
	}
	if *h.created != base+1 || h.typer.count() != 0 || h.run(t) != nil {
		t.Errorf("first must start one agent and record nothing (created %d)", *h.created-base)
	}
}

func TestStepSendNumbers(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) { h.event(agent.HookEvent{Event: "UserPromptSubmit"}) }
	deliverStep(t, h, custom("a", 1))
	for _, idx := range []int32{1, 0} {
		if _, err := h.e.svc.DeliverStep(bg, custom("a", idx)); !errors.Is(err, ErrStepSent) {
			t.Errorf("index %d = %v, want ErrStepSent", idx, err)
		}
	}
	// A gap after a skipped step is fine.
	deliverStep(t, h, custom("c", 3))
	if h.typer.count() != 2 || h.run(t).GetStepIndex() != 3 {
		t.Errorf("typed %d, index %d", h.typer.count(), h.run(t).GetStepIndex())
	}
	// A retry needs a halted run; on one it types once, and a second is refused.
	retry := custom("c", 3)
	retry.Retry = true
	if _, err := h.e.svc.DeliverStep(bg, retry); !errors.Is(err, ErrStepSent) {
		t.Errorf("retry on a running step = %v", err)
	}
	h.mutate(t, func(x *pb.Sandbox) { x.Controller.Run.Halted = true })
	deliverStep(t, h, retry)
	if h.typer.count() != 3 || h.run(t).GetHalted() {
		t.Errorf("retry typed %d, halted %v", h.typer.count(), h.run(t).GetHalted())
	}
	if _, err := h.e.svc.DeliverStep(bg, retry); !errors.Is(err, ErrStepSent) || h.typer.count() != 3 {
		t.Errorf("second retry = %v", err)
	}
}

func TestStepRefusedRetryStaysRetryable(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) { h.event(agent.HookEvent{Event: "UserPromptSubmit"}) }
	deliverStep(t, h, custom("a", 1))
	h.mutate(t, func(x *pb.Sandbox) { x.Controller.Run.Halted = true })
	retry := custom("a", 1)
	retry.Retry = true
	h.scr.set(fixture(t, "text-left-in-box"), false, true)
	if _, err := h.e.svc.DeliverStep(bg, retry); err == nil || !strings.Contains(err.Error(), "input box not empty") {
		t.Fatalf("check 6 = %v", err)
	}
	if !h.run(t).GetHalted() || h.typer.count() != 1 {
		t.Fatal("a refused retry stays halted and types nothing")
	}
	h.scr.set(blankBox(t), false, true)
	deliverStep(t, h, retry)
	if h.typer.count() != 2 {
		t.Errorf("the retry types exactly once: %d", h.typer.count())
	}
}

func TestStepUnderNeedsDeveloper(t *testing.T) {
	h := newStepHarness(t)
	h.mutate(t, func(x *pb.Sandbox) {
		x.Controller.Run = &pb.WorkerRun{Id: "run-1", StepIndex: 5, StepSentAt: timestamppb.Now(), StepRole: pb.StepRole_STEP_ROLE_CLEANUP,
			Hold: pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER}
	})
	post := custom("/tidy", 6)
	post.Step.Role = pb.StepRole_STEP_ROLE_CLEANUP
	if _, err := h.e.svc.DeliverStep(bg, post); err == nil || !strings.Contains(err.Error(), "cleanup dropped") {
		t.Errorf("custom cleanup under the hold = %v", err)
	}
	clear := stepReq(pb.StepKind_STEP_KIND_CLEAR, "", "", 5)
	clear.Step.Role, clear.First = pb.StepRole_STEP_ROLE_CLEANUP, false
	if _, err := h.e.svc.DeliverStep(bg, clear); err == nil || !strings.Contains(err.Error(), "cleanup dropped") {
		t.Errorf("an unretried Clear under the hold = %v", err)
	}
	clear.Retry = true
	h.typer.onType = func(string) {
		h.scr.set(fixture(t, "clear-banner"), false, true)
		h.event(agent.HookEvent{Event: "SessionStart", Source: "clear", SessionID: "after-clear"})
	}
	if resp := deliverStep(t, h, clear); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("retried Clear = %+v", resp)
	}
	if h.typer.count() != 1 || h.typer.typed[0] != "/clear" {
		t.Errorf("typed %q", h.typer.typed)
	}
}

func TestStepRetriedFirst(t *testing.T) {
	h := newStepHarness(t)
	h.mutate(t, func(x *pb.Sandbox) { x.Controller.Run = &pb.WorkerRun{Id: "run-1", Action: "review-pr"} })
	h.typer.onType = func(string) { h.event(agent.HookEvent{Event: "UserPromptSubmit"}) }
	if resp := deliverStep(t, h, custom("a", 1)); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN {
		t.Errorf("retried first for the same run = %+v", resp)
	}
	other := custom("a", 1)
	other.RunId = "run-2"
	if _, err := h.e.svc.DeliverStep(bg, other); err == nil || !strings.Contains(err.Error(), "run run-1 is already on this sandbox") {
		t.Errorf("first for another run = %v", err)
	}
}

func TestStepRollbackOnTypeError(t *testing.T) {
	h := newStepHarness(t)
	h.typer.err = errors.New("pty gone")
	if _, err := h.e.svc.DeliverStep(bg, custom("a", 1)); err == nil || !strings.Contains(err.Error(), "pty gone") {
		t.Fatalf("type error = %v", err)
	}
	if h.run(t) != nil {
		t.Error("a first step's run is rolled back")
	}
	sent := timestamppb.New(time.Unix(42, 0))
	h.mutate(t, func(x *pb.Sandbox) {
		x.Controller.Run = &pb.WorkerRun{Id: "run-1", StepIndex: 2, StepSentAt: sent, StepRole: pb.StepRole_STEP_ROLE_SETUP}
	})
	if _, err := h.e.svc.DeliverStep(bg, stepReq(pb.StepKind_STEP_KIND_ACTION_PROMPT, "go", "", 3)); err == nil {
		t.Fatal("type error expected")
	}
	if r := h.run(t); r.GetId() != "run-1" || r.GetStepIndex() != 2 || r.GetStepSentAt().AsTime() != sent.AsTime() || r.GetStepRole() != pb.StepRole_STEP_ROLE_SETUP {
		t.Errorf("a later step's send is rolled back: %+v", r)
	}
}

// --- §4: check_only ---

func checkOnly(kind pb.StepKind, expect string) *pb.DeliverWorkerStepRequest {
	r := stepReq(kind, "", expect, 1)
	r.CheckOnly, r.First = true, false
	return r
}

func TestStepCheckOnly(t *testing.T) {
	h := newStepHarness(t)
	sent := time.Unix(1_700_000_000, 0)
	h.mutate(t, func(x *pb.Sandbox) {
		x.Controller.Run = &pb.WorkerRun{Id: "run-1", StepIndex: 1, StepSentAt: timestamppb.New(sent)}
	})
	h.scr.set(fixture(t, "model-set-opus"), false, true)
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_MODEL, "opus")); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL || resp.GetSentAt() != nil {
		t.Errorf("model read-back pass = %+v", resp)
	}
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_EFFORT, "high")); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED {
		t.Errorf("effort with no echo = %+v", resp)
	}
	for _, k := range []pb.StepKind{pb.StepKind_STEP_KIND_CUSTOM, pb.StepKind_STEP_KIND_ACTION_PROMPT} {
		r := checkOnly(k, "")
		r.Step.Text = "x"
		if resp := deliverStep(t, h, r); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED {
			t.Errorf("%v check_only = %+v", k, resp)
		}
	}
	// Clear: by the recorded evidence.
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_CLEAR, "")); resp.GetNote() != "no new session after /clear" {
		t.Errorf("clear without evidence = %+v", resp)
	}
	h.mutate(t, func(x *pb.Sandbox) {
		x.Agent.LastSessionSource, x.Agent.LastSessionStartAt = "clear", timestamppb.New(sent.Add(time.Second))
	})
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_CLEAR, "")); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED {
		t.Errorf("clear with evidence but the conversation on screen = %+v", resp)
	}
	h.scr.set(fixture(t, "clear-banner"), false, true)
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_CLEAR, "")); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("clear with evidence = %+v", resp)
	}
	h.scr.set(fixture(t, "clear-banner"), true, true)
	h.scr.freshAfter = 0
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_CLEAR, "")); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED {
		t.Errorf("clear on a stale screen = %+v", resp)
	}
	// Stale: the lazy repaint first; fresh in time decides, still stale is unconfirmed.
	h.scr.set(fixture(t, "model-set-opus"), true, true)
	h.scr.freshAfter = h.scr.repaints + 1
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_MODEL, "opus")); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Errorf("stale then fresh = %+v", resp)
	}
	h.scr.set(fixture(t, "model-set-opus"), true, true)
	h.scr.freshAfter = 0
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_MODEL, "opus")); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED {
		t.Errorf("still stale = %+v", resp)
	}
	h.scr.set(nil, false, false)
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_MODEL, "opus")); resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED {
		t.Errorf("no screen = %+v", resp)
	}
	if h.typer.count() != 0 {
		t.Error("check_only types nothing")
	}
	// No live agent: nothing is created.
	h.e.svc.agents.Close(h.id)
	base := *h.created
	if resp := deliverStep(t, h, checkOnly(pb.StepKind_STEP_KIND_MODEL, "opus")); resp.GetNote() != "no live agent" || *h.created != base {
		t.Errorf("no live agent = %+v", resp)
	}
	// Guard checks 1–2 only.
	other := checkOnly(pb.StepKind_STEP_KIND_MODEL, "opus")
	other.OrchestratorId = "orch-2"
	if _, err := h.e.svc.DeliverStep(bg, other); !errors.Is(err, ErrPrecondition) {
		t.Errorf("foreign check_only = %v", err)
	}
	h.mutate(t, func(x *pb.Sandbox) { x.State = pb.SandboxState_SANDBOX_STATE_STOPPED })
	if _, err := h.e.svc.DeliverStep(bg, checkOnly(pb.StepKind_STEP_KIND_MODEL, "opus")); !errors.Is(err, ErrPrecondition) {
		t.Errorf("stopped check_only = %v", err)
	}
	missing := checkOnly(pb.StepKind_STEP_KIND_MODEL, "opus")
	missing.SandboxId = "nope"
	if _, err := h.e.svc.DeliverStep(bg, missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown sandbox = %v", err)
	}
}

// --- the production wiring: terminal registry screen and agent registry typing ---

func TestStepOverTheRealRegistries(t *testing.T) {
	e := newEnv(t, "")
	sess := newRecSession()
	created := 0
	agents := agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) { created++; return sess, nil })
	e.svc.agents = agents
	terms := terminal.NewRegistry(func(id string) (terminal.PTY, error) { return agents.Session(id, &pb.AgentSpec{}) }, 0, nil)
	terms.SetExisting(func(id string) (terminal.PTY, error) { return agents.Existing(id) })
	terms.SetScreenFor(func(string) bool { return true })
	e.svc.SetTerminals(terms)
	e.svc.timing = stepTiming{window: 20 * time.Millisecond, rePresses: 1, modalGrace: 10 * time.Millisecond, extension: 10 * time.Millisecond, poll: 5 * time.Millisecond}
	sb := e.sandbox(t, stepSandbox, "w", pb.SandboxState_SANDBOX_STATE_RUNNING)

	// No live agent: EnsureScreen creates nothing.
	if err := (termScreen{terms}).EnsureScreen(sb.GetId()); err == nil {
		t.Error("no live agent: no broadcaster")
	}
	attachReady(t, e, sb.GetId())
	// The screen exists now but holds no box until the agent draws one.
	if _, err := e.svc.DeliverStep(bg, custom("x", 1)); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("blank screen = %v", err)
	}
	// Draw an empty box through the PTY's read side is not possible with this
	// session; serve the screen from a fake instead and keep the real typer.
	scr := &fakeScreen{lines: blankBox(t), ok: true}
	e.svc.screen = scr
	resp, err := e.svc.DeliverStep(bg, custom("hello", 1))
	if err != nil || resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL {
		t.Fatalf("real typer = %+v / %v", resp, err)
	}
	if got := sess.typed(); !strings.Contains(got, agent.Typed("hello")) {
		t.Errorf("typed %q", got)
	}
	if created != 1 {
		t.Errorf("one agent only: %d", created)
	}
	// EnsureScreen failures are logged, not fatal.
	scr.ensureErr = errors.New("boom")
	if _, err := e.svc.DeliverStep(bg, custom("again", 2)); err != nil {
		t.Errorf("ensure error = %v", err)
	}
	// With neither a screen nor terminals, the guard sees no box.
	e.svc.screen, e.svc.terms = nil, nil
	if _, err := e.svc.DeliverStep(bg, custom("x", 3)); err == nil || !strings.Contains(err.Error(), "not at an input box") {
		t.Errorf("no screen source = %v", err)
	}
	if (realClock{}).Now().IsZero() {
		t.Error("real clock")
	}
}

// Local serves the step in-process.
func TestLocalDeliverWorkerStep(t *testing.T) {
	h := newStepHarness(t)
	h.typer.onType = func(string) { h.event(agent.HookEvent{Event: "UserPromptSubmit"}) }
	resp, err := Local(h.e.svc, agent.NewHub("h")).DeliverWorkerStep(bg, custom("a", 1))
	if err != nil || resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN {
		t.Errorf("local = %+v / %v", resp, err)
	}
	if _, err := Local(h.e.svc, agent.NewHub("h")).DeliverWorkerStep(bg, custom("a", 1)); !errors.Is(err, ErrStepSent) {
		t.Errorf("local repeat = %v", err)
	}
}

// stepClient is a SwitchboardClient answering DeliverWorkerStep with err.
type stepClient struct {
	pb.SwitchboardClient
	err error
}

func (c stepClient) DeliverWorkerStep(context.Context, *pb.DeliverWorkerStepRequest, ...grpc.CallOption) (*pb.DeliverWorkerStepResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_MODEL_TURN}, nil
}

func TestRemoteDeliverWorkerStepMapsCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		want error
		code codes.Code
	}{
		{status.Error(codes.AlreadyExists, "step already sent"), ErrStepSent, codes.Unknown},
		{status.Error(codes.Unimplemented, "unknown method"), ErrUnsupported, codes.Unknown},
		{status.Error(codes.FailedPrecondition, "agent is working"), nil, codes.FailedPrecondition},
	} {
		_, err := Remote(stepClient{err: c.err}).DeliverWorkerStep(bg, &pb.DeliverWorkerStepRequest{})
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%v → %v, want %v", c.err, err, c.want)
		}
		if c.want == nil && status.Code(err) != c.code {
			t.Errorf("%v → %v, want it passed through", c.err, err)
		}
	}
	if resp, err := Remote(stepClient{}).DeliverWorkerStep(bg, &pb.DeliverWorkerStepRequest{}); err != nil || resp.GetOutcome() != pb.StepOutcome_STEP_OUTCOME_MODEL_TURN {
		t.Errorf("ok = %+v / %v", resp, err)
	}
}
