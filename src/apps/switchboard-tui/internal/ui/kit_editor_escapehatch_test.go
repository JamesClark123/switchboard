package ui

import (
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// navToEscapeHatch drills the editor into the escape-hatch section.
func navToEscapeHatch(t *testing.T, out Model) Model {
	t.Helper()
	for out.kitEditor.section != secEscapeHatch {
		out, _ = update(out, press("j"))
	}
	out, _ = update(out, press("enter"))
	if !out.kitEditor.inSection {
		t.Fatal("enter should drill into the escape-hatch section")
	}
	return out
}

// openEHForm opens the add/edit form and returns the model with the form live.
func openEHForm(t *testing.T, out Model, idx int) Model {
	t.Helper()
	key := "a"
	if idx >= 0 {
		key = "enter"
	}
	out = pressCmd(out, key)
	if out.kitEditor.form == nil {
		t.Fatal("expected the escape-hatch item form to open")
	}
	return out
}

// setEHVals fills the bound form values directly, then applies the form. Driving a
// six-field huh form (including a Confirm) keystroke-by-keystroke is brittle; the
// bound-pointer path is exactly what applyKitForm reads, so this exercises the real
// apply logic (the same values huh would have written).
func setEHVals(out Model, c store.KitEscapeHatchCommand) Model {
	v := out.kitEditor.vals
	v.ehName = c.Name
	v.itemCommand = c.Command
	v.ehWhenToUse = c.WhenToUse
	v.ehRequiresApproval = c.RequiresApproval
	v.ehWorkingDir = c.WorkingDir
	v.ehSubcommands = strings.Join(c.Subcommands, "\n")
	v.ehArgsPattern = c.ArgsPattern
	v.ehWorkspaces = strings.Join(c.Workspaces, "\n")
	if c.MaxDurationSecs > 0 {
		v.ehMaxDuration = "600"
	}
	out, _ = update(out, ctrlS())
	return out
}

func TestKitEditorEscapeHatchSectionListed(t *testing.T) {
	out := editorOn(t, ruffKit())
	if v := out.View(); !strings.Contains(v, "Escape-hatch commands") {
		t.Errorf("section list should include the escape-hatch section; got:\n%s", v)
	}
}

func TestKitEditorAddEscapeHatchCommand(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out = navToEscapeHatch(t, out)
	out = openEHForm(t, out, -1)
	if out.kitEditor.formItem != -1 {
		t.Errorf("formItem = %d, want -1 (appending)", out.kitEditor.formItem)
	}
	out = setEHVals(out, store.KitEscapeHatchCommand{
		Name: "install-deps", Command: "pnpm install", WhenToUse: "when deps change",
	})
	if out.kitEditor.form != nil {
		t.Error("ctrl+s should close the item form")
	}
	got := out.kitEditor.kit.EscapeHatch
	if len(got) != 1 || got[0].Name != "install-deps" || got[0].Command != "pnpm install" {
		t.Fatalf("escape-hatch commands = %+v, want the entered command", got)
	}
	if got[0].RequiresApproval {
		t.Error("default consent should be auto-run (requiresApproval false)")
	}
}

func TestKitEditorAddApprovalGatedCommand(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out = navToEscapeHatch(t, out)
	out = openEHForm(t, out, -1)
	out = setEHVals(out, store.KitEscapeHatchCommand{
		Name: "deploy", Command: "./deploy.sh", WhenToUse: "to ship", RequiresApproval: true,
	})
	got := out.kitEditor.kit.EscapeHatch
	if len(got) != 1 || !got[0].RequiresApproval {
		t.Fatalf("command should be approval-gated: %+v", got)
	}
}

func TestKitEditorAddCommandWithSubcommandsAndWorkspaces(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out = navToEscapeHatch(t, out)
	out = openEHForm(t, out, -1)
	out = setEHVals(out, store.KitEscapeHatchCommand{
		Name: "pnpm", Command: "pnpm", WhenToUse: "run scripts",
		Subcommands: []string{"install", "dev"},
		Workspaces:  []string{"src/apps/*", "packages/shared"},
	})
	got := out.kitEditor.kit.EscapeHatch
	if len(got) != 1 {
		t.Fatalf("want 1 command, got %d", len(got))
	}
	if len(got[0].Subcommands) != 2 || got[0].Subcommands[1] != "dev" {
		t.Errorf("subcommands not applied through the form: %v", got[0].Subcommands)
	}
	if len(got[0].Workspaces) != 2 || got[0].Workspaces[0] != "src/apps/*" {
		t.Errorf("workspaces not applied through the form: %v", got[0].Workspaces)
	}
}

func TestKitEditorRejectsBlankEscapeHatch(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out = navToEscapeHatch(t, out)
	out = openEHForm(t, out, -1)
	out = setEHVals(out, store.KitEscapeHatchCommand{Name: "", Command: ""})
	if !strings.Contains(out.kitEditor.status, "required") {
		t.Errorf("status = %q, want a required-field message", out.kitEditor.status)
	}
	if len(out.kitEditor.kit.EscapeHatch) != 0 {
		t.Error("a blank command must not be appended")
	}
}

func TestKitEditorEditEscapeHatchInPlace(t *testing.T) {
	kit := &store.Kit{Name: "k", EscapeHatch: []store.KitEscapeHatchCommand{
		{Name: "install-deps", Command: "pnpm install", WhenToUse: "x"},
	}}
	out := editorOn(t, kit)
	out = navToEscapeHatch(t, out)
	out = openEHForm(t, out, 0)
	if out.kitEditor.formItem != 0 {
		t.Fatalf("formItem = %d, want 0 (editing)", out.kitEditor.formItem)
	}
	out = setEHVals(out, store.KitEscapeHatchCommand{Name: "install-deps", Command: "npm ci", WhenToUse: "x"})
	got := out.kitEditor.kit.EscapeHatch
	if len(got) != 1 || got[0].Command != "npm ci" {
		t.Fatalf("edit should replace in place: %+v", got)
	}
}

func TestKitEditorDeleteEscapeHatch(t *testing.T) {
	kit := &store.Kit{Name: "k", EscapeHatch: []store.KitEscapeHatchCommand{
		{Name: "a", Command: "cmd-a", WhenToUse: "x"},
		{Name: "b", Command: "cmd-b", WhenToUse: "y"},
	}}
	out := editorOn(t, kit)
	out = navToEscapeHatch(t, out)
	out, _ = update(out, press("d")) // delete the first item
	got := out.kitEditor.kit.EscapeHatch
	if len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("delete should remove the highlighted item: %+v", got)
	}
}

// An abandoned edit must not mutate the stored kit or its sidecar (US1).
func TestKitEditorAbandonedEscapeHatchEditLeavesStoreUntouched(t *testing.T) {
	kit := &store.Kit{Name: "ruff", Commands: &store.KitCommands{Install: []store.KitInstallCommand{{Command: "pip install ruff"}}}}
	out := editorOn(t, kit)
	out = navToEscapeHatch(t, out)
	out = openEHForm(t, out, -1)
	out = setEHVals(out, store.KitEscapeHatchCommand{Name: "sneaky", Command: "rm -rf /", WhenToUse: "never"})
	// The in-memory editor kit changed...
	if len(out.kitEditor.kit.EscapeHatch) != 1 {
		t.Fatal("precondition: the edit should be in the editor's working copy")
	}
	// ...but without a save, the stored kit is untouched.
	stored, err := out.kits.Get("ruff")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.EscapeHatch) != 0 {
		t.Errorf("abandoned edit leaked into the stored kit: %+v", stored.EscapeHatch)
	}
}
