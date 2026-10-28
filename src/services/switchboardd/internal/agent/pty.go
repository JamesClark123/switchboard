package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Session is a live agent terminal: raw bytes in (keystrokes/prompts) and out
// (screen). Backed by a PTY in production; a fake in tests.
type Session interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

// SessionFactory creates a Session for a sandbox's agent.
type SessionFactory func(sandboxID string, spec *pb.AgentSpec) (Session, error)

// SpawnFactory creates a Session for a sandbox's agent launched with a hook
// token (SWI-17, research R13): sp carries its generation and token, to be set
// in the agent's environment only (PTYFactory). The zero Spawn has neither.
type SpawnFactory func(sandboxID string, spec *pb.AgentSpec, sp Spawn) (Session, error)

// Waiter is implemented by a Session whose underlying process can be waited on.
// The production ptySession waits on its child; the registry uses it to learn
// when an agent exits on its own (feature 009, research R7).
type Waiter interface {
	Wait() error
}

// sessionEntry is one live session plus whether the registry closed it on
// purpose (so a deliberate Close is never reported as an agent exit), whether
// the agent in it has reported ready (SessionStart, see MarkReady) and the
// timer that parks it if it never does (nil for an agent kind that reports no
// readiness, which counts as ready from the start).
type sessionEntry struct {
	s        Session
	closing  bool
	ready    bool
	startT   *time.Timer
	timedOut bool // the start timer parked it NEEDS_INPUT
	// gen is the agent's generation (0 without a token index); done is closed
	// once its exec client has exited (onExit), or at once for a session that
	// cannot be waited on.
	gen  int64
	done chan struct{}
}

// Notifier emits a developer notification (the agent Hub).
type Notifier interface {
	EmitNotification(sandboxID string, kind pb.NotificationKind, message string, now time.Time) *pb.NotificationEvent
}

// Agent readiness (SWI-15). A new session's Claude Code agent is STARTING until
// its SessionStart hook fires, which it does only once its input box takes
// typed input: after any first-run dialog (workspace trust, Bypass Permissions
// mode) has been answered, never while one is showing (verified against Claude
// Code 2.1.311 in a PTY; a paste written right after SessionStart lands in the
// input box). Prompts are typed only into a ready agent (see Ready). One that
// has not reported within agentStartTimeout is set NEEDS_INPUT with a
// notification: something on its screen (most likely one of those dialogs)
// needs the developer, whose mark-free also vouches for it. Another agent kind
// fires no hooks and is ready at once. Package-level so tests can shorten it.
var agentStartTimeout = 90 * time.Second

// Registry holds one Session per sandbox, created on demand (R8: one PTY per
// agent serves both programmatic prompting and interactive attach).
//
// When a session's child exits on its own the registry forgets it and reports
// AGENT_STATUS_EXITED through the StatusUpdater (feature 009, research R7): the
// status was never set before, and the orchestrator folds it into a failed run.
// A session the registry closed itself (sandbox stopped, refreshed, destroyed)
// is forgotten silently. The next Session/Prompt starts a fresh agent either way.
type Registry struct {
	mu sync.Mutex
	// statusMu orders the registry's own status writes (STARTING, the start
	// timeout's NEEDS_INPUT, EXITED, Close's reset) against each other and
	// against MarkReady, so the IDLE the ready hook writes after MarkReady
	// returns always lands last. Taken before mu, never while holding it, and
	// never by a path a status write's observers can reach (Close, Session).
	statusMu sync.Mutex
	sessions map[string]*sessionEntry
	factory  SpawnFactory
	// tokens is the hook-token index (nil: agents are launched without a
	// token). Read without mu: Generation runs inside the sandbox store's
	// write transactions, while Session holds mu across a store write.
	tokens atomic.Pointer[HookTokens]
	status StatusUpdater
	notify Notifier
	now    func() time.Time
	logf   func(format string, args ...any)
	// gate is asked before every spawn (SWI-17, research R14; nil = none).
	gate OrphanGate
	// onLost, when set, is told that an agent's exec client exited without the
	// registry closing it, instead of the registry writing EXITED: the agent
	// may outlive its client (research R14), so a probe decides.
	onLost func(sandboxID string, gen int64)
}

