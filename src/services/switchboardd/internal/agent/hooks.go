package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// StatusUpdater applies an agent-status transition to a sandbox (implemented by
// the sandbox Manager, which persists it and publishes a sandbox_changed event).
type StatusUpdater interface {
	SetAgentStatus(sandboxID string, status pb.AgentStatus, now time.Time) (*pb.Sandbox, error)
}

// sandboxGetter is the optional read side of a StatusUpdater (the sandbox
// Manager has it). Without it a legacy Notification cannot be told apart from
// the idle nudge and keeps its historical NEEDS_INPUT mapping.
type sandboxGetter interface {
	Get(sandboxID string) (*pb.Sandbox, error)
}

// EvidenceRecorder is the optional evidence side of a StatusUpdater (the
// sandbox Manager has it, SWI-17 research R4): it writes the hook evidence on
// the agent session (last_submit_at, last_turn_end_at, …) and publishes the
// change once. Without it no evidence is recorded.
type EvidenceRecorder interface {
	SetAgentEvidence(sandboxID string, fn func(*pb.AgentSession)) (*pb.Sandbox, error)
}

// HoldReleaser is the optional hold side of a StatusUpdater (the sandbox
// Manager has it, SWI-17 research R7): it clears a run held NEEDS_DEVELOPER,
// only while it is still held so.
type HoldReleaser interface {
	ClearHeldRun(sandboxID string) (bool, error)
}

// sandboxLister is the optional list side of a StatusUpdater, which the
// periodic input-idle sample walks.
type sandboxLister interface {
	List() ([]*pb.Sandbox, error)
}

// ScreenFunc reads a sandbox's agent screen without creating anything
// (terminal.Registry.Screen): its lines, whether it is stale, and ok = false
// when the sandbox has no screen.
type ScreenFunc func(sandboxID string) (lines []termview.Line, stale, ok bool)

// HookEvent is one hook report as the step submit check sees it (SWI-17,
// research R4), delivered on a Tap after dispatch has applied its status.
type HookEvent struct {
	Event            string
	Source           string // SessionStart: startup | resume | clear (compact never reaches a tap)
	NotificationType string
	// NeedsInput is a Notification that asks the developer for input (not the
	// idle nudge, not auth_success): a turn is waiting on them.
	NeedsInput      bool
	BackgroundTasks int // Stop: tasks still pending
	At              time.Time
	// SessionID, PID and Gen identify the reporting agent: Claude Code's
	// session_id, the hook's X-Sxb-Pid (the claude process that fired it) and
	// the generation its token belongs to. A tap only sees the main agent's
	// events (research R13): the current generation's token and its main PID.
	SessionID string
	PID       int
	Gen       int64
}

// tapBuffer is how many events a tap holds for a slow reader; the fan-out is
// best effort and drops beyond it (the submit check also reads the screen).
const tapBuffer = 64

// idleSampleEvery is how often input_idle_since is sampled for sandboxes with
// a run recorded, besides on each hook (data-model.md AgentSession).
const idleSampleEvery = 5 * time.Second

// Claude Code's Notification hook carries a notification_type (its matcher key);
// these are the ones the daemon tells apart.
const (
	notifyIdle        = "idle_prompt"  // "Claude is waiting for your input": idle for a while, nothing on screen
	notifyAuthSuccess = "auth_success" // sign-in finished; not an input request
)

// maxHookBody bounds the original hook command's JSON body ({"event",
// "sandbox_id"}). Claude Code's forwarded input is streamed instead, under
// maxHookRead (readHookInput).
const maxHookBody = 1 << 20

// clearSettle is how long the screen is polled for the post-clear state after
// a clear SessionStart that may end a hold (research R13): the hook can land
// before the redraw. clearPoll is the poll interval. Variables so tests can
// shorten them.
var (
	clearSettle = time.Second
	clearPoll   = 50 * time.Millisecond
)

// The headers a hook (and the escape-hatch wrapper) identifies its agent with
// (research R13): its generation's token and number, from the agent's
// environment, and the PID of the claude process that fired it.
const (
	headerToken = "X-Sxb-Token"
	headerGen   = "X-Sxb-Gen"
	headerPID   = "X-Sxb-Pid"
)

// backgroundWaitMax bounds how long an agent stays WAITING on background work
// (SWI-12). A background task that never ends — a dev server, a watcher — would
// otherwise hold the agent there for good; past this the Stop is taken at its
// word: IDLE and a task-complete notification, which lets the orchestrator's
// review flow take over. A package constant like every other bound (009 R15).
const backgroundWaitMax = 30 * time.Minute

