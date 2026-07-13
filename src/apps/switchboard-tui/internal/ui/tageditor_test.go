package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// TestTagEditorRoundTrip proves `#` opens the tag editor prefilled with the
// current tag, Enter sends the trimmed tag to the daemon, and the list shows it
// (US5, FR-021/023, SC-007).
func TestTagEditorRoundTrip(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(sized(New(d, "/work")),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING, Tag: "old"}})

	// `#` opens the editor prefilled with the existing tag.
	m, _ = update(m, press("#"))
	if m.screen != screenTag {
		t.Fatalf("screen = %v, want screenTag", m.screen)
	}
	if m.tagInput.Value() != "old" {
		t.Fatalf("editor prefill = %q, want current tag", m.tagInput.Value())
	}

	// Replace the value and confirm.
	m.tagInput.SetValue("  auth-refactor  ")
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList {
		t.Fatalf("after confirm screen = %v, want list", m.screen)
	}
	if msg := runCmd(cmd); msg == nil {
		t.Fatal("confirm should issue a SetTag command")
	}
	if d.lastTagID != "sb1" || d.lastTag != "auth-refactor" {
		t.Fatalf("daemon got tag (%q,%q), want (sb1, auth-refactor trimmed)", d.lastTagID, d.lastTag)
	}
}

func TestTagEditorCancel(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(sized(New(d, "/work")),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})
	m, _ = update(m, press("#"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Errorf("esc should return to the list, got %v", m.screen)
	}
	if d.lastTagID != "" {
		t.Error("cancel must not call SetTag")
	}
}

// TestTagAndCountRenderOnRow proves the list row renders the tag chip and the
// connected-terminal count (US3/US5, FR-008/023).
func TestTagAndCountRenderOnRow(t *testing.T) {
	title := sandboxTitle(&pb.Sandbox{
		DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Tag: "billing", AttachedTerminals: 2, ExternalAttached: true,
	})
	if !strings.Contains(title, "#billing") {
		t.Errorf("row title missing tag chip: %q", title)
	}
	if !strings.Contains(title, "2") {
		t.Errorf("row title missing terminal count: %q", title)
	}
}

func TestRenderFieldsDifferOnTagAndCount(t *testing.T) {
	a := &pb.Sandbox{Id: "x"}
	if !renderFieldsDiffer(a, &pb.Sandbox{Id: "x", Tag: "t"}) {
		t.Error("tag change should mark the row dirty")
	}
	if !renderFieldsDiffer(a, &pb.Sandbox{Id: "x", AttachedTerminals: 1}) {
		t.Error("count change should mark the row dirty")
	}
	if renderFieldsDiffer(a, &pb.Sandbox{Id: "x"}) {
		t.Error("identical snapshots should not be dirty")
	}
}
