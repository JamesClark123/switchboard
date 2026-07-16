package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// The help footer must describe the level the user is actually on.
func TestKitEditorHelpPerLevel(t *testing.T) {
	out := editorOn(t, ruffKit())
	if got := helpText(out.kitEditorHelp()); !strings.Contains(got, "validate") {
		t.Errorf("section-list help = %q, want the save/validate keys", got)
	}
	out.kitEditor.section = secInstall
	out, _ = update(out, press("enter"))
	if got := helpText(out.kitEditorHelp()); !strings.Contains(got, "add") {
		t.Errorf("item-list help = %q, want the add/delete keys", got)
	}
	out = pressCmd(out, "a")
	if got := helpText(out.kitEditorHelp()); !strings.Contains(got, "apply") {
		t.Errorf("form help = %q, want the apply key", got)
	}
}

// pump applies msg and then feeds back the messages its commands produce, a few
// rounds deep. huh advances its own state via returned commands; bubbletea runs
// them in the real app, so a test that only calls Update sees a half-advanced form.
func pump(m Model, msg tea.Msg) Model {
	m, cmd := update(m, msg)
	for range 4 {
		if cmd == nil {
			break
		}
		next := runCmd(cmd)
		if next == nil {
			break
		}
		m, cmd = update(m, next)
	}
	return m
}

func helpText(hb helpBindings) string {
	var b strings.Builder
	for _, k := range hb {
		b.WriteString(k.Help().Key + " " + k.Help().Desc + " ")
	}
	return b.String()
}

// A form completed by tabbing through (huh's own StateCompleted) must apply, not
// just ctrl+s.
func TestKitEditorFormCompletionApplies(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out.kitEditor.section = secInstall
	out, _ = update(out, press("enter"))
	out = pressCmd(out, "a")
	out = typeIn(out, "echo hi")
	// Tab through user + description to the last field, then submit — huh reaches
	// StateCompleted, which advanceKitForm turns into an apply. huh drives its own
	// state through returned commands, so they must be pumped (bubbletea does this
	// in the real app).
	out = pump(out, tea.KeyMsg{Type: tea.KeyTab})
	out = pump(out, tea.KeyMsg{Type: tea.KeyTab})
	out = pump(out, tea.KeyMsg{Type: tea.KeyEnter})
	if out.kitEditor.form != nil {
		t.Fatal("completing the form should close it")
	}
	got := out.kitEditor.kit.Commands.Install
	if len(got) != 1 || got[0].Command != "echo hi" {
		t.Errorf("install = %+v, want the typed command applied on completion", got)
	}
}

// validateKit needs a daemon; without one it must explain rather than panic.
func TestValidateKitWithoutDaemon(t *testing.T) {
	out := editorOn(t, ruffKit())
	out.daemon = nil
	out, cmd := update(out, press("v"))
	if cmd != nil {
		runCmd(cmd)
	}
	if !strings.Contains(out.kitEditor.status, "no daemon") {
		t.Errorf("status = %q, want an explanation", out.kitEditor.status)
	}
}

// A nameless kit cannot be rendered, so validation must refuse up front.
func TestValidateKitWithoutNameRefuses(t *testing.T) {
	out := editorOn(t, ruffKit())
	out.kitEditor.kit.Name = ""
	out, cmd := update(out, press("v"))
	if cmd != nil {
		runCmd(cmd)
	}
	if !strings.Contains(out.kitEditor.status, "name is required") {
		t.Errorf("status = %q, want a name-required message", out.kitEditor.status)
	}
}

// A transport failure during validation is reported as a diagnostic, not lost.
func TestValidateKitTransportErrorShown(t *testing.T) {
	d := &fakeDaemon{validateErr: errBoom{}}
	m := kitModel(t, d, ruffKit())
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))
	out, cmd = update(out, press("v"))
	out, _ = update(out, runCmd(cmd))
	if out.kitEditor.validOK {
		t.Error("a transport failure must not report the kit valid")
	}
	if v := out.View(); !strings.Contains(v, "boom") {
		t.Errorf("view should surface the error; got:\n%s", v)
	}
}

// Esc on the editor's section list returns to the picker and reloads it.
func TestKitEditorEscReloadsPicker(t *testing.T) {
	out := editorOn(t, ruffKit())
	out, cmd := update(out, press("esc"))
	if out.screen != screenKitPicker {
		t.Fatalf("screen = %v, want the picker", out.screen)
	}
	if cmd == nil {
		t.Fatal("expected the picker to reload its kits")
	}
	out, _ = update(out, runCmd(cmd))
	if len(out.kitPicker.kits) != 1 {
		t.Errorf("picker reloaded %d kits, want 1", len(out.kitPicker.kits))
	}
}

// An unknown key on the editor's section list is inert.
func TestKitEditorIgnoresUnknownKeys(t *testing.T) {
	out := editorOn(t, ruffKit())
	before := out.kitEditor.section
	out, _ = update(out, press("z"))
	if out.kitEditor.section != before || out.screen != screenKitEditor {
		t.Error("an unknown key should be inert on the section list")
	}
}

// An unknown key inside an item list is inert too.
func TestKitSectionIgnoresUnknownKeys(t *testing.T) {
	out := openSection(t, ruffKit(), secInstall)
	out, _ = update(out, press("z"))
	if !out.kitEditor.inSection {
		t.Error("an unknown key should not leave the section")
	}
}

// Enter on an empty itemized section opens the add form (nothing to edit).
func TestKitSectionEnterOnEmptyAdds(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secInstall)
	out = pressCmd(out, "enter")
	if out.kitEditor.form == nil {
		t.Fatal("enter on an empty section should open the add form")
	}
	if out.kitEditor.formItem != -1 {
		t.Errorf("formItem = %d, want -1 (appending)", out.kitEditor.formItem)
	}
}

// Aborting a form via huh (ctrl+c inside the form) closes it without applying.
func TestKitEditorFormAbort(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secInstall)
	out = pressCmd(out, "a")
	out = typeIn(out, "discard me")
	out, _ = update(out, tea.KeyMsg{Type: tea.KeyCtrlC})
	if out.kitEditor.form != nil {
		t.Error("aborting should close the form")
	}
	if out.kitEditor.kit.Commands != nil && len(out.kitEditor.kit.Commands.Install) != 0 {
		t.Error("an aborted form must not apply")
	}
}

// applyKitForm with no form open is a no-op rather than a nil dereference.
func TestApplyKitFormWithoutForm(t *testing.T) {
	out := editorOn(t, ruffKit())
	got, cmd := out.applyKitForm()
	if cmd != nil {
		t.Error("expected no command")
	}
	if got.(Model).kitEditor.form != nil {
		t.Error("expected no form")
	}
}