// finishedTaskStatus are background_tasks statuses that no longer hold the
// agent. Claude Code does not document the status values and, as observed
// (2.1.311), drops a task from the list once it ends ("running" is the only
// value seen), so any other status is taken as still pending.
var finishedTaskStatus = map[string]bool{
	"completed": true, "done": true, "finished": true, "failed": true, "error": true,
	"killed": true, "stopped": true, "cancelled": true, "canceled": true,
}

// --- Hook injection (research R2) ---

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}
type hookMatcher struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}
type claudePermissions struct {
	DefaultMode string `json:"defaultMode"`
}
type claudeSettings struct {
	Permissions claudePermissions        `json:"permissions"`
	Hooks       map[string][]hookMatcher `json:"hooks"`
}

// BuildSettings produces the .claude/settings.local.json contents that make a
// sandbox's Claude Code call back to the daemon on the lifecycle events the
// daemon maps to an agent status (see HookServer.dispatch):
//
//   - UserPromptSubmit / PreToolUse -> WORKING (a task began, or the agent is
//     actively running tools — the latter also flips it back to WORKING after a
//     permission prompt is answered).
//   - Notification                  -> by notification_type: permission_prompt
//     (and any other request for the developer) -> NEEDS_INPUT; idle_prompt,
//     Claude Code's "waiting for your input" nudge after a minute of idleness,
//     -> IDLE, because an idle agent is a free one — mapping it to NEEDS_INPUT
//     parked every orchestrator worker a minute after its run finished and
//     nothing ever un-parked it (feature 009).
//   - Stop                          -> IDLE + a task-complete notification, or
//     WAITING (no notification) when the turn ended with background work still
//     running (SWI-12): Claude Code ends a turn while a background Bash, subagent
//     or Monitor runs and wakes the agent again (UserPromptSubmit) when it
//     finishes, so that Stop is a pause, not the end of the task.
//   - SessionStart                  -> IDLE, silently: the agent finished
//     starting (STARTING, SWI-15). Its source tells a fresh start or resume
//     from a mid-session clear or compaction; see sessionStart.
//
// SessionStart (other than a compaction) also tells the agent Registry the
// agent is ready (SetOnReady): Claude Code fires it once its input box is up.
// The event and sandbox id ride on the callback URL's query; every hook also
// forwards Claude Code's hook input (stdin JSON, which carries the session_id,
// the notification_type, the Stop's background_tasks and the SessionStart
// source) as the request body. Without the work-start hooks the status would
// never leave IDLE while the agent runs, so the "working" indicator would never
// show.
//
// Each hook also sends the agent's hook token and generation from its
// environment (SXB_HOOK_TOKEN, SXB_AGENT_GEN; SWI-17, research R13) and $PPID,
// the claude process that fired it (hooks run as its `sh -c` children,
// verified on Claude Code 2.1.312). The settings only name the variables, so
// they carry no secret, and rewriting them (RefreshHooks) changes no agent's
// token.
//
// It also sets permissions.defaultMode to "bypassPermissions" so the agent runs
// non-interactively inside the sandbox — no permission prompt can block a detached
// session that has no terminal attached to answer it.
func BuildSettings(sandboxID, callbackURL string) claudeSettings {
	curl := func(event string) []hookMatcher {
		// Best-effort, fire-and-forget status ping. It MUST never disrupt the
		// agent: `-m 2` bounds the time so a slow/unreachable daemon can't stall a
		// prompt, `-o /dev/null` discards the response (UserPromptSubmit injects a
		// hook's stdout into Claude's context), and `|| true` guarantees exit 0 so
		// a failed ping is never surfaced as a hook error. The URL is quoted: it
		// carries `&`. Claude Code writes the hook input to stdin and closes it.
		cmd := fmt.Sprintf(
			"curl -s -m 2 -o /dev/null -X POST -H 'Content-Type: application/json' "+
				`-H "%s: $%s" -H "%s: $%s" -H "%s: $PPID" `+
				"--data-binary @- '%s?event=%s&sandbox_id=%s' || true",
			headerToken, envHookToken, headerGen, envAgentGen, headerPID, callbackURL, event, sandboxID)
		return []hookMatcher{{Matcher: "", Hooks: []hookEntry{{Type: "command", Command: cmd}}}}
	}
	return claudeSettings{
		// Sandboxes are isolated throwaway environments driven by an unattended
		// agent, so skip the interactive permission prompts entirely — otherwise a
		// detached prompt would stall forever waiting on approval nobody is there
		// to give.
		Permissions: claudePermissions{DefaultMode: "bypassPermissions"},
		Hooks: map[string][]hookMatcher{
			"UserPromptSubmit": curl("UserPromptSubmit"),
			"PreToolUse":       curl("PreToolUse"),
			"Notification":     curl("Notification"),
			"Stop":             curl("Stop"),
			"SessionStart":     curl("SessionStart"),
			// SessionEnd changes no status; a main agent's re-probes an orphan
			// (handle, research R14).
			"SessionEnd": curl("SessionEnd"),
		},
	}
}

