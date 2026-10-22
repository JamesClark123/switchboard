package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
//   - Stop                          -> IDLE + a task-complete notification.
//
// The event and sandbox id ride on the callback URL's query; the Notification
// hook also forwards Claude Code's hook input (stdin JSON, which carries the
// notification_type) as the request body. Without the work-start hooks the
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
			"Stop":             curl("Stop", false),
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
// command, the JSON body), plus the notification_type Claude Code forwarded.
type hookPayload struct {
	Event            string `json:"event"`
	SandboxID        string `json:"sandbox_id"`
	Matcher          string `json:"matcher"`
	NotificationType string `json:"notification_type,omitempty"`
}

// hookInput is the part of Claude Code's own hook input the daemon reads.
type hookInput struct {
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
}

// HookServer receives hook callbacks and translates them into agent-status
// transitions + NotificationEvents.
type HookServer struct {
	hub    *Hub
	status StatusUpdater
	now    func() time.Time
}

// NewHookServer constructs a HookServer.
func NewHookServer(hub *Hub, status StatusUpdater) *HookServer {
	return &HookServer{hub: hub, status: status, now: time.Now}
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
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_IDLE, now)
		s.hub.EmitNotification(p.SandboxID, pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, "agent task complete", now)
	case "Notification":
		s.notification(p, now)
	default:
		// UserPromptSubmit / PreToolUse / PostToolUse etc. => the agent is working.
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_WORKING, now)
	}
}

// notification maps a Notification hook by its notification_type.
func (s *HookServer) notification(p hookPayload, now time.Time) {
	kind := p.NotificationType
	if kind == "" && s.agentIdle(p.SandboxID) {
		// The original hook command forwards no type. A notification that
		// arrives while the agent is idle can only be the idle nudge: permission
		// and elicitation prompts happen mid-turn, after UserPromptSubmit or
		// PreToolUse moved the agent to WORKING.
		kind = notifyIdle
	}
	switch kind {
	case notifyAuthSuccess:
		return
	case notifyIdle:
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
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, now)
		s.hub.EmitNotification(p.SandboxID, pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING, "agent needs prompting", now)
	}
}

// agentIdle reports whether the sandbox's agent is recorded IDLE, when the
// status side can be read at all.
func (s *HookServer) agentIdle(sandboxID string) bool {
	g, ok := s.status.(sandboxGetter)
	if !ok {
		return false
	}
	sb, err := g.Get(sandboxID)
	return err == nil && sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_IDLE
}