// NewRegistry constructs a Registry using the given session factory. Exit
// reporting is wired with SetStatusUpdater.
func NewRegistry(factory SessionFactory) *Registry {
	return NewSpawnRegistry(func(sandboxID string, spec *pb.AgentSpec, _ Spawn) (Session, error) {
		return factory(sandboxID, spec)
	})
}

// NewSpawnRegistry constructs a Registry whose factory launches each agent
// with its generation and hook token (SetHookTokens).
func NewSpawnRegistry(factory SpawnFactory) *Registry {
	return &Registry{sessions: map[string]*sessionEntry{}, factory: factory, now: time.Now}
}

// SetHookTokens registers the hook-token index each spawned agent gets its
// generation and token from (SWI-17, research R13; nil = none).
func (r *Registry) SetHookTokens(t *HookTokens) { r.tokens.Store(t) }

// RetireTokens retires every hook token of a sandbox known to have no agent
// left (a confirmed stop; with purge, a destroy, which drops its generation
// counter too). No-op without a token index.
func (r *Registry) RetireTokens(sandboxID string, purge bool) {
	if t := r.tokens.Load(); t != nil {
		t.RetireAll(sandboxID, purge)
	}
}

// Generation is the sandbox's agent generation counter (0 without a token
// index): the generation a NEEDS_DEVELOPER hold records as hold_gen.
func (r *Registry) Generation(sandboxID string) int64 {
	t := r.tokens.Load()
	if t == nil {
		return 0
	}
	return t.Generation(sandboxID)
}

// SetOrphanGate registers the gate every spawn asks first (SWI-17, research
// R14; nil = none).
func (r *Registry) SetOrphanGate(g OrphanGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gate = g
}

// SetOnClientExit registers the observer told that an agent's exec client
// exited on its own (SWI-17, research R14). With one set, the registry no
// longer writes EXITED itself: the observer probes whether the agent is gone.
// It is called under the registry lock (as the gate is), so it must not call
// back into the registry and must not block (Manager.AgentClientExited only
// marks the sandbox unknown and probes asynchronously).
func (r *Registry) SetOnClientExit(fn func(sandboxID string, gen int64)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onLost = fn
}

// SetStatusUpdater registers where an agent's spontaneous exit is reported as
// AGENT_STATUS_EXITED (nil = not reported). When it can also be read (the
// sandbox Manager's Get), Prompt verifies that a typed prompt was submitted and
// presses Enter again while it was not (see ensureSubmitted).
func (r *Registry) SetStatusUpdater(status StatusUpdater) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

// SetNotifier registers where an agent that never finished starting is
// announced (nil = status only).
func (r *Registry) SetNotifier(n Notifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notify = n
}

// SetLogger registers where the registry reports a submit retry (nil = silent).
func (r *Registry) SetLogger(logf func(format string, args ...any)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logf = logf
}

func (r *Registry) log(format string, args ...any) {
	r.mu.Lock()
	logf := r.logf
	r.mu.Unlock()
	if logf != nil {
		logf(format, args...)
	}
}

// ErrHeld refuses to start an agent for a sandbox whose run holds it (SWI-17,
// research R13): only the developer's own terminal (AttachAgent) starts one
// then, so nothing else can spawn the fresh session that ends a hold.
var ErrHeld = errors.New("sandbox is held; open its terminal")

// Session returns the sandbox's session, creating it on first use. A created
// session's agent is recorded STARTING (SWI-15) until it reports ready. With a
// token index, each spawned agent is a new generation with its own hook token.
func (r *Registry) Session(sandboxID string, spec *pb.AgentSpec) (Session, error) {
	return r.session(sandboxID, spec, false)
}

// SessionUnlessHeld is Session, refusing to start an agent (ErrHeld) while the
// sandbox's run holds it. A live session is returned as Session does.
func (r *Registry) SessionUnlessHeld(sandboxID string, spec *pb.AgentSpec) (Session, error) {
	return r.session(sandboxID, spec, true)
}