// InjectHooks writes the hook settings into <workspace>/.claude/settings.local.json
// (highest-precedence project settings, gitignored). An existing file keeps
// every other key — Claude Code records the developer's own choices there — and
// only the daemon's `hooks` and `permissions.defaultMode` are (re)written, so
// the daemon can refresh its hooks on every start without losing them.
func InjectHooks(workspacePath, sandboxID, callbackURL string) error {
	dir := filepath.Join(workspacePath, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.local.json")
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			doc = map[string]any{} // unreadable: the daemon's file wins
		}
	}
	want := BuildSettings(sandboxID, callbackURL)
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	perms["defaultMode"] = want.Permissions.DefaultMode
	doc["permissions"] = perms
	doc["hooks"] = want.Hooks
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// --- Hook callback handling ---

// hookPayload is one hook callback as the daemon sees it: the event and sandbox
// from the URL query (or, for sandboxes still running the original hook
// command, the JSON body), plus what dispatch needs from Claude Code's
// forwarded input: the notification_type, and how many background tasks a Stop
// left running.
type hookPayload struct {
	Event             string `json:"event"`
	SandboxID         string `json:"sandbox_id"`
	Matcher           string `json:"matcher"`
	NotificationType  string `json:"notification_type,omitempty"`
	Source            string `json:"source,omitempty"` // SessionStart: startup | resume | clear | compact
	PendingBackground int    `json:"-"`
	SessionID         string `json:"-"` // Claude Code's session_id (forwarded input)
	PID               int    `json:"-"` // X-Sxb-Pid: the claude process that fired the hook
	// Tokened is a call whose hook token checked out (HookTokens.Verify); Gen
	// is then its generation. A tokenless call counts for status display only.
	Tokened bool  `json:"-"`
	Gen     int64 `json:"-"`
	// Foreign is a tokened call that is not the main agent's (a nested
	// session, a process left by an earlier generation): it does not report
	// the agent ready either.
	Foreign bool `json:"-"`
}

// hookInput is the part of Claude Code's own hook input the daemon reads.
type hookInput struct {
	HookEventName    string           `json:"hook_event_name"`
	SessionID        string           `json:"session_id"`
	NotificationType string           `json:"notification_type"`
	Source           string           `json:"source"`           // SessionStart
	BackgroundTasks  []backgroundTask `json:"background_tasks"` // Stop / SubagentStop
}

// backgroundTask is one entry of a Stop's background_tasks: a background Bash
// ("shell"), subagent, monitor, … the session is still waiting on.
type backgroundTask struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// pendingBackground counts the tasks that still hold the agent.
func pendingBackground(tasks []backgroundTask) int {
	n := 0
	for _, t := range tasks {
		if !finishedTaskStatus[t.Status] {
			n++
		}
	}
	return n
}

// stopper is the part of a timer the backstop needs (*time.Timer has it).
type stopper interface{ Stop() bool }

// HookServer receives hook callbacks and translates them into agent-status
// transitions + NotificationEvents.
type HookServer struct {
	hub       *Hub
	status    StatusUpdater
	now       func() time.Time
	afterFunc func(time.Duration, func()) stopper
	onReady   func(sandboxID string)
	onEnd     func(sandboxID string)
	screen    ScreenFunc
	repaint   func(sandboxID string) bool
	tokens    *HookTokens
	logf      func(format string, args ...any)

	mu       sync.Mutex
	backstop map[string]backstopTimer // sandbox id -> its WAITING backstop
	seq      uint64
	taps     map[string]map[uint64]chan HookEvent // sandbox id -> open taps
}

// backstopTimer is one armed WAITING backstop; gen tells a fired timer apart
// from a newer one armed for the same sandbox since.
type backstopTimer struct {
	t   stopper
	gen uint64
}

// NewHookServer constructs a HookServer.
func NewHookServer(hub *Hub, status StatusUpdater) *HookServer {
	return &HookServer{
		hub: hub, status: status, now: time.Now,
		afterFunc: func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) },
		backstop:  map[string]backstopTimer{},
		taps:      map[string]map[uint64]chan HookEvent{},
	}
}

