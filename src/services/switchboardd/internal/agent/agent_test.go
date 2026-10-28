package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func recv(t *testing.T, ch <-chan *pb.Event) *pb.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

func TestHubEmitFanoutAndBuffer(t *testing.T) {
	h := NewHub("host-1")
	_, ch, replay := h.Subscribe(true)
	if len(replay) != 0 {
		t.Fatalf("expected no replay on a fresh hub, got %d", len(replay))
	}

	ev := h.EmitNotification("sb1", pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, "done", time.Unix(1, 0))
	if ev.GetHostId() != "host-1" || ev.GetSandboxId() != "sb1" {
		t.Errorf("event attribution wrong: %+v", ev)
	}
	got := recv(t, ch)
	if got.GetNotification().GetId() != ev.GetId() {
		t.Errorf("subscriber did not receive the emitted notification")
	}

	// Sandbox change + removal also fan out.
	h.PublishSandbox(&pb.Sandbox{Id: "sb1", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	if recv(t, ch).GetSandboxChanged() == nil {
		t.Error("expected sandbox_changed event")
	}
	h.PublishRemoved("sb1")
	if recv(t, ch).GetRemoved() == nil {
		t.Error("expected removed event")
	}
}

func TestHubReplayAndAck(t *testing.T) {
	h := NewHub("host-1")
	h.EmitNotification("sb1", pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING, "needs you", time.Unix(1, 0))
	h.EmitNotification("sb2", pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, "done", time.Unix(2, 0))

	if len(h.Undelivered()) != 2 {
		t.Fatalf("expected 2 undelivered, got %d", len(h.Undelivered()))
	}

	// A reconnecting client replays both undelivered events (FR-026b).
	_, _, replay := h.Subscribe(true)
	if len(replay) != 2 {
		t.Fatalf("expected 2 replayed, got %d", len(replay))
	}

	// Ack one -> only the other remains undelivered.
	acked := h.Ack([]string{replay[0].GetNotification().GetId(), "nonexistent"})
	if acked != 1 {
		t.Errorf("acked = %d, want 1", acked)
	}
	if len(h.Undelivered()) != 1 {
		t.Errorf("expected 1 undelivered after ack, got %d", len(h.Undelivered()))
	}

	// A fresh subscribe replays only the still-undelivered one.
	_, _, replay2 := h.Subscribe(true)
	if len(replay2) != 1 {
		t.Errorf("expected 1 replayed after ack, got %d", len(replay2))
	}

	// Subscribe without replay returns nothing.
	_, _, none := h.Subscribe(false)
	if len(none) != 0 {
		t.Errorf("no-replay subscribe should return nothing, got %d", len(none))
	}
}

func TestHubUnsubscribe(t *testing.T) {
	h := NewHub("h")
	id, ch, _ := h.Subscribe(false)
	h.Unsubscribe(id)
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after unsubscribe")
	}
	// Emitting after unsubscribe must not panic.
	h.EmitNotification("sb", pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, "x", time.Unix(1, 0))
}

// fakeStatus records SetAgentStatus calls.
type fakeStatus struct {
	last       pb.AgentStatus
	calls      int
	lastID     string
	controller *pb.SandboxController // returned on the updated record
}

func (f *fakeStatus) SetAgentStatus(id string, s pb.AgentStatus, _ time.Time) (*pb.Sandbox, error) {
	f.calls++
	f.last = s
	f.lastID = id
	return &pb.Sandbox{Id: id, Controller: f.controller}, nil
}

// readableStatus is a fakeStatus whose current status the hook server can read
// (the sandbox Manager's Get), which enables the legacy-notification inference.
type readableStatus struct {
	fakeStatus
	current pb.AgentStatus
}

func (f *readableStatus) Get(id string) (*pb.Sandbox, error) {
	return &pb.Sandbox{Id: id, Agent: &pb.AgentSession{Status: f.current}, Controller: f.controller}, nil
}

func TestHookDispatchTransitions(t *testing.T) {
	cases := []struct {
		event      string
		wantStatus pb.AgentStatus
		wantNotify bool
		wantKind   pb.NotificationKind
	}{
		{"Stop", pb.AgentStatus_AGENT_STATUS_IDLE, true, pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE},
		// A legacy Notification (no type forwarded) on a status side that cannot
		// be read keeps the historical mapping.
		{"Notification", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, true, pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING},
		{"UserPromptSubmit", pb.AgentStatus_AGENT_STATUS_WORKING, false, 0},
	}
	for _, c := range cases {
		hub := NewHub("h")
		status := &fakeStatus{}
		hs := NewHookServer(hub, status)
		hs.dispatch(hookPayload{Event: c.event, SandboxID: "sb1"})

		if status.last != c.wantStatus || status.lastID != "sb1" {
			t.Errorf("%s: status = %v, want %v", c.event, status.last, c.wantStatus)
		}
		got := hub.Undelivered()
		if c.wantNotify {
			if len(got) != 1 || got[0].GetKind() != c.wantKind {
				t.Errorf("%s: expected a %v notification, got %+v", c.event, c.wantKind, got)
			}
		} else if len(got) != 0 {
			t.Errorf("%s: expected no notification, got %d", c.event, len(got))
		}
	}
}

// TestHookNotificationTypes pins the Notification mapping by notification_type:
// Claude Code's idle nudge ("Claude is waiting for your input", idle_prompt) is
// IDLE, not NEEDS_INPUT — mapping it to NEEDS_INPUT parked every orchestrator
// worker a minute after its run finished, with nothing to un-park it. A real
// request for the developer still parks the agent, and auth_success is noise.
func TestHookNotificationTypes(t *testing.T) {
	cases := []struct {
		name       string
		typ        string
		controlled bool
		current    pb.AgentStatus // for the readable status side; legacy inference only
		readable   bool
		wantCalls  int
		wantStatus pb.AgentStatus
		wantNotify bool
	}{
		{"idle nudge on a developer's sandbox", "idle_prompt", false, 0, false, 1, pb.AgentStatus_AGENT_STATUS_IDLE, true},
		{"idle nudge on a worker is silent", "idle_prompt", true, 0, false, 1, pb.AgentStatus_AGENT_STATUS_IDLE, false},
		{"permission prompt parks the agent", "permission_prompt", true, 0, false, 1, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, true},
		{"elicitation parks the agent", "elicitation_dialog", false, 0, false, 1, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, true},
		{"unknown type parks the agent", "something_new", false, 0, false, 1, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, true},
		{"auth success is ignored", "auth_success", false, 0, false, 0, 0, false},
		// Sandboxes launched with the original hook command forward no type: a
		// notification while the agent is idle can only be the idle nudge (a
		// prompt for the developer happens mid-turn, after WORKING).
		{"legacy while idle is the idle nudge", "", true, pb.AgentStatus_AGENT_STATUS_IDLE, true, 1, pb.AgentStatus_AGENT_STATUS_IDLE, false},
		{"legacy while working is a prompt", "", true, pb.AgentStatus_AGENT_STATUS_WORKING, true, 1, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, true},
		{"legacy while already parked stays parked", "", false, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, true, 1, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hub := NewHub("h")
			var controller *pb.SandboxController
			if c.controlled {
				controller = &pb.SandboxController{OrchestratorId: "orch-1", Name: "review-bot"}
			}
			var st StatusUpdater
			var calls *int
			var last *pb.AgentStatus
			if c.readable {
				r := &readableStatus{fakeStatus: fakeStatus{controller: controller}, current: c.current}
				st, calls, last = r, &r.calls, &r.last
			} else {
				f := &fakeStatus{controller: controller}
				st, calls, last = f, &f.calls, &f.last
			}
			NewHookServer(hub, st).dispatch(hookPayload{Event: "Notification", SandboxID: "sb1", NotificationType: c.typ})
			if *calls != c.wantCalls {
				t.Fatalf("status calls = %d, want %d", *calls, c.wantCalls)
			}
			if c.wantCalls > 0 && *last != c.wantStatus {
				t.Errorf("status = %v, want %v", *last, c.wantStatus)
			}
			got := hub.Undelivered()
			if c.wantNotify && (len(got) != 1 || got[0].GetKind() != pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING) {
				t.Errorf("expected a needs-prompting notification, got %+v", got)
			}
			if !c.wantNotify && len(got) != 0 {
				t.Errorf("expected no notification, got %+v", got)
			}
		})
	}
}