func (r *Registry) session(sandboxID string, spec *pb.AgentSpec, refuseHeld bool) (Session, error) {
	r.mu.Lock()
	if e, ok := r.sessions[sandboxID]; ok {
		r.mu.Unlock()
		return e.s, nil
	}
	if refuseHeld && r.heldLocked(sandboxID) {
		r.mu.Unlock()
		return nil, ErrHeld
	}
	// The gate never blocks, so asking it under mu is safe; a pass holds the
	// sandbox's runtime lock until the session is registered (research R14).
	release := func() {}
	if r.gate != nil {
		rel, err := r.gate.Admit(sandboxID)
		if err != nil {
			r.mu.Unlock()
			return nil, err
		}
		release = rel
	}
	var sp Spawn
	tokens := r.tokens.Load()
	if tokens != nil {
		var err error
		if sp, err = tokens.Issue(sandboxID); err != nil {
			// An agent without a token would never count for steps or holds.
			release()
			r.mu.Unlock()
			return nil, err
		}
	}
	s, err := r.factory(sandboxID, spec, sp)
	if err != nil {
		if tokens != nil {
			tokens.Retire(sandboxID, sp.Gen) // never started
		}
		release()
		r.mu.Unlock()
		return nil, err
	}
	e := &sessionEntry{s: s, ready: !reportsReady(spec), gen: sp.Gen, done: make(chan struct{})}
	r.sessions[sandboxID] = e
	if !e.ready {
		limit := agentStartTimeout
		e.startT = time.AfterFunc(limit, func() { r.startTimedOut(sandboxID, e, limit) })
	}
	release()
	r.mu.Unlock()
	if w, ok := s.(Waiter); ok {
		go r.watch(sandboxID, e, w)
	} else {
		close(e.done)
	}
	if e.ready {
		return s, nil
	}
	// On its own goroutine: the status write publishes a sandbox change whose
	// observers take locks a caller may hold here (the terminal registry creates
	// a broadcaster's session under its own lock, and the gRPC server's emit
	// reads its attachment counts), and may call back into the registry.
	go r.writeUnready(sandboxID, e, pb.AgentStatus_AGENT_STATUS_STARTING)
	return s, nil
}

// reportsReady reports whether the spec's agent fires the SessionStart hook
// readiness is learned from (Claude Code); another kind is ready at once.
func reportsReady(spec *pb.AgentSpec) bool {
	switch spec.GetKind() {
	case "", "claude", "claude-code":
		return true
	}
	return false
}

// stopTimer stops an entry's start timer, if it has one.
func (e *sessionEntry) stopTimer() {
	if e.startT != nil {
		e.startT.Stop()
	}
}

// writeUnready records a not-ready status for e's agent unless it has already
// reported ready or its session is gone, reporting whether it wrote. Holding
// statusMu across the check and the write keeps a concurrent MarkReady (and so
// the ready hook's IDLE) behind it.
func (r *Registry) writeUnready(sandboxID string, e *sessionEntry, st pb.AgentStatus) bool {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.writeUnreadyLocked(sandboxID, e, st)
}

func (r *Registry) writeUnreadyLocked(sandboxID string, e *sessionEntry, st pb.AgentStatus) bool {
	r.mu.Lock()
	cur, ok := r.sessions[sandboxID]
	stale := !ok || cur != e || e.ready
	status, now := r.status, r.now()
	r.mu.Unlock()
	if stale {
		return false
	}
	if status != nil {
		_, _ = status.SetAgentStatus(sandboxID, st, now)
	}
	return true
}

// Held reports whether the sandbox's run holds it (any hold): no agent is
// started for it then except from the developer's terminal (ErrHeld).
func (r *Registry) Held(sandboxID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.heldLocked(sandboxID)
}

func (r *Registry) heldLocked(sandboxID string) bool {
	g, ok := r.status.(sandboxGetter)
	if !ok {
		return false
	}
	sb, err := g.Get(sandboxID)
	return err == nil && sb.GetController().GetRun().GetHold() != pb.WorkerHold_WORKER_HOLD_NONE
}

// Live reports whether the sandbox has a session, without creating one.
func (r *Registry) Live(sandboxID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sessions[sandboxID]
	return ok
}

// LiveGen reports the generation of the sandbox's live session, if it has one.
func (r *Registry) LiveGen(sandboxID string) (gen int64, live bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[sandboxID]; ok {
		return e.gen, true
	}
	return 0, false
}

// ErrNoLiveAgent is returned by Existing when the sandbox has no live session.
var ErrNoLiveAgent = errors.New("no live agent")

// Existing returns the sandbox's live session, or ErrNoLiveAgent, in one step
// under the registry lock: there is no window between checking liveness and
// using the session, and it never starts an agent (SWI-17: the step path's
// broadcaster provider, terminal.Registry.BroadcasterExisting).
func (r *Registry) Existing(sandboxID string) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[sandboxID]; ok {
		return e.s, nil
	}
	return nil, ErrNoLiveAgent
}

