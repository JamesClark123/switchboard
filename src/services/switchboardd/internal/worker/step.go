package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Step delivery (SWI-17, contracts/worker-step.md): DeliverWorkerStep types one
// step of a run's sequence into the worker's agent and runs its submit check
// here, next to the hooks and the screen, then returns what it saw. The guard
// commits nothing unless every check passes, so a refused step is never taken
// for a typed one; the recorded send number (step_index) only grows, so no
// step is typed twice. The orchestrator owns the sequence (research R3).

// StepTap opens a sandbox's hook tap (agent.HookServer.Tap).
type StepTap interface {
	Tap(sandboxID string) (<-chan agent.HookEvent, func())
}

// StepScreen is the agent screen as the step path reads it (terminal.Registry):
// it never starts an agent.
type StepScreen interface {
	// Screen returns the sandbox's screen, whether it is stale, and ok = false
	// when there is none. It creates nothing.
	Screen(sandboxID string) (lines []termview.Line, stale, ok bool)
	// EnsureScreen gives a live agent a broadcaster with a screen, creating the
	// broadcaster only over the live session (BroadcasterExisting).
	EnsureScreen(sandboxID string) error
	// RequestRepaint asks for the lazy repaint nudge of a stale screen.
	RequestRepaint(sandboxID string) bool
}

// StepTyper types into a sandbox's live agent (agent.Registry).
type StepTyper interface {
	TypeStep(sandboxID string, spec *pb.AgentSpec, text string) (time.Time, error)
	RePress(sandboxID string, n int) error
}

// StepClock is the submit check's clock.
type StepClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// stepTiming holds the submit check's bounds (constants.go); tests shorten them.
type stepTiming struct {
	window     time.Duration
	rePresses  int
	modalGrace time.Duration
	extension  time.Duration
	poll       time.Duration
}

var defaultStepTiming = stepTiming{
	window: submitWindow, rePresses: submitRePresses, modalGrace: modalGrace, extension: stepExtension, poll: stepPoll,
}

// termScreen adapts the terminal registry to StepScreen.
type termScreen struct{ t *terminal.Registry }

func (a termScreen) Screen(id string) ([]termview.Line, bool, bool) { return a.t.Screen(id) }
func (a termScreen) RequestRepaint(id string) bool                  { return a.t.RequestRepaint(id) }
func (a termScreen) EnsureScreen(id string) error {
	if _, err := a.t.BroadcasterExisting(id); err != nil {
		return err
	}
	a.t.EnableScreen(id)
	return nil
}

// SetHookTap wires the hook tap the submit check listens on.
func (s *Service) SetHookTap(t StepTap) { s.tap = t }

func (s *Service) stepScreen() StepScreen {
	if s.screen != nil {
		return s.screen
	}
	if s.terms != nil {
		return termScreen{s.terms}
	}
	return nil
}

func (s *Service) stepTyper() StepTyper {
	if s.typer != nil {
		return s.typer
	}
	return s.agents
}

// The fixed sets a built-in's expect must be in (research R13).
var (
	modelFamilies = map[string]bool{"opus": true, "fable": true, "sonnet": true, "haiku": true}
	effortLevels  = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true, "auto": true}
)

// builtinText derives what a step types (contracts/worker-step.md §2): for
// MODEL, EFFORT and CLEAR the command from kind + expect, never step.text;
// for CUSTOM the text as is. ACTION_PROMPT is framed once the guard knows the
// orchestrator's name. INVALID_ARGUMENT for an expect outside the fixed sets.
func builtinText(step *pb.RunStep) (string, error) {
	expect := strings.ToLower(strings.TrimSpace(step.GetExpect()))
	switch step.GetKind() {
	case pb.StepKind_STEP_KIND_MODEL:
		if !modelFamilies[expect] {
			return "", fmt.Errorf("%w: model %q is not one of opus, fable, sonnet, haiku", ErrInvalid, step.GetExpect())
		}
		return "/model " + expect, nil
	case pb.StepKind_STEP_KIND_EFFORT:
		if !effortLevels[expect] {
			return "", fmt.Errorf("%w: effort %q is not one of low, medium, high, xhigh, max, auto", ErrInvalid, step.GetExpect())
		}
		return "/effort " + expect, nil
	case pb.StepKind_STEP_KIND_CLEAR:
		return "/clear", nil
	case pb.StepKind_STEP_KIND_CUSTOM, pb.StepKind_STEP_KIND_ACTION_PROMPT:
		if strings.TrimSpace(step.GetText()) == "" {
			return "", fmt.Errorf("%w: the step has no text", ErrInvalid)
		}
		return step.GetText(), nil
	}
	return "", fmt.Errorf("%w: unknown step kind %s", ErrInvalid, step.GetKind())
}