// SetScreen registers where the agent screen is read for input_idle_since
// and a clear's hold release (SWI-17; nil = never sampled, and no clear ends
// a hold).
func (s *HookServer) SetScreen(fn ScreenFunc) { s.screen = fn }

// SetRepaint registers the lazy repaint nudge for a stale screen
// (terminal.Registry.RequestRepaint; nil = none).
func (s *HookServer) SetRepaint(fn func(sandboxID string) bool) { s.repaint = fn }

// SetTokens registers the hook-token index calls are checked against (SWI-17,
// research R13). Without one every call is tokenless: status display only.
func (s *HookServer) SetTokens(t *HookTokens) { s.tokens = t }

// SetLogger registers where hold releases and dropped calls are reported
// (nil = silent). No line carries a token.
func (s *HookServer) SetLogger(logf func(format string, args ...any)) { s.logf = logf }

func (s *HookServer) log(format string, args ...any) {
	if s.logf != nil {
		s.logf(format, args...)
	}
}

// Tap subscribes to a sandbox's hook events (SWI-17, research R4): each main
// agent event is delivered after dispatch has applied its status. The channel
// is buffered and the fan-out best effort: a reader that falls tapBuffer
// events behind misses the rest. cancel ends the subscription; the channel is
// never closed.
func (s *HookServer) Tap(sandboxID string) (<-chan HookEvent, func()) {
	ch := make(chan HookEvent, tapBuffer)
	s.mu.Lock()
	s.seq++
	id := s.seq
	if s.taps[sandboxID] == nil {
		s.taps[sandboxID] = map[uint64]chan HookEvent{}
	}
	s.taps[sandboxID][id] = ch
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.taps[sandboxID], id)
			if len(s.taps[sandboxID]) == 0 {
				delete(s.taps, sandboxID)
			}
		})
	}
}