// Ready reports whether the sandbox's live session has an agent that reported
// in (MarkReady) — the only kind a prompt may be typed into unattended.
func (r *Registry) Ready(sandboxID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.sessions[sandboxID]
	return ok && e.ready
}

// MarkReady records that the sandbox's agent is at its prompt: its SessionStart
// hook fired (the HookServer's ready observer; Claude Code fires it only once
// its input box takes input), or the developer vouched for it with mark-free.
// No live session is a no-op. It returns after any STARTING / NEEDS_INPUT write
// in flight, so a status the caller writes next lands after it.
func (r *Registry) MarkReady(sandboxID string) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[sandboxID]; ok && !e.ready {
		e.ready = true
		e.stopTimer()
	}
}

// ReassertUnready rewrites the record of a live agent that has not reported
// ready back to STARTING, or NEEDS_INPUT once its start timed out. Any hook can
// write IDLE over those (a lost SessionStart, then a developer typing into the
// agent and its Stop), which would leave the worker reading free while every
// delivery is refused and mark-free has nothing to vouch for. A ready agent, or
// no session, is left alone, and so is a record no longer reading seen (the
// stray status the caller found): a hook that landed since, such as the
// WORKING of the developer's next prompt, is not overwritten.
func (r *Registry) ReassertUnready(sandboxID string, seen pb.AgentStatus) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.mu.Lock()
	e, ok := r.sessions[sandboxID]
	st := pb.AgentStatus_AGENT_STATUS_STARTING
	if ok && e.timedOut {
		st = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
	}
	g, canGet := r.status.(sandboxGetter)
	r.mu.Unlock()
	if !ok {
		return
	}
	if canGet {
		if sb, err := g.Get(sandboxID); err != nil || sb.GetAgent().GetStatus() != seen {
			return
		}
	}
	if r.writeUnreadyLocked(sandboxID, e, st) {
		r.log("[agent] %s: agent never reported ready; status %s put back over a stray %s", sandboxID, statusWord(st), statusWord(seen))
	}
}

// statusWord is an agent status for the log, e.g. "needs_input".
func statusWord(st pb.AgentStatus) string {
	return strings.ToLower(strings.TrimPrefix(st.String(), "AGENT_STATUS_"))
}

// startTimedOut parks an agent that never reported ready: NEEDS_INPUT plus a
// notification, so the developer opens its terminal. A run queued for it stays
// queued (the worker is not IDLE); the agent's first hook clears this.
func (r *Registry) startTimedOut(sandboxID string, e *sessionEntry, limit time.Duration) {
	// The notification goes out under statusMu too, so an agent that reports
	// ready meanwhile is never told about after the fact.
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	if !r.writeUnreadyLocked(sandboxID, e, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT) {
		return
	}
	r.log("[agent] %s: agent did not report ready within %s; parking it NEEDS_INPUT", sandboxID, limit)
	r.mu.Lock()
	e.timedOut = true
	notify, now := r.notify, r.now()
	r.mu.Unlock()
	if notify != nil {
		notify.EmitNotification(sandboxID, pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING, "agent did not finish starting — open its terminal", now)
	}
}

// watch waits for the session's child to exit and runs the exit hook.
func (r *Registry) watch(sandboxID string, e *sessionEntry, w Waiter) {
	_ = w.Wait()
	r.onExit(sandboxID, e)
}

// onExit forgets an exited session (if it is still the current one) and, unless
// the registry closed it on purpose, reports AGENT_STATUS_EXITED exactly once.
// With a client-exit observer (research R14) the observer is told instead: the
// agent may outlive its exec client, so only a probe says it is gone. The
// observer runs under mu, before the session leaves the table, so no spawn
// asking the gate can slip in between and start a second agent beside a
// surviving one. done is closed last, after the locks are released.
func (r *Registry) onExit(sandboxID string, e *sessionEntry) {
	defer close(e.done)
	r.statusMu.Lock() // EXITED lands after a STARTING write in flight
	defer r.statusMu.Unlock()
	r.mu.Lock()
	lost, onLost := !e.closing, r.onLost
	if lost && onLost != nil {
		onLost(sandboxID, e.gen)
	}
	cur, ok := r.sessions[sandboxID]
	if ok && cur == e {
		delete(r.sessions, sandboxID)
	}
	e.stopTimer()
	report := lost && onLost == nil && r.status != nil
	status := r.status
	now := r.now()
	r.mu.Unlock()
	if ok && cur == e {
		_ = e.s.Close() // release the PTY file; the child is already gone
	}
	if report {
		_, _ = status.SetAgentStatus(sandboxID, pb.AgentStatus_AGENT_STATUS_EXITED, now)
	}
}