// readSpec is the expected read-back of a built-in.
type readSpec struct {
	kind   pb.StepKind
	text   string // what was typed (the echo to look for)
	expect string // MODEL: family ("opus"); EFFORT: level
	before int    // echoes of text on screen before typing
}

// stepCheck is one step's submit check.
type stepCheck struct {
	id          string
	read        readSpec
	sentAt      time.Time
	tap         <-chan agent.HookEvent
	priorSessID string // the session recorded before typing (Clear, T046)
}

// DeliverStep serves DeliverWorkerStep (contracts/worker-step.md).
func (s *Service) DeliverStep(ctx context.Context, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
	text, err := builtinText(req.GetStep())
	if err != nil {
		return nil, err
	}
	if s.agents == nil {
		return nil, fmt.Errorf("%w: agent registry not configured", ErrPrecondition)
	}
	if req.GetCheckOnly() {
		return s.checkOnly(ctx, req, text)
	}
	id := req.GetSandboxId()
	// 1. The screen, read without creating anything: only a live agent's
	// broadcaster is ensured (and given a screen).
	live, ready := s.agents.Live(id), s.agents.Ready(id)
	lines, stale, screenOK := s.readScreen(id, live)

	var (
		name, priorSess string
		recordedFirst   bool
		prev            *pb.WorkerRun
		startFirst      bool
	)
	kind := req.GetStep().GetKind()
	sb, err := s.reg.Update(id, func(x *pb.Sandbox) error {
		c := x.GetController()
		run := c.GetRun()
		switch {
		case x.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING:
			return fmt.Errorf("%w: sandbox is %s", ErrPrecondition, strings.ToLower(strings.TrimPrefix(x.GetState().String(), "SANDBOX_STATE_")))
		case c == nil || c.GetOrchestratorId() != req.GetOrchestratorId():
			return fmt.Errorf("%w: not this orchestrator's worker", ErrPrecondition)
		case req.GetRestart():
			// T047: R on a step halted by an owning-daemon restart reaps the
			// orphan and starts a fresh agent. No halt is recorded that way yet.
			return fmt.Errorf("%w: not a restart halt", ErrPrecondition)
		case req.GetFirst() && run != nil && run.GetId() != req.GetRunId():
			return fmt.Errorf("%w: run %s is already on this sandbox", ErrPrecondition, run.GetId())
		case !req.GetFirst() && (run == nil || run.GetId() != req.GetRunId()):
			return fmt.Errorf("%w: run moved", ErrPrecondition)
		}
		if err := checkSendNumber(run, req); err != nil {
			return err
		}
		switch {
		case !live:
			if req.GetFirst() && run.GetHold() == pb.WorkerHold_WORKER_HOLD_NONE {
				startFirst = true
				return fmt.Errorf("%w: agent is starting", ErrPrecondition)
			}
			return fmt.Errorf("%w: no live agent", ErrPrecondition)
		case !ready:
			return fmt.Errorf("%w: agent is starting", ErrPrecondition)
		case x.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE:
			// WAITING is not at a prompt for a step either.
			return fmt.Errorf("%w: agent is %s", ErrPrecondition, strings.ToLower(strings.TrimPrefix(x.GetAgent().GetStatus().String(), "AGENT_STATUS_")))
		}
		if err := checkInputBox(lines, stale, screenOK); err != nil {
			return err
		}
		// Every check passed: commit.
		if run == nil {
			run = &pb.WorkerRun{Id: req.GetRunId(), Action: req.GetAction()}
			c.Run = run
			recordedFirst = true
		} else {
			prev = proto.Clone(run).(*pb.WorkerRun)
		}
		run.StepRole = req.GetStep().GetRole()
		run.StepIndex = req.GetStepIndex()
		run.StepSentAt = timestamppb.New(s.now())
		run.Halted = false
		name, priorSess = c.GetName(), x.GetAgent().GetSessionId()
		x.UpdatedAt = timestamppb.New(s.now())
		return nil
	})
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return nil, fmt.Errorf("%w: sandbox %s", ErrNotFound, id)
		}
		if startFirst {
			s.startAgent(id, "step")
		}
		if stale {
			s.stepScreen().RequestRepaint(id)
		}
		return nil, err
	}
	s.mgr.Emit(sb)

	// 2. Type, with the tap already open so no report is missed.
	if kind == pb.StepKind_STEP_KIND_ACTION_PROMPT {
		text = Frame(name, req.GetRunId(), req.GetAction(), text)
	}
	tap, cancel := s.openTap(id)
	defer cancel()
	read := readSpec{kind: kind, text: text, expect: strings.ToLower(strings.TrimSpace(req.GetStep().GetExpect())), before: agent.Echoes(lines, text)}
	sentAt, err := s.stepTyper().TypeStep(id, sb.GetAgent().GetSpec(), text)
	if err != nil {
		s.rollbackStep(id, req.GetRunId(), req.GetStepIndex(), recordedFirst, prev)
		return nil, fmt.Errorf("deliver step to %s: %w", id, err)
	}
	// 3. The submit check.
	return s.submitCheck(stepCheck{id: id, read: read, sentAt: sentAt, tap: tap, priorSessID: priorSess}), nil
}