func TestHookHTTPHandler(t *testing.T) {
	hub := NewHub("h")
	hs := NewHookServer(hub, &fakeStatus{})
	srv := httptest.NewServer(http.HandlerFunc(hs.Handle))
	defer srv.Close()

	// Valid POST -> 204 + notification emitted.
	body, _ := json.Marshal(hookPayload{Event: "Stop", SandboxID: "sb1"})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if len(hub.Undelivered()) != 1 {
		t.Error("expected a notification from the POST")
	}

	// The current hook command: event + sandbox in the query, Claude Code's own
	// hook input as the body. The notification_type reaches dispatch.
	st := &fakeStatus{}
	hs2 := NewHookServer(NewHub("h"), st)
	srv2 := httptest.NewServer(http.HandlerFunc(hs2.Handle))
	defer srv2.Close()
	input := `{"session_id":"s","hook_event_name":"Notification","message":"Claude is waiting for your input","notification_type":"idle_prompt"}`
	resp, err = http.Post(srv2.URL+"?event=Notification&sandbox_id=sb9", "application/json", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent || st.lastID != "sb9" || st.last != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Errorf("query form: status %d, dispatched %s/%v; want 204, sb9/IDLE", resp.StatusCode, st.lastID, st.last)
	}
	// An empty body (the events that forward nothing) is fine.
	resp, err = http.Post(srv2.URL+"?event=Stop&sandbox_id=sb9", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent || st.last != pb.AgentStatus_AGENT_STATUS_IDLE || st.calls != 2 {
		t.Errorf("empty body: status %d, calls %d", resp.StatusCode, st.calls)
	}
	// The query form still needs a sandbox id.
	if r, _ := http.Post(srv2.URL+"?event=Stop", "application/json", strings.NewReader("")); r.StatusCode != http.StatusBadRequest {
		t.Errorf("query form without sandbox_id status = %d, want 400", r.StatusCode)
	}

	// GET is rejected.
	if r, _ := http.Get(srv.URL); r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", r.StatusCode)
	}
	// Bad JSON.
	if r, _ := http.Post(srv.URL, "application/json", strings.NewReader("{bad")); r.StatusCode != http.StatusBadRequest {
		t.Errorf("bad json status = %d, want 400", r.StatusCode)
	}
	// Missing sandbox_id.
	mb, _ := json.Marshal(hookPayload{Event: "Stop"})
	if r, _ := http.Post(srv.URL, "application/json", bytes.NewReader(mb)); r.StatusCode != http.StatusBadRequest {
		t.Errorf("missing sandbox_id status = %d, want 400", r.StatusCode)
	}
}