// pasteStart / pasteEnd are the bracketed-paste markers (CSI 200~ / CSI 201~)
// that wrap every prompt body. A raw-mode agent TUI (Claude Code) that receives
// a bare run of bytes has to guess where a paste ends: when the Enter lands in
// the same read as the body, the "\r" is folded into the text and the prompt
// sits in the input box unsent. Writing the body alone and the Enter 50 ms
// later only helps while the transport keeps the two writes apart; behind
// `sbx exec` they evidently arrive together — orchestrator runs were typed into
// the worker's agent but never started, which is exactly what a merged read
// reproduces in a plain PTY. Inside the markers the body is one paste by
// definition, the terminator ends it, and the "\r" that follows is parsed as
// its own keystroke whether or not it shares a read with the body (verified
// against Claude Code 2.1.296–2.1.307 in a PTY, merged and unmerged). This is
// also how Claude Code's own PTY driver feeds a prompt to another claude
// process.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
	submitKey  = "\r"
	// keypadEnter is the application-keypad Enter (SS3 M), which the agent's
	// input parser maps to the same Return key as "\r" — and which a transport
	// that rewrites a carriage return into a line feed (a line feed only
	// inserts a newline into the agent's input box) leaves alone.
	keypadEnter = "\x1bOM"
)

// promptSubmitDelay separates the paste from the trailing Enter keystroke so
// the submit is also a distinct read on a transport that does preserve write
// boundaries. Package-level so tests can zero it.
var promptSubmitDelay = 50 * time.Millisecond

// A typed prompt is verified, not assumed: an idle agent that received one
// reports WORKING through its UserPromptSubmit hook within a moment of the
// submit. While the agent is still idle submitCheckDelay after the Enter, the
// Enter is pressed again — first as "\r", then as keypadEnter — up to
// submitRetries times (observed on macOS hosts: the paste landed in the input
// box but the Enter typed through `sbx exec` never submitted it, while an
// Enter typed by the developer did). An extra Enter on an agent that did submit
// is harmless: Enter on an empty input box does nothing. Package-level so tests
// can shorten them.
var (
	submitCheckDelay = 2500 * time.Millisecond
	submitRetries    = 3
)

// Typed is the exact byte sequence Prompt writes for text: the body as one
// bracketed paste followed by the Enter keystroke. For tests in other packages.
func Typed(text string) string { return pasteStart + text + pasteEnd + submitKey }

// Prompt types text into the sandbox's agent and submits it (FR-022).
//
// The body goes out as one bracketed paste (pasteStart/pasteEnd), then — after
// promptSubmitDelay — the Enter as a separate write. Submission uses a carriage
// return ("\r"), the byte a terminal sends for the Enter key in raw mode, NOT a
// linefeed ("\n"), which the agent's multiline input inserts as a literal newline
// rather than submitting.
//
// It starts no agent for a held sandbox (ErrHeld, SWI-17): an escape-hatch
// result arriving after the agent exited must not spawn the session that ends
// the hold.
func (r *Registry) Prompt(sandboxID string, spec *pb.AgentSpec, text string) error {
	s, err := r.SessionUnlessHeld(sandboxID, spec)
	if err != nil {
		return err
	}
	idleAt, verify := r.idleNow(sandboxID)
	if _, err := s.Write([]byte(pasteStart + text + pasteEnd)); err != nil {
		return err
	}
	time.Sleep(promptSubmitDelay)
	if _, err := s.Write([]byte(submitKey)); err != nil {
		return err
	}
	if verify {
		go r.ensureSubmitted(sandboxID, s, idleAt)
	}
	return nil
}

// atPrompt reports whether an agent with this status sits at an empty input box
// between turns: IDLE, or WAITING on background work (SWI-12) — a prompt typed
// into a waiting agent starts a turn just as one typed into an idle agent does,
// so it is verified the same way.
func atPrompt(st pb.AgentStatus) bool {
	return st == pb.AgentStatus_AGENT_STATUS_IDLE || st == pb.AgentStatus_AGENT_STATUS_WAITING
}