// readScreen reads the sandbox's screen for the guard: a live agent gets a
// broadcaster and a screen if it has none (a screen just enabled reads as no
// box until the agent repaints, and the orchestrator retries); nothing is
// created for a sandbox with no live agent.
func (s *Service) readScreen(id string, live bool) ([]termview.Line, bool, bool) {
	scr := s.stepScreen()
	if scr == nil {
		return nil, false, false
	}
	if live {
		if err := scr.EnsureScreen(id); err != nil {
			s.logf("[worker] step %s: agent screen: %v", id, err)
		}
	}
	return scr.Screen(id)
}

// checkSendNumber is guard 3a: a send number only grows (a skipped step leaves
// a gap), and an equal one is a retry, allowed only while the run is halted,
// or for Clear while the run is held for the developer. Under that hold every
// other step is dropped cleanup.
func checkSendNumber(run *pb.WorkerRun, req *pb.DeliverWorkerStepRequest) error {
	if run == nil {
		return nil
	}
	isClear := req.GetStep().GetKind() == pb.StepKind_STEP_KIND_CLEAR
	held := run.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
	if held && (!isClear || !req.GetRetry()) {
		// Not ALREADY_EXISTS: the step was never typed.
		return fmt.Errorf("%w: cleanup dropped", ErrPrecondition)
	}
	if run.GetStepSentAt() == nil {
		return nil // nothing typed for this run yet
	}
	idx, rec := req.GetStepIndex(), run.GetStepIndex()
	if idx > rec || (idx == rec && req.GetRetry() && (run.GetHalted() || (isClear && held))) {
		return nil
	}
	return fmt.Errorf("%w: step already sent", ErrStepSent)
}

// checkInputBox is guard 6: the input box is on screen, ready and empty.
func checkInputBox(lines []termview.Line, stale, ok bool) error {
	switch {
	case !ok:
		return fmt.Errorf("%w: not at an input box", ErrPrecondition)
	case stale:
		return fmt.Errorf("%w: screen resyncing", ErrPrecondition)
	}
	text, present, ready := agent.InputBox(lines)
	switch {
	case !present || !ready:
		return fmt.Errorf("%w: not at an input box", ErrPrecondition)
	case strings.TrimSpace(text) != "":
		return fmt.Errorf("%w: input box not empty", ErrPrecondition)
	}
	return nil
}

func (s *Service) openTap(id string) (<-chan agent.HookEvent, func()) {
	if s.tap == nil {
		return nil, func() {}
	}
	return s.tap.Tap(id)
}

// rollbackStep undoes the guard's record of a step that could not be typed:
// the run it recorded for a first step, otherwise its send. Only while the
// record still holds this send.
func (s *Service) rollbackStep(id, runID string, idx int32, recordedFirst bool, prev *pb.WorkerRun) {
	if _, err := s.reg.Update(id, func(x *pb.Sandbox) error {
		c := x.GetController()
		run := c.GetRun()
		if run.GetId() != runID || run.GetStepIndex() != idx {
			return nil
		}
		if recordedFirst {
			c.Run = nil
			return nil
		}
		run.StepRole, run.StepIndex, run.StepSentAt, run.Halted = prev.GetStepRole(), prev.GetStepIndex(), prev.GetStepSentAt(), prev.GetHalted()
		return nil
	}); err != nil {
		s.logf("[worker] step %s: rollback: %v", id, err)
	}
}

func notAccepted(note string, sentAt time.Time) *pb.DeliverWorkerStepResponse {
	r := &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED, Note: note}
	if !sentAt.IsZero() {
		r.SentAt = timestamppb.New(sentAt)
	}
	return r
}

