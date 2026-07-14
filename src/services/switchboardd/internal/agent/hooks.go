package agent

import (
	"encoding/json"
	"fmt"
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
//   - Notification                  -> NEEDS_INPUT (awaiting the user).
//   - Stop                          -> IDLE + a task-complete notification.
//
// Without the work-start hooks the status would never leave IDLE while the agent
// runs, so the "working" indicator would never show. The sandbox id is embedded
// so the daemon can attribute each callback.
//
// It also sets permissions.defaultMode to "bypassPermissions" so the agent runs
// non-interactively inside the sandbox — no permission prompt can block a detached
// session that has no terminal attached to answer it.
func BuildSettings(sandboxID, callbackURL string) claudeSettings {
	curl := func(event string) []hookMatcher {
		body := fmt.Sprintf(`{"event":"%s","sandbox_id":"%s"}`, event, sandboxID)
		// Best-effort, fire-and-forget status ping. It MUST never disrupt the
		// agent: `-m 2` bounds the time so a slow/unreachable daemon can't stall a
		// prompt, `-o /dev/null` discards the response (UserPromptSubmit injects a
		// hook's stdout into Claude's context), and `|| true` guarantees exit 0 so
		// a failed ping is never surfaced as a hook error.
		cmd := fmt.Sprintf(
			"curl -s -m 2 -o /dev/null -X POST -H 'Content-Type: application/json' -d '%s' %s || true",
			body, callbackURL)
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
		},
	}
}

// InjectHooks writes the hook settings into <workspace>/.claude/settings.local.json
// (highest-precedence project settings, gitignored).
func InjectHooks(workspacePath, sandboxID, callbackURL string) error {
	dir := filepath.Join(workspacePath, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(BuildSettings(sandboxID, callbackURL), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.local.json"), b, 0o644)
}

// --- Hook callback handling ---

// hookPayload is the JSON a sandbox's hook posts back to the daemon.
type hookPayload struct {
	Event     string `json:"event"`
	SandboxID string `json:"sandbox_id"`
	Matcher   string `json:"matcher"`
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

// Handle processes one hook callback (POST /hook).
func (s *HookServer) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p hookPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
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
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, now)
		s.hub.EmitNotification(p.SandboxID, pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING, "agent needs prompting", now)
	default:
		// UserPromptSubmit / PreToolUse / PostToolUse etc. => the agent is working.
		_, _ = s.status.SetAgentStatus(p.SandboxID, pb.AgentStatus_AGENT_STATUS_WORKING, now)
	}
}
