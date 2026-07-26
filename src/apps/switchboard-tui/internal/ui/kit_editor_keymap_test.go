package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// TestKitFormKeyMapEnterInsertsNewline guards the fix for multi-line list fields:
// Enter must insert a newline (each line is a distinct list entry), not advance to
// the next field the way huh's default keymap does. huh drives the textarea's
// newline key from Text.NewLine and dispatches Text.Next/Submit before it, so the
// fix hinges on Enter being on NewLine and off Next/Submit.
func TestKitFormKeyMapEnterInsertsNewline(t *testing.T) {
	km := kitFormKeyMap()
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	tab := tea.KeyMsg{Type: tea.KeyTab}

	if !key.Matches(enter, km.Text.NewLine) {
		t.Error("Enter should insert a newline in multi-line Text fields")
	}
	if key.Matches(enter, km.Text.Next) {
		t.Error("Enter must NOT advance to the next field from a Text field")
	}
	if key.Matches(enter, km.Text.Submit) {
		t.Error("Enter must NOT submit the form from a Text field")
	}
	// Tab still navigates between fields.
	if !key.Matches(tab, km.Text.Next) {
		t.Error("Tab should still advance to the next field")
	}
	// Single-line Input fields keep the default Enter = next (only Text fields change).
	if !key.Matches(enter, km.Input.Next) {
		t.Error("Enter should still advance from single-line Input fields")
	}
}