func TestInjectHooks(t *testing.T) {
	ws := t.TempDir()
	if err := InjectHooks(ws, "sb1", "http://host.docker.internal:8765/hook"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(ws, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s claudeSettings
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	// Sandboxes run the agent unattended, so permission prompts must be bypassed.
	if s.Permissions.DefaultMode != "bypassPermissions" {
		t.Errorf("defaultMode = %q, want bypassPermissions", s.Permissions.DefaultMode)
	}
	// Every event the daemon maps to a status MUST be injected, or that status is
	// never reached. In particular the work-start hooks (UserPromptSubmit /
	// PreToolUse) are what drive the WORKING indicator — their absence was a bug.
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "Notification", "Stop", "SessionStart"} {
		hm, ok := s.Hooks[event]
		if !ok || len(hm) == 0 || len(hm[0].Hooks) == 0 {
			t.Fatalf("settings missing %s hook", event)
		}
		// The event name and sandbox id ride on the (quoted — it carries `&`)
		// callback URL so dispatch reads them back whatever the body holds.
		if !strings.Contains(hm[0].Hooks[0].Command, `'http://host.docker.internal:8765/hook?event=`+event+`&sandbox_id=sb1'`) {
			t.Errorf("%s hook should name its event and the sandbox id in the URL: %s", event, hm[0].Hooks[0].Command)
		}
		// Every hook forwards Claude Code's hook input (SWI-17: the session_id
		// tells the main agent's events from a nested session's).
		if forwards := strings.Contains(hm[0].Hooks[0].Command, "--data-binary @-"); !forwards {
			t.Errorf("%s hook forwards stdin = %v: %s", event, forwards, hm[0].Hooks[0].Command)
		}
		// The ping must be best-effort: time-bounded, output discarded, and never
		// non-zero — otherwise a slow/unreachable daemon disrupts the agent.
		cmd := hm[0].Hooks[0].Command
		for _, want := range []string{"-m 2", "-o /dev/null", "|| true"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s hook command should be best-effort (missing %q): %s", event, want, cmd)
			}
		}
	}

	// Re-injecting into an existing file (the daemon refreshes its hooks on
	// every start) keeps the developer's other settings and replaces only the
	// daemon's keys.
	existing := `{"permissions":{"defaultMode":"default","allow":["Bash(go test:*)"]},"hooks":{"Stop":[]},"env":{"FOO":"1"}}`
	if err := os.WriteFile(filepath.Join(ws, ".claude", "settings.local.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InjectHooks(ws, "sb1", "http://host.docker.internal:8765/hook"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(ws, ".claude", "settings.local.json"))
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms["defaultMode"] != "bypassPermissions" || perms["allow"] == nil || doc["env"] == nil {
		t.Errorf("re-injection should keep the developer's keys and reset defaultMode: %s", b)
	}
	if hooks, _ := doc["hooks"].(map[string]any); len(hooks) != 5 {
		t.Errorf("re-injection should replace the hooks wholesale: %s", b)
	}

	// The injected work-start events must actually map to WORKING in dispatch, so
	// injection and handling stay in lockstep.
	for _, event := range []string{"UserPromptSubmit", "PreToolUse"} {
		st := &fakeStatus{}
		NewHookServer(NewHub("h"), st).dispatch(hookPayload{Event: event, SandboxID: "sb1"})
		if st.last != pb.AgentStatus_AGENT_STATUS_WORKING {
			t.Errorf("%s should dispatch to WORKING, got %v", event, st.last)
		}
	}
}
