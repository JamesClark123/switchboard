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
	last   pb.AgentStatus
	calls  int
	lastID string
}

func (f *fakeStatus) SetAgentStatus(id string, s pb.AgentStatus, _ time.Time) (*pb.Sandbox, error) {
	f.calls++
	f.last = s
	f.lastID = id
	return &pb.Sandbox{Id: id}, nil
}

func TestHookDispatchTransitions(t *testing.T) {
	cases := []struct {
		event      string
		wantStatus pb.AgentStatus
		wantNotify bool
		wantKind   pb.NotificationKind
	}{
		{"Stop", pb.AgentStatus_AGENT_STATUS_IDLE, true, pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE},
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
	// Every event the daemon maps to a status MUST be injected, or that status is
	// never reached. In particular the work-start hooks (UserPromptSubmit /
	// PreToolUse) are what drive the WORKING indicator — their absence was a bug.
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "Notification", "Stop"} {
		hm, ok := s.Hooks[event]
		if !ok || len(hm) == 0 || len(hm[0].Hooks) == 0 {
			t.Fatalf("settings missing %s hook", event)
		}
		if !strings.Contains(hm[0].Hooks[0].Command, "sb1") {
			t.Errorf("%s hook command should embed the sandbox id", event)
		}
		// The posted event name must match the map key so dispatch reads it back.
		if !strings.Contains(hm[0].Hooks[0].Command, `"event":"`+event+`"`) {
			t.Errorf("%s hook should post its own event name", event)
		}
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