// publish fans an event out to the sandbox's taps, dropping it for a full one.
func (s *HookServer) publish(sandboxID string, ev HookEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.taps[sandboxID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// SetOnReady registers the observer told that a sandbox's agent reported in
// with SessionStart (the agent Registry's MarkReady, SWI-15). It runs before the
// event's own status write.
func (s *HookServer) SetOnReady(fn func(sandboxID string)) { s.onReady = fn }

// SetOnSessionEnd registers the observer told of a main-agent SessionEnd
// (SWI-17, research R14: a detached agent exiting normally is re-probed at
// once). It runs after the event is dispatched.
func (s *HookServer) SetOnSessionEnd(fn func(sandboxID string)) { s.onEnd = fn }

// Handle processes one hook callback (POST /hook). The current hook command
// names the event and sandbox in the query and posts Claude Code's hook input
// as the body; the original one posts {"event","sandbox_id"} as the body. Both
// stay accepted: a sandbox keeps the hook command it was launched with until
// its agent restarts.
//
// A call carrying a hook token is checked against the sandbox's unretired
// generations (research R13): one the sandbox does not have is dropped with
// 403, and nothing of it is applied. A call with no token (an agent launched
// before this feature, or a process the daemon did not start) still updates
// the status display, and nothing else.
func (s *HookServer) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p hookPayload
	q := r.URL.Query()
	if ev := q.Get("event"); ev != "" {
		p.Event, p.SandboxID = ev, q.Get("sandbox_id")
		in := readHookInput(r.Body)
		p.NotificationType, p.Source, p.SessionID = in.NotificationType, in.Source, in.SessionID
		p.PendingBackground = pendingBackground(in.BackgroundTasks)
		p.PID, _ = strconv.Atoi(r.Header.Get(headerPID))
	} else if err := json.NewDecoder(io.LimitReader(r.Body, maxHookBody)).Decode(&p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if p.SandboxID == "" {
		http.Error(w, "missing sandbox_id", http.StatusBadRequest)
		return
	}
	if tok := r.Header.Get(headerToken); tok != "" && s.tokens != nil {
		gen, ok := s.tokens.Verify(p.SandboxID, r.Header.Get(headerGen), tok)
		if !ok {
			s.log("[hook] %s: %s dropped: unknown agent token", p.SandboxID, p.Event)
			http.Error(w, "unknown agent token", http.StatusForbidden)
			return
		}
		p.Tokened, p.Gen = true, gen
	}
	s.handle(p)
	w.WriteHeader(http.StatusNoContent)
}

// handle processes one hook event: its status (dispatch), then, for the main
// agent's events (research R13), the end of a hold, the recorded evidence and
// the taps, and finally an input-idle sample (SWI-17). A generation's first
// non-compact SessionStart binds its main agent first, so that event is the
// main agent's already.
func (s *HookServer) handle(p hookPayload) {
	now := s.now()
	if p.Tokened && s.tokens != nil && p.Event == "SessionStart" && p.Source != "compact" {
		s.tokens.BindMain(p.SandboxID, p.Gen, p.PID)
	}
	main := s.mainEvent(p)
	p.Foreign = p.Tokened && !main
	needsInput := false
	if !s.nestedEvent(p) {
		needsInput = s.dispatch(p)
	}
	if main {
		s.releaseHold(p)
		s.recordEvidence(p, needsInput, now)
		if p.Event != "SessionStart" || p.Source != "compact" {
			s.publish(p.SandboxID, HookEvent{
				Event: p.Event, Source: p.Source, NotificationType: p.NotificationType, NeedsInput: needsInput,
				BackgroundTasks: p.PendingBackground, At: now, SessionID: p.SessionID, PID: p.PID, Gen: p.Gen,
			})
		}
		if p.Event == "SessionEnd" && s.onEnd != nil {
			s.onEnd(p.SandboxID)
		}
	}
	s.sampleIdle(p.SandboxID)
}

// mainEvent reports whether a hook event comes from the sandbox's main agent,
// the only one whose events count for evidence, holds and steps (research
// R13): it carries the current generation's token and comes from that
// generation's main PID. Until the PID is bound, nothing is.
func (s *HookServer) mainEvent(p hookPayload) bool {
	return p.Tokened && s.tokens != nil && s.tokens.IsMain(p.SandboxID, p.Gen, p.PID)
}

// nestedEvent reports whether a hook event comes from a session nested in an
// agent (a `claude -p` it ran): its generation's main PID is bound and the
// event names another process. Such a session's start, turns and Stop say
// nothing of the agent's own status, so they change none and notify nothing.
// With no PID bound (a lost SessionStart) every event still drives the status.
func (s *HookServer) nestedEvent(p hookPayload) bool {
	if !p.Foreign || p.PID <= 0 {
		return false
	}
	pid, _, ok := s.tokens.MainPID(p.SandboxID, p.Gen)
	return ok && pid != p.PID
}

// dispatch maps a hook event to a status transition and (for terminal/await
// events) a notification, reporting whether it was a request for input.
// Exposed for direct testing.
func (s *HookServer) dispatch(p hookPayload) (needsInput bool) {
	now := s.now()
	// Only SessionStart says the agent is at its prompt: after a daemon restart
	// an agent that outlived the old client keeps sending its other hooks for
	// the same sandbox while the new one is still booting.
	// A compaction (possibly by such an agent, mid-turn) is not a start either.
	// Neither is another session's (Foreign): a nested `claude -p` starting
	// says nothing about the agent's own input box.
	if p.Event == "SessionStart" && p.Source != "compact" && !p.Foreign && s.onReady != nil {
		s.onReady(p.SandboxID)
	}
	switch p.Event {
	case "SessionStart":
		s.sessionStart(p, now)
	case "Stop":
		if p.PendingBackground > 0 {
			// The turn ended to wait on background work, not because the task is
			// done: Claude Code wakes the agent when the work finishes.
			_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_WAITING, now)
			s.armBackstop(p.SandboxID)
			return false
		}
		s.disarmBackstop(p.SandboxID)
		sb, _ := s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_IDLE, now)
		if !quietStep(sb) {
			s.hub.EmitNotification(p.SandboxID, pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, "agent task complete", now)
		}
	case "Notification":
		return s.notification(p, now)
	case "SessionEnd":
		// The agent is exiting, not working: no status change. Its exec
		// client's exit (or the orphan re-probe in handle) settles it.
	case "PreToolUse", "PostToolUse":
		if st, ok := s.agentStatus(p.SandboxID); ok && st == pb.AgentStatus_AGENT_STATUS_WAITING {
			// A background subagent's own tool calls fire these too, with the
			// same sandbox id, while the main turn waits on it: the wait (and
			// its backstop) goes on. The main agent's wake-up is a
			// UserPromptSubmit (the <task-notification>).
			return false
		}
		s.working(p.SandboxID, now)
	default:
		// UserPromptSubmit etc. => the agent is working (a WAITING agent woken
		// by its finished background work lands here too).
		s.working(p.SandboxID, now)
	}
	return false
}