// idleNow reports whether the sandbox's agent is recorded at its prompt (IDLE
// or WAITING) right now — so a submit can be verified through its next hook —
// and the time of the check.
func (r *Registry) idleNow(sandboxID string) (time.Time, bool) {
	r.mu.Lock()
	g, ok := r.status.(sandboxGetter)
	now := r.now()
	r.mu.Unlock()
	if !ok {
		return now, false
	}
	sb, err := g.Get(sandboxID)
	return now, err == nil && atPrompt(sb.GetAgent().GetStatus())
}

// submitted reports whether the agent has reported anything since the prompt
// was typed: it is no longer at its prompt, or a hook event arrived after
// `since`.
func (r *Registry) submitted(sandboxID string, since time.Time) bool {
	r.mu.Lock()
	g, ok := r.status.(sandboxGetter)
	r.mu.Unlock()
	if !ok {
		return true
	}
	sb, err := g.Get(sandboxID)
	if err != nil {
		return true // the sandbox is gone; nothing to retry on
	}
	return !atPrompt(sb.GetAgent().GetStatus()) ||
		sb.GetAgent().GetLastEventAt().AsTime().After(since)
}

// current reports whether s is still the sandbox's live session.
func (r *Registry) current(sandboxID string, s Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.sessions[sandboxID]
	return ok && e.s == s
}

// ensureSubmitted presses Enter again while a prompt typed into an idle agent
// goes unreported (see submitCheckDelay).
func (r *Registry) ensureSubmitted(sandboxID string, s Session, since time.Time) {
	for attempt := 1; attempt <= submitRetries; attempt++ {
		time.Sleep(submitCheckDelay)
		if !r.current(sandboxID, s) || r.submitted(sandboxID, since) {
			return
		}
		key, how := submitKey, "Enter"
		if attempt > 1 {
			key, how = keypadEnter, "keypad Enter"
		}
		r.log("[agent] %s: prompt still unsubmitted %s after typing it; pressing %s again (%d/%d)",
			sandboxID, time.Duration(attempt)*submitCheckDelay, how, attempt, submitRetries)
		if _, err := s.Write([]byte(key)); err != nil {
			return
		}
	}
}

