package notify

import "testing"

func TestNoopNotifier(t *testing.T) {
	// Must not panic and must accept any input.
	NoopNotifier{}.Notify("title", "message")
}

func TestNewNotifierIsBestEffort(t *testing.T) {
	n := New()
	if n == nil {
		t.Fatal("New returned nil")
	}
	// On a headless host beeep returns an error, which the wrapper swallows; the
	// call must not panic and must not block the caller.
	n.Notify("Switchboard", "task complete")
}