// recordEvidence writes what a main-agent hook says about the agent's turns
// and sessions on its record (research R4): the submit, the end of a turn
// with nothing left in the background, a request for input, and a new session
// (a compaction is not one).
func (s *HookServer) recordEvidence(p hookPayload, needsInput bool, now time.Time) {
	var fn func(*pb.AgentSession)
	at := timestamppb.New(now)
	switch {
	case p.Event == "UserPromptSubmit":
		fn = func(a *pb.AgentSession) { a.LastSubmitAt = at }
	case p.Event == "Stop" && p.PendingBackground == 0:
		fn = func(a *pb.AgentSession) { a.LastTurnEndAt = at }
	case p.Event == "Notification" && needsInput:
		fn = func(a *pb.AgentSession) { a.LastNeedsInputAt = at }
	case p.Event == "SessionStart" && (p.Source == "startup" || p.Source == "resume" || p.Source == "clear"):
		fn = func(a *pb.AgentSession) {
			a.LastSessionStartAt, a.LastSessionSource = at, p.Source
			if p.SessionID != "" {
				a.SessionId = p.SessionID
			}
		}
	default:
		return
	}
	s.writeEvidence(p.SandboxID, fn)
}

// writeEvidence applies fn through the status side's EvidenceRecorder, if it
// has one.
func (s *HookServer) writeEvidence(sandboxID string, fn func(*pb.AgentSession)) {
	if r, ok := s.status.(EvidenceRecorder); ok {
		_, _ = r.SetAgentEvidence(sandboxID, fn)
	}
}

// quietStep reports whether a sandbox's recorded run is on a setup or cleanup
// step, whose turn ends are not the task's (FR-223, research R10): no
// task-complete notification for them.
func quietStep(sb *pb.Sandbox) bool {
	switch sb.GetController().GetRun().GetStepRole() {
	case pb.StepRole_STEP_ROLE_SETUP, pb.StepRole_STEP_ROLE_CLEANUP:
		return true
	}
	return false
}

// releaseHold ends a NEEDS_DEVELOPER hold on a fresh session (FR-216, research
// R7): a main-agent SessionStart from a new agent (startup) or after /clear.
// A resume or a compaction keeps the conversation and ends nothing. It runs
// before the evidence write, so the record still names the session the hold
// was taken on.
//
// The hook alone is not trusted (research R13): a process in the sandbox can
// read its own token. A startup ends the hold only from a generation spawned
// after the hold began (greater than its hold_gen; while held, only the
// developer's terminal spawns one, ErrHeld). A clear ends it only with a new
// session_id and the daemon's own screen showing the post-clear state, polled
// for up to clearSettle after the hook (releaseAfterClear).
func (s *HookServer) releaseHold(p hookPayload) {
	if p.Event != "SessionStart" || (p.Source != "startup" && p.Source != "clear") {
		return
	}
	g, ok := s.status.(sandboxGetter)
	r, canRelease := s.status.(HoldReleaser)
	if !ok || !canRelease {
		return
	}
	sb, err := g.Get(p.SandboxID)
	run := sb.GetController().GetRun()
	if err != nil || run.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		return
	}
	switch {
	case p.Source == "startup" && p.Gen > run.GetHoldGen():
		if cleared, _ := r.ClearHeldRun(p.SandboxID); cleared {
			s.log("[hook] %s: hold on %s ended by a fresh agent (generation %d)", p.SandboxID, run.GetId(), p.Gen)
		}
	case p.Source == "clear" && p.SessionID != "" && p.SessionID != sb.GetAgent().GetSessionId():
		go s.releaseAfterClear(p.SandboxID, run.GetId(), g, r)
	}
}

// releaseAfterClear ends a hold once the daemon's screen shows the post-clear
// state (PostClear), polling for up to clearSettle, with a repaint nudge while
// it is stale. Only while the same run is still held.
func (s *HookServer) releaseAfterClear(sandboxID, runID string, g sandboxGetter, r HoldReleaser) {
	if s.screen == nil {
		return
	}
	deadline := time.Now().Add(clearSettle)
	for {
		lines, stale, ok := s.screen(sandboxID)
		switch {
		case stale && s.repaint != nil:
			s.repaint(sandboxID)
		case ok && !stale && PostClear(lines):
			sb, err := g.Get(sandboxID)
			if err != nil || sb.GetController().GetRun().GetId() != runID {
				return
			}
			if cleared, _ := r.ClearHeldRun(sandboxID); cleared {
				s.log("[hook] %s: hold on %s ended by /clear", sandboxID, runID)
			}
			return
		}
		if !time.Now().Before(deadline) {
			s.log("[hook] %s: a clear did not end the hold on %s: the screen does not show a cleared session", sandboxID, runID)
			return
		}
		time.Sleep(clearPoll)
	}
}