// TypeStep types one sequence step into the sandbox's live agent (SWI-17,
// contracts/worker-step.md §2): the text as one bracketed paste, then the
// Enter after promptSubmitDelay, as Prompt does, but with no ensureSubmitted:
// the step's submit check decides on any re-press (RePress), from the hooks
// and the screen. It never starts an agent (ErrNoLiveAgent): the step guard
// starts one only for a run's first step, and refuses the call meanwhile.
// spec names the agent the step is for, as Prompt's does. sentAt is when the
// Enter was written.
func (r *Registry) TypeStep(sandboxID string, _ *pb.AgentSpec, text string) (sentAt time.Time, err error) {
	s, err := r.Existing(sandboxID)
	if err != nil {
		return time.Time{}, err
	}
	if _, err := s.Write([]byte(pasteStart + text + pasteEnd)); err != nil {
		return time.Time{}, err
	}
	time.Sleep(promptSubmitDelay)
	if _, err := s.Write([]byte(submitKey)); err != nil {
		return time.Time{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now(), nil
}

// RePress presses Enter again in the sandbox's live agent, for a step whose
// text is still in the input box: "\r" for the first re-press (n = 0), then
// the keypad Enter, as ensureSubmitted does.
func (r *Registry) RePress(sandboxID string, n int) error {
	s, err := r.Existing(sandboxID)
	if err != nil {
		return err
	}
	key := submitKey
	if n > 0 {
		key = keypadEnter
	}
	_, err = s.Write([]byte(key))
	return err
}

// Close terminates and forgets a sandbox's session. A session closed here is
// never reported as an agent exit.
func (r *Registry) Close(sandboxID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[sandboxID]; ok {
		e.closing = true
		e.stopTimer()
		_ = e.s.Close()
		delete(r.sessions, sandboxID)
		if !e.ready {
			go r.resetUnready(sandboxID)
		}
	}
}

// Terminator is implemented by a Session whose exec client can be asked to
// end (SIGTERM) before it is killed: a sandbox stop's drain (research R14).
type Terminator interface {
	Terminate() error
}

// CloseForStop is Close for a sandbox about to be stopped (research R14): the
// session is marked closing (no EXITED, no probe) and forgotten, and its exec
// client is sent TERM, not killed. done is closed once the client has exited
// (by the existing watcher's onExit; never a second Wait), and finish kills it
// if it is still alive and releases the PTY. The caller waits on done only
// after this returns, outside every registry lock: onExit takes mu. No session
// returns a nil done and a no-op finish.
func (r *Registry) CloseForStop(sandboxID string) (done <-chan struct{}, finish func()) {
	r.mu.Lock()
	e, ok := r.sessions[sandboxID]
	if !ok {
		r.mu.Unlock()
		return nil, func() {}
	}
	e.closing = true
	e.stopTimer()
	delete(r.sessions, sandboxID)
	if !e.ready {
		go r.resetUnready(sandboxID)
	}
	r.mu.Unlock()
	if t, ok := e.s.(Terminator); ok {
		_ = t.Terminate()
	}
	return e.done, func() { _ = e.s.Close() }
}

// resetUnready clears the STARTING (or start-timeout NEEDS_INPUT) an agent
// closed before it reported ready leaves on its record, so the worker reads
// IDLE again and the next delivery starts a fresh agent instead of waiting on
// one that is gone. Asynchronous (Close runs inside status observers) and
// ordered by statusMu: skipped once a new session exists, whose own STARTING
// then lands after it.
func (r *Registry) resetUnready(sandboxID string) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.mu.Lock()
	_, live := r.sessions[sandboxID]
	status, now := r.status, r.now()
	r.mu.Unlock()
	g, ok := status.(sandboxGetter)
	if live || !ok {
		return
	}
	if sb, err := g.Get(sandboxID); err != nil ||
		(sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_STARTING && sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT) {
		return
	}
	_, _ = status.SetAgentStatus(sandboxID, pb.AgentStatus_AGENT_STATUS_IDLE, now)
}

// --- Production PTY-backed factory ---

// ptySession wraps a PTY file and its child process.
type ptySession struct {
	f   *os.File
	cmd *exec.Cmd
}

func (p *ptySession) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptySession) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *ptySession) Resize(cols, rows uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
}
func (p *ptySession) Close() error {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.f.Close()
}

// Terminate asks the exec client to end (Terminator): SIGTERM.
func (p *ptySession) Terminate() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Signal(syscall.SIGTERM)
}

// Wait blocks until the agent child exits (Waiter). The registry calls it once
// per session; Close only kills, it never waits, so the two never race on the
// process state.
func (p *ptySession) Wait() error { return p.cmd.Wait() }

// Target describes how the daemon reaches a sandbox's agent through `sbx`.
type Target struct {
	// Ref is the sbx-addressable handle (container_ref / --name). It MUST NOT be
	// the daemon's uuid registry key: sbx addresses sandboxes by name, so
	// `sbx exec -it <uuid> …` fails immediately and the PTY reports EOF the moment
	// a client attaches.
	Ref string
	// Workdir is the in-container working directory to open the agent in — the
	// sandbox's code directory. sbx mounts the seeded workspace at the same path
	// it has on the host (which is why `sxb` run inside the sandbox finds the
	// workspace marker by walking up, FR-017), so this is the sandbox's
	// WorkspacePath. Empty means "leave sbx's default directory".
	Workdir string
}

// agentArgv builds the in-sandbox command + arguments to launch for an agent spec.
// The sbx runner currently always creates sandboxes with the `claude` agent
// (sandbox/runner.go), so an unset kind maps to claude rather than a bare shell —
// opening the terminal should drop the user into their agent, matching what
// `sbx run claude` does. A future multi-runner world can carry the concrete kind
// through here.
//
// Claude is launched with --dangerously-skip-permissions so it runs
// non-interactively inside the throwaway sandbox: a detached session has no
// terminal attached to answer a permission prompt, so it must never block on one.
// (The injected settings.local.json also sets defaultMode=bypassPermissions; the
// flag is the CLI-level belt-and-braces for the same intent.) Spec args follow so
// a caller can still override/extend the invocation.
func agentArgv(spec *pb.AgentSpec) []string {
	var argv []string
	switch spec.GetKind() {
	case "", "claude", "claude-code":
		argv = []string{"claude", "--dangerously-skip-permissions"}
	default:
		argv = []string{spec.GetKind()}
	}
	return append(argv, spec.GetArgs()...)
}