// submitCheck watches a typed step until it is classified (contracts/
// worker-step.md §3): hook events from the tap as they arrive, and the screen
// every poll. It holds no registry lock while it waits.
func (s *Service) submitCheck(c stepCheck) *pb.DeliverWorkerStepResponse {
	t, clock, scr := s.timing, s.clock, s.stepScreen()
	deadline := c.sentAt.Add(t.window)
	extension := t.extension // one extension per check, shared (§6)
	presses := 0
	var (
		modalSince       time.Time
		turn, needsInput bool
		submitAt         time.Time
		clearEv          *agent.HookEvent
	)
	take := func(ev agent.HookEvent) {
		switch {
		case ev.At.Before(c.sentAt):
			// A report from before the Enter belongs to something else.
		case ev.Event == "UserPromptSubmit":
			turn = true
			if submitAt.IsZero() {
				submitAt = ev.At
			}
		case ev.Event == "PreToolUse":
			turn = true
		case ev.Event == "Notification" && ev.NeedsInput:
			needsInput = true
		case ev.Event == "SessionStart" && ev.Source == "clear" && clearEv == nil:
			e := ev
			clearEv = &e // evidence for Clear's read-back, never a turn start
		}
	}
	resp := func(o pb.StepOutcome) *pb.DeliverWorkerStepResponse {
		r := &pb.DeliverWorkerStepResponse{Outcome: o, SentAt: timestamppb.New(c.sentAt)}
		if !submitAt.IsZero() {
			r.SubmitAt = timestamppb.New(submitAt)
		}
		return r
	}
	for {
	drain:
		for {
			select {
			case ev := <-c.tap:
				take(ev)
			default:
				break drain
			}
		}
		switch {
		case turn:
			return resp(pb.StepOutcome_STEP_OUTCOME_MODEL_TURN)
		case needsInput:
			return resp(pb.StepOutcome_STEP_OUTCOME_MODEL_TURN_INPUT_ONLY)
		}
		now := clock.Now()
		var lines []termview.Line
		stale, ok := false, false
		if scr != nil {
			lines, stale, ok = scr.Screen(c.id)
		}
		if stale {
			scr.RequestRepaint(c.id)
		}
		switch {
		case ok && !stale && agent.ModalOpen(lines):
			// A picker, or a skill's own dialog whose needs-input report may
			// still be on its way: wait for it, pressing nothing (FR-206d).
			if modalSince.IsZero() {
				modalSince = now
			}
			if now.Sub(modalSince) >= t.modalGrace {
				return notAccepted("picker opened", c.sentAt)
			}
		case now.Before(deadline):
			modalSince = time.Time{}
		case stale:
			// Unknown, never a halt on its own (research R2).
			if extension <= 0 {
				r := resp(pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED)
				r.Note = "screen resyncing"
				return r
			}
			deadline, extension = now.Add(extension), 0
		case !ok:
			return notAccepted("not at an empty input box", c.sentAt)
		default:
			modalSince = time.Time{}
			text, present, ready := agent.InputBox(lines)
			switch {
			case present && agent.StepInBox(text, c.read.text):
				if presses >= t.rePresses {
					return notAccepted("text still in the input box", c.sentAt)
				}
				if err := s.stepTyper().RePress(c.id, presses); err != nil {
					return notAccepted("text still in the input box", c.sentAt)
				}
				s.logf("[worker] step %s: still in the input box; pressing Enter again (%d/%d)", c.id, presses+1, t.rePresses)
				presses++
				deadline = now.Add(t.window)
			case present && ready && strings.TrimSpace(text) == "" && s.agentIdle(c.id):
				if note, passed := s.readBack(c, lines, clearEv); !passed {
					return notAccepted(note, c.sentAt)
				}
				return resp(pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL)
			default:
				return notAccepted("not at an empty input box", c.sentAt)
			}
		}
		select {
		case ev := <-c.tap:
			take(ev)
		case <-clock.After(t.poll):
		}
	}
}

// agentIdle reports whether the sandbox's agent is recorded IDLE.
func (s *Service) agentIdle(id string) bool {
	sb, err := s.reg.Get(id)
	return err == nil && sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_IDLE
}

// readBack checks a step that ended agent-local (contracts/worker-step.md §3):
// Model and Effort by the confirmation under an echo of the command added
// since it was typed, Clear by a new session; a custom step or the action
// prompt needs nothing more. note is what was seen when it fails.
func (s *Service) readBack(c stepCheck, lines []termview.Line, clearEv *agent.HookEvent) (note string, ok bool) {
	switch c.read.kind {
	case pb.StepKind_STEP_KIND_MODEL, pb.StepKind_STEP_KIND_EFFORT:
		return confirmed(c.read, lines)
	case pb.StepKind_STEP_KIND_CLEAR:
		if clearEv == nil || !s.clearCorroborated(c, *clearEv) {
			return "no new session after /clear", false
		}
	}
	return "", true
}