// sampleIdle keeps input_idle_since current for a sandbox with a run recorded
// (the idle-nudge fallback, research R1): set to when the input box became
// empty and ready with the agent not WORKING, unset otherwise. It is written
// only when it changes. An unreadable or stale screen tells nothing and
// changes nothing; a sandbox with no run has the field unset.
func (s *HookServer) sampleIdle(sandboxID string) {
	g, ok := s.status.(sandboxGetter)
	if !ok {
		return
	}
	sb, err := g.Get(sandboxID)
	if err != nil {
		return
	}
	s.sampleIdleOf(sb)
}

func (s *HookServer) sampleIdleOf(sb *pb.Sandbox) {
	idle := false
	if sb.GetController().GetRun() != nil {
		if s.screen == nil {
			return
		}
		lines, stale, ok := s.screen(sb.GetId())
		if !ok || stale {
			return
		}
		text, present, ready := InputBox(lines)
		idle = present && ready && strings.TrimSpace(text) == "" && sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_WORKING
	}
	if idle == (sb.GetAgent().GetInputIdleSince() != nil) {
		return
	}
	since := timestamppb.New(s.now())
	s.writeEvidence(sb.GetId(), func(a *pb.AgentSession) {
		switch {
		case !idle:
			a.InputIdleSince = nil
		case a.InputIdleSince == nil:
			a.InputIdleSince = since
		}
	})
}

// SampleIdle samples input_idle_since every idleSampleEvery for every sandbox
// with a run recorded (or a stale value to unset) until ctx ends: a box the
// developer empties fires no hook.
func (s *HookServer) SampleIdle(ctx context.Context) {
	t := time.NewTicker(idleSampleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sampleAll()
		}
	}
}

// sampleAll runs one input-idle sample over the status side's sandboxes.
func (s *HookServer) sampleAll() {
	l, ok := s.status.(sandboxLister)
	if !ok {
		return
	}
	all, err := l.List()
	if err != nil {
		return
	}
	for _, sb := range all {
		if sb.GetController().GetRun() != nil || sb.GetAgent().GetInputIdleSince() != nil {
			s.sampleIdleOf(sb)
		}
	}
}

// working records the agent busy on a turn, ending any background wait.
func (s *HookServer) working(sandboxID string, now time.Time) {
	s.disarmBackstop(sandboxID)
	_, _ = s.status.SetAgentStatus(sandboxID, pb.AgentStatus_AGENT_STATUS_WORKING, now)
}

// armBackstop (re)starts the WAITING backstop for a sandbox.
func (s *HookServer) armBackstop(sandboxID string) {
	s.armBackstopFor(sandboxID, backgroundWaitMax)
}

func (s *HookServer) armBackstopFor(sandboxID string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.backstop[sandboxID]; ok {
		b.t.Stop()
	}
	s.seq++
	gen := s.seq
	s.backstop[sandboxID] = backstopTimer{t: s.afterFunc(d, func() { s.backstopExpired(sandboxID, gen) }), gen: gen}
}

// RearmWaiting restores the backstop of every sandbox recorded WAITING when
// the daemon starts: the timers live in memory, and without this an agent
// whose wake-up was missed while the daemon was down would wait forever. Each
// gets what is left of backgroundWaitMax since its last hook event. A sandbox
// that is not running has no agent left to wait: it is set IDLE instead (a
// record a daemon stopped before a stop reset the status).
func (s *HookServer) RearmWaiting(sandboxes []*pb.Sandbox) {
	now := s.now()
	for _, sb := range sandboxes {
		if sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_WAITING {
			continue
		}
		if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
			_, _ = s.status.SetAgentStatus(sb.GetId(), pb.AgentStatus_AGENT_STATUS_IDLE, now)
			continue
		}
		left := backgroundWaitMax
		if at := sb.GetAgent().GetLastEventAt(); at != nil {
			left -= now.Sub(at.AsTime())
		}
		s.armBackstopFor(sb.GetId(), max(left, 0))
	}
}

// disarmBackstop cancels a sandbox's WAITING backstop (the agent moved on).
func (s *HookServer) disarmBackstop(sandboxID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.backstop[sandboxID]; ok {
		b.t.Stop()
		delete(s.backstop, sandboxID)
	}
}

