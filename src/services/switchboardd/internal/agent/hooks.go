package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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

// Claude Code's Notification hook carries a notification_type (its matcher key);
// these are the ones the daemon tells apart.
const (
	notifyIdle        = "idle_prompt"  // "Claude is waiting for your input": idle for a while, nothing on screen
	notifyAuthSuccess = "auth_success" // sign-in finished; not an input request
)

// maxHookBody bounds the forwarded hook input the daemon will read.
const maxHookBody = 1 << 20

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
//
// The event and sandbox id ride on the callback URL's query; the Notification
// and Stop hooks also forward Claude Code's hook input (stdin JSON, which
// carries the notification_type and the Stop's background_tasks) as the
// request body. Without the work-start hooks the
// status would never leave IDLE while the agent runs, so the "working" indicator
// would never show.
//
// It also sets permissions.defaultMode to "bypassPermissions" so the agent runs
// non-interactively inside the sandbox — no permission prompt can block a detached
// session that has no terminal attached to answer it.
func BuildSettings(sandboxID, callbackURL string) claudeSettings {
	curl := func(event string, forwardInput bool) []hookMatcher {
		// Best-effort, fire-and-forget status ping. It MUST never disrupt the
		// agent: `-m 2` bounds the time so a slow/unreachable daemon can't stall a
		// prompt, `-o /dev/null` discards the response (UserPromptSubmit injects a
		// hook's stdout into Claude's context), and `|| true` guarantees exit 0 so
		// a failed ping is never surfaced as a hook error. The URL is quoted: it
		// carries `&`.
		data := "-d ''"
		if forwardInput {
			data = "--data-binary @-" // Claude Code writes the hook input to stdin and closes it
		}
		cmd := fmt.Sprintf(
			"curl -s -m 2 -o /dev/null -X POST -H 'Content-Type: application/json' %s '%s?event=%s&sandbox_id=%s' || true",
			data, callbackURL, event, sandboxID)
		return []hookMatcher{{Matcher: "", Hooks: []hookEntry{{Type: "command", Command: cmd}}}}
	}
	return claudeSettings{
		// Sandboxes are isolated throwaway environments driven by an unattended
		// agent, so skip the interactive permission prompts entirely — otherwise a
		// detached prompt would stall forever waiting on approval nobody is there
		// to give.
		Permissions: claudePermissions{DefaultMode: "bypassPermissions"},
		Hooks: map[string][]hookMatcher{
			"UserPromptSubmit": curl("UserPromptSubmit", false),
			"PreToolUse":       curl("PreToolUse", false),
			"Notification":     curl("Notification", true),
			"Stop":             curl("Stop", true),
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
	PendingBackground int    `json:"-"`
}

// hookInput is the part of Claude Code's own hook input the daemon reads.
type hookInput struct {
	HookEventName    string           `json:"hook_event_name"`
	NotificationType string           `json:"notification_type"`
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

	mu       sync.Mutex
	backstop map[string]backstopTimer // sandbox id -> its WAITING backstop
	seq      uint64
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
	}
}

// Handle processes one hook callback (POST /hook). The current hook command
// names the event and sandbox in the query and posts Claude Code's hook input
// as the body (empty for the events that forward nothing); the original one
// posts {"event","sandbox_id"} as the body. Both stay accepted: a sandbox keeps
// the hook command it was launched with until its agent restarts.
func (s *HookServer) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p hookPayload
	q := r.URL.Query()
	if ev := q.Get("event"); ev != "" {
		p.Event, p.SandboxID = ev, q.Get("sandbox_id")
		var in hookInput
		if err := json.NewDecoder(io.LimitReader(r.Body, maxHookBody)).Decode(&in); err == nil {
			p.NotificationType = in.NotificationType
			p.PendingBackground = pendingBackground(in.BackgroundTasks)
		}
	} else if err := json.NewDecoder(io.LimitReader(r.Body, maxHookBody)).Decode(&p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if p.SandboxID == "" {
		http.Error(w, "missing sandbox_id", http.StatusBadRequest)
		return
	}
	s.dispatch(p)
	w.WriteHeader(http.StatusNoContent)
}

// dispatch maps a hook event to a status transition and (for terminal/await
// events) a notification. Exposed for direct testing.
func (s *HookServer) dispatch(p hookPayload) {
	now := s.now()
	switch p.Event {
	case "Stop":
		if p.PendingBackground > 0 {
			// The turn ended to wait on background work, not because the task is
			// done: Claude Code wakes the agent when the work finishes.
			_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_WAITING, now)
			s.armBackstop(p.SandboxID)
			return
		}
		s.disarmBackstop(p.SandboxID)
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_IDLE, now)
		s.hub.EmitNotification(p.SandboxID, pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, "agent task complete", now)
	case "Notification":
		s.notification(p, now)
	default:
		// UserPromptSubmit / PreToolUse / PostToolUse etc. => the agent is working
		// (a WAITING agent woken by its finished background work lands here too).
		s.disarmBackstop(p.SandboxID)
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_WORKING, now)
	}
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
// gets what is left of backgroundWaitMax since its last hook event.
func (s *HookServer) RearmWaiting(sandboxes []*pb.Sandbox) {
	now := s.now()
	for _, sb := range sandboxes {
		if sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_WAITING {
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
// WAITING is treated as having stopped (IDLE + task complete). gen names the
// timer that fired; a newer one armed since then owns the sandbox instead.
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
	s.hub.EmitNotification(sandboxID, pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, "agent task complete", now)
}

// notification maps a Notification hook by its notification_type.
func (s *HookServer) notification(p hookPayload, now time.Time) {
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
		return
	case notifyIdle:
		if waiting {
			// Claude Code's idle nudge also fires while a turn waits on
			// background work; the agent is not idle, so it stays WAITING and
			// nobody is prompted.
			return
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
	default:
		// permission_prompt, elicitation_dialog, a type this daemon does not
		// know, or a legacy notification while the agent is busy: the agent
		// waits for the developer.
		s.disarmBackstop(p.SandboxID)
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, now)
		s.hub.EmitNotification(p.SandboxID, pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING, "agent needs prompting", now)
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