// agentCommand maps an AgentSpec to the in-sandbox command. The daemon execs into
// the sandbox via `sbx exec` and launches the agent from the sandbox's code
// directory (tgt.Workdir) so the terminal opens exactly where `sbx run <agent>`
// would — not at the container root. The `cd` is best-effort (`; exec` rather than
// `&& exec`) so a missing/renamed workspace still yields a working agent shell
// instead of an immediate EOF.
//
// The command is launched UNWRAPPED w.r.t. session/job-control (no setsid/nohup)
// on purpose. The T002 spike (research.md R4 "Verification result") verified
// against real sbx/Docker that a docker-exec child survives both a hard kill of
// the host exec client and a controlling-PTY hangup — the exact ptySession.Close()
// sequence — so host-side persistence already satisfies FR-002 (an in-flight AI
// prompt keeps running after the terminal closes / across a daemon restart's
// client-kill). `exec` replaces the wrapping shell with the agent so the PTY's
// controlling process IS the agent, keeping interactive job control intact.
//
// An agent launched with a hook token (SWI-17, research R13) gets it, and its
// generation, through the environment: both are set in the `sbx exec` client's
// own environment (cmd.Env, never os.Setenv, which every other child of the
// daemon would inherit) and passed by NAME (`-e SXB_HOOK_TOKEN -e
// SXB_AGENT_GEN`, which reads the value from the client's environment, as
// `docker exec -e NAME` does), so no value is in argv or the host process
// list. ⚠ The `-e NAME` form is documentation-derived like the rest of `sbx
// exec`'s argv; TestAgentCommandPassesTheTokenByName pins it. Neither the
// launch string nor the environment is ever logged.
//
// Just before the `exec` (after the cd, which stays first), the launch string
// records the agent's generation, PID, start time and PTY in ~/.sxb/agent.pid
// (pidRecord, research R14), which is how a restarted daemon finds an agent
// that outlived it.
func agentCommand(sbxBin string, tgt Target, spec *pb.AgentSpec, sp Spawn) *exec.Cmd {
	launch := pidRecord(sp.Gen) + "exec"
	for _, a := range agentArgv(spec) {
		launch += " " + shellQuote(a)
	}
	if tgt.Workdir != "" {
		launch = "cd " + shellQuote(tgt.Workdir) + " 2>/dev/null; " + launch
	}
	args := []string{"exec", "-it"}
	if sp.Token != "" {
		args = append(args, "-e", envHookToken, "-e", envAgentGen)
	}
	args = append(args, tgt.Ref, "bash", "-lc", launch)
	cmd := exec.Command(sbxBin, args...)
	if sp.Token != "" {
		cmd.Env = append(os.Environ(), envHookToken+"="+sp.Token, envAgentGen+"="+strconv.FormatInt(sp.Gen, 10))
	}
	return cmd
}

// The agent's environment variables carrying its hook token and generation
// (research R13); the hook command and the escape-hatch wrapper read them.
const (
	envHookToken = "SXB_HOOK_TOKEN"
	envAgentGen  = "SXB_AGENT_GEN"
)

// shellQuote wraps s in single quotes for safe use inside a `bash -lc` string,
// escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// PTYFactory returns a SessionFactory that starts the sandbox's agent under a PTY.
//
// resolve maps the daemon's uuid registry key to the sbx Target (handle + code
// directory). It may return a zero Target when the sandbox is unknown; the factory
// then falls back to targeting the uuid so behavior is no worse than before
// resolution existed. resolve may be nil (uuid used directly, no workdir), which is
// intended only for tests.
func PTYFactory(sbxBin string, resolve func(sandboxID string) Target) SpawnFactory {
	return func(sandboxID string, spec *pb.AgentSpec, sp Spawn) (Session, error) {
		tgt := Target{Ref: sandboxID}
		if resolve != nil {
			if r := resolve(sandboxID); r.Ref != "" {
				tgt = r
			}
		}
		cmd := agentCommand(sbxBin, tgt, spec, sp)
		f, err := pty.Start(cmd)
		if err != nil {
			// The error names the binary, never the launch string or env.
			return nil, fmt.Errorf("start pty for %s: %w", sandboxID, err)
		}
		return &ptySession{f: f, cmd: cmd}, nil
	}
}