// backstopExpired ends a wait that outlived backgroundWaitMax: an agent still
// WAITING is treated as having stopped (IDLE), and the developer is told the
// wait was given up — the background work may well still be running. gen names
// the timer that fired; a newer one armed since then owns the sandbox instead.
// A sandbox that stopped meanwhile needs nothing: leaving RUNNING already
// reset its agent.
func (s *HookServer) backstopExpired(sandboxID string, gen uint64) {
	s.mu.Lock()
	b, ok := s.backstop[sandboxID]
	current := ok && b.gen == gen
	if current {
		delete(s.backstop, sandboxID)
	}
	s.mu.Unlock()
	if !current {
		return
	}
	// Only a running sandbox still waiting is ended: one removed, stopped or
	// whose agent moved on since gets nothing.
	g, ok := s.status.(sandboxGetter)
	if !ok {
		return
	}
	sb, err := g.Get(sandboxID)
	if err != nil || sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_WAITING {
		return
	}
	now := s.now()
	_, _ = s.status.SetAgentStatus(sandboxID, pb.AgentStatus_AGENT_STATUS_IDLE, now)
	at := timestamppb.New(now)
	s.writeEvidence(sandboxID, func(a *pb.AgentSession) { a.LastTurnEndAt = at })
	if !quietStep(sb) {
		s.hub.EmitNotification(sandboxID, pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, backstopMessage, now)
	}
}

// backstopMessage tells the developer the wait was given up, not that the work
// finished: background work (a dev server, a long build) may still be running.
var backstopMessage = fmt.Sprintf("stopped waiting on background work after %s — check the agent; it may still be running", fmtWait(backgroundWaitMax))

// fmtWait renders a wait as "30m" / "2h" rather than time.Duration's "30m0s".
func fmtWait(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

// sessionStart marks an agent that finished starting IDLE (no notification:
// nothing happened that the developer must see). Claude Code also fires
// SessionStart inside a running session — "clear" after /clear (at the prompt,
// so IDLE is right) and "compact" after a compaction, which can happen mid-turn:
// that one changes nothing, or a busy worker would read as free. An unknown
// source counts only while the agent is still recorded starting.
func (s *HookServer) sessionStart(p hookPayload, now time.Time) {
	switch p.Source {
	case "startup", "resume", "clear":
	case "compact":
		return
	default:
		if st, ok := s.agentStatus(p.SandboxID); !ok || (st != pb.AgentStatus_AGENT_STATUS_STARTING && st != pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT) {
			return
		}
	}
	// Every way out of WAITING clears its backstop, this one too (a /clear or a
	// replaced agent while background work was pending).
	s.disarmBackstop(p.SandboxID)
	_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_IDLE, now)
}

// notification maps a Notification hook by its notification_type, reporting
// whether it asked the developer for input.
func (s *HookServer) notification(p hookPayload, now time.Time) bool {
	kind := p.NotificationType
	current, readable := s.agentStatus(p.SandboxID)
	waiting := readable && current == pb.AgentStatus_AGENT_STATUS_WAITING
	if kind == "" && readable && (current == pb.AgentStatus_AGENT_STATUS_IDLE || waiting) {
		// The original hook command forwards no type. A notification that
		// arrives while the agent is idle (or waiting on background work) can
		// only be the idle nudge: permission and elicitation prompts happen
		// mid-turn, after UserPromptSubmit or PreToolUse moved the agent to
		// WORKING.
		kind = notifyIdle
	}
	switch kind {
	case notifyAuthSuccess:
		return false
	case notifyIdle:
		if waiting {
			// Claude Code's idle nudge also fires while a turn waits on
			// background work; the agent is not idle, so it stays WAITING and
			// nobody is prompted.
			return false
		}
		s.disarmBackstop(p.SandboxID)
		// Idle is not a request for input: the Stop already said so, and an
		// orchestrator worker that is idle is free. The developer still gets the
		// "needs prompting" nudge for a sandbox of their own (001 R2); a
		// controlled sandbox is between runs, not waiting for anyone.
		sb, _ := s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_IDLE, now)
		if sb.GetController() == nil {
			s.hub.EmitNotification(p.SandboxID, pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING, "agent needs prompting", now)
		}
		return false
	default:
		// permission_prompt, elicitation_dialog, a type this daemon does not
		// know, or a legacy notification while the agent is busy: the agent
		// waits for the developer.
		s.disarmBackstop(p.SandboxID)
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, now)
		s.hub.EmitNotification(p.SandboxID, pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING, "agent needs prompting", now)
		return true
	}
}

// agentStatus reads the sandbox's recorded agent status; ok is false when the
// status side cannot be read at all.
func (s *HookServer) agentStatus(sandboxID string) (pb.AgentStatus, bool) {
	g, ok := s.status.(sandboxGetter)
	if !ok {
		return 0, false
	}
	sb, err := g.Get(sandboxID)
	if err != nil {
		return 0, false
	}
	return sb.GetAgent().GetStatus(), true
}