// confirmed reads a Model or Effort confirmation under a new echo.
func confirmed(r readSpec, lines []termview.Line) (string, bool) {
	if agent.Echoes(lines, r.text) <= r.before {
		return "no confirmation", false
	}
	line, ok := agent.ConfirmationAfter(lines, r.text)
	if !ok {
		return "no confirmation", false
	}
	pass := false
	if r.kind == pb.StepKind_STEP_KIND_MODEL {
		pass = agent.ModelConfirmed(line, strings.ToUpper(r.expect[:1])+r.expect[1:])
	} else {
		pass = agent.EffortConfirmed(line, r.expect)
	}
	if !pass {
		return line, false
	}
	return "", true
}

// clearCorroborated reports whether a clear SessionStart passes Clear's
// read-back (research R13). T046 adds the corroboration: the current
// generation's token, a session_id other than c.priorSessID, and the
// post-clear screen polled for clearSettle (sharing the check's extension).
// Until then the main agent's clear SessionStart after the Enter counts.
func (s *Service) clearCorroborated(stepCheck, agent.HookEvent) bool { return true }

// checkOnly is the restart resolution's read-back (contracts/worker-step.md §4):
// guard checks 1–2 only, nothing recorded or typed, for MODEL, EFFORT and
// CLEAR against the current screen and recorded evidence. With no live agent
// it creates nothing. A screen still stale after the lazy repaint is
// UNCONFIRMED, never a not-accepted on a stale read alone (research R2).
func (s *Service) checkOnly(_ context.Context, req *pb.DeliverWorkerStepRequest, text string) (*pb.DeliverWorkerStepResponse, error) {
	id := req.GetSandboxId()
	sb, err := s.reg.Get(id)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return nil, fmt.Errorf("%w: sandbox %s", ErrNotFound, id)
		}
		return nil, err
	}
	switch c := sb.GetController(); {
	case sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING:
		return nil, fmt.Errorf("%w: sandbox is %s", ErrPrecondition, strings.ToLower(strings.TrimPrefix(sb.GetState().String(), "SANDBOX_STATE_")))
	case c == nil || c.GetOrchestratorId() != req.GetOrchestratorId():
		return nil, fmt.Errorf("%w: not this orchestrator's worker", ErrPrecondition)
	}
	kind := req.GetStep().GetKind()
	switch {
	case kind != pb.StepKind_STEP_KIND_MODEL && kind != pb.StepKind_STEP_KIND_EFFORT && kind != pb.StepKind_STEP_KIND_CLEAR:
		return notAccepted("an empty input box never finishes this step", time.Time{}), nil
	case !s.agents.Live(id):
		return notAccepted("no live agent", time.Time{}), nil
	case kind == pb.StepKind_STEP_KIND_CLEAR:
		a := sb.GetAgent()
		sent := sb.GetController().GetRun().GetStepSentAt()
		if a.GetLastSessionSource() == "clear" && sent != nil && a.GetLastSessionStartAt().AsTime().After(sent.AsTime()) {
			return &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL}, nil
		}
		return notAccepted("no new session after /clear", time.Time{}), nil
	}
	lines, stale, ok := s.readScreen(id, true)
	if stale {
		lines, stale, ok = s.awaitFresh(id)
	}
	if stale {
		return &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED, Note: "screen resyncing"}, nil
	}
	if !ok {
		return notAccepted("no confirmation", time.Time{}), nil
	}
	// The send's echo cannot be told from an older one here: any echo counts.
	read := readSpec{kind: kind, text: text, expect: strings.ToLower(strings.TrimSpace(req.GetStep().GetExpect()))}
	if note, passed := confirmed(read, lines); !passed {
		return notAccepted(note, time.Time{}), nil
	}
	return &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL}, nil
}

// awaitFresh requests the lazy repaint of a stale screen and polls for a fresh
// one for up to the check's extension.
func (s *Service) awaitFresh(id string) ([]termview.Line, bool, bool) {
	scr := s.stepScreen()
	scr.RequestRepaint(id)
	until := s.clock.Now().Add(s.timing.extension)
	for {
		<-s.clock.After(s.timing.poll)
		lines, stale, ok := scr.Screen(id)
		if !stale || !s.clock.Now().Before(until) {
			return lines, stale, ok
		}
	}
}
