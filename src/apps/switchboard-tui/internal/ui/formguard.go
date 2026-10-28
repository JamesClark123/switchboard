package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
)

// formKeyMap is the one keymap every huh form in the app uses (SWI-9 R1).
//
// Enter inserts a newline in multi-line Text fields (subcommands, workspaces,
// allowed/denied domains, env vars, … — one list entry per line) instead of
// advancing to the next field. huh's default binds Enter to "next" and puts
// newline on alt+enter/ctrl+j, which is unintuitive for these list fields. Tab
// still moves between fields and ctrl+s applies the form, so Enter is freed up
// for newlines. Enter is removed from Text.Next/Submit because huh dispatches
// those before the textarea's newline, and the textarea's newline key is driven
// from Text.NewLine. Single-line Input/Confirm fields keep huh's default (Enter
// advances).
//
// Text.Editor is keyless: huh's Text.Update matches keymap.Editor whatever
// ExternalEditor is set to (ExternalEditor(false) only hides the binding when
// help renders), so any key left on it would still launch $EDITOR on a form that
// hides help. ctrl+e opens the in-app enlarged view instead.
func formKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Text.NewLine = key.NewBinding(key.WithKeys("enter", "alt+enter", "ctrl+j"), key.WithHelp("enter", "new line"))
	km.Text.Next = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next"))
	km.Text.Prev = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	km.Text.Submit = key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "apply"))
	km.Text.Editor = key.NewBinding()
	return km
}

// textEntry is what a formSpec remembers about one multi-line Text field: the
// pointer it is bound to, its title (for the enlarged view's border), and the
// value it was built with (for the normalisation notice).
type textEntry struct {
	ptr      *string
	title    string
	original string
}

// formSpec is the registry of a form's multi-line Text fields. Every Text in the
// app is built through spec.text so the enlarged view can find the focused
// field's pointer, and no Text can bring the external editor back.
type formSpec struct {
	texts map[*huh.Text]textEntry
}

func newFormSpec() *formSpec {
	return &formSpec{texts: map[*huh.Text]textEntry{}}
}

// text builds a multi-line field bound to ptr and records it. CRLF line endings
// are folded to LF first: the textarea maps each \r and \n to a newline, so CRLF
// content would otherwise come back with every line doubled.
func (s *formSpec) text(ptr *string, title string) *huh.Text {
	original := *ptr
	*ptr = strings.ReplaceAll(*ptr, "\r\n", "\n")
	t := huh.NewText().ExternalEditor(false).Title(title).Value(ptr)
	s.texts[t] = textEntry{ptr: ptr, title: title, original: original}
	return t
}

// lookup reports whether field is one of this spec's Text fields.
func (s *formSpec) lookup(field huh.Field) (*huh.Text, textEntry, bool) {
	if s == nil {
		return nil, textEntry{}, false
	}
	t, ok := field.(*huh.Text)
	if !ok {
		return nil, textEntry{}, false
	}
	e, ok := s.texts[t]
	return t, e, ok
}

// normalize pushes each Text's cleaned textarea value back into its pointer
// (huh only writes the pointer on Update), so a baseline taken afterwards
// matches what a later tab-past-without-editing writes. It returns the titles
// of fields whose value differs from what they were built with, so the surface
// can tell the user the text will be rewritten when saved.
func (s *formSpec) normalize() []string {
	var changed []string
	for t, e := range s.texts {
		t.Update(nil)
		if *e.ptr != e.original {
			changed = append(changed, e.title)
		}
	}
	return changed
}

// normalizeNotice is the status shown when normalize reports rewritten fields.
func normalizeNotice(titles []string) string {
	if len(titles) == 0 {
		return ""
	}
	return strings.Join(titles, ", ") + ": line endings/tabs/control characters will be rewritten when saved"
}

// snapshotOpVals copies an op form's bound values so later edits can be compared
// against it. Values are copied, never aliased: the vals are live pointers huh
// writes to.
func snapshotOpVals(vals any) any {
	switch v := vals.(type) {
	case *actionFormVals:
		return *v
	case *eventFormVals:
		return *v
	case *markFreeVals:
		return *v
	case *callFormVals:
		return flattenCallVals(v)
	}
	return nil
}

// flattenCallVals turns a call form's per-argument pointers into plain values.
func flattenCallVals(v *callFormVals) map[string]string {
	out := make(map[string]string, len(v.values))
	for k, p := range v.values {
		out[k] = *p
	}
	return out
}
