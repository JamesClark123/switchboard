package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func ctrlS() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlS} }

// kitModel returns a sized list model with a kit store containing kits.
func kitModel(t *testing.T, d *fakeDaemon, kits ...*store.Kit) Model {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ks := st.Kits()
	for _, k := range kits {
		if _, err := ks.Save(k); err != nil {
			t.Fatal(err)
		}
	}
	d.sandboxes = []*pb.Sandbox{refreshableSandbox()}
	m := withSandboxes(sized(New(d, "test")), d.sandboxes).WithKits(ks)
	return m
}

func ruffKit() *store.Kit {
	return &store.Kit{
		Name:        "ruff",
		DisplayName: "Ruff",
		Commands: &store.KitCommands{
			Install: []store.KitInstallCommand{{Command: "pip install ruff"}},
		},
	}
}

func TestKitsKeyOpensPicker(t *testing.T) {
	m := kitModel(t, &fakeDaemon{}, ruffKit())
	out, cmd := update(m, press("K"))
	if out.screen != screenKitPicker {
		t.Fatalf("screen = %v, want screenKitPicker", out.screen)
	}
	// The picker loads kits asynchronously.
	out, _ = update(out, runCmd(cmd))
	if len(out.kitPicker.kits) != 1 {
		t.Fatalf("picker loaded %d kits, want 1", len(out.kitPicker.kits))
	}
	if v := out.View(); !strings.Contains(v, "Ruff") {
		t.Errorf("picker should list the kit; got:\n%s", v)
	}
}

// "n" in the picker creates a kit; the editor opens blank.
func TestNewKitOpensEmptyEditor(t *testing.T) {
	m := kitModel(t, &fakeDaemon{})
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("n"))
	if out.screen != screenKitEditor {
		t.Fatalf("screen = %v, want screenKitEditor", out.screen)
	}
	if out.kitEditor.editing != "" || out.kitEditor.kit.Name != "" {
		t.Errorf("new kit editor should start blank, got %+v", out.kitEditor.kit)
	}
	if v := out.View(); !strings.Contains(v, "New kit") || !strings.Contains(v, "Install commands") {
		t.Errorf("editor should show its sections; got:\n%s", v)
	}
}

// Enter on a kit opens it for update, prefilled — the create/update distinction the
// config editor lacks.
func TestEnterOpensKitForUpdatePrefilled(t *testing.T) {
	m := kitModel(t, &fakeDaemon{}, ruffKit())
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))
	if out.screen != screenKitEditor {
		t.Fatalf("screen = %v, want screenKitEditor", out.screen)
	}
	if out.kitEditor.editing != "ruff" {
		t.Errorf("editing = %q, want the existing kit id", out.kitEditor.editing)
	}
	if got := out.kitEditor.kit.Commands.Install[0].Command; got != "pip install ruff" {
		t.Errorf("editor not prefilled: install[0] = %q", got)
	}
	if v := out.View(); !strings.Contains(v, "Edit kit ruff") {
		t.Errorf("editor should show it is updating; got:\n%s", v)
	}
}

// Abandoning an edit must not mutate the stored kit — the editor holds a copy.
func TestAbandonedEditDoesNotMutateStoredKit(t *testing.T) {
	m := kitModel(t, &fakeDaemon{}, ruffKit())
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))

	out.kitEditor.kit.Name = "clobbered"
	out.kitEditor.kit.Commands.Install[0].Command = "rm -rf /"

	stored, err := out.kits.Get("ruff")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "ruff" || stored.Commands.Install[0].Command != "pip install ruff" {
		t.Errorf("stored kit was mutated by an in-memory edit: %+v", stored)
	}
}

func TestDeleteKitFromPicker(t *testing.T) {
	m := kitModel(t, &fakeDaemon{}, ruffKit())
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, cmd = update(out, press("d"))
	if cmd != nil {
		out, _ = update(out, runCmd(cmd))
	}
	if _, err := out.kits.Get("ruff"); err != store.ErrKitNotFound {
		t.Errorf("kit should be deleted, Get = %v", err)
	}
}

// Saving writes the kit and returns to the picker.
func TestSaveKitRequiresNameThenWrites(t *testing.T) {
	m := kitModel(t, &fakeDaemon{})
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("n"))

	// ctrl+s with no name must refuse rather than write a nameless kit.
	out, _ = update(out, ctrlS())
	if out.screen != screenKitEditor {
		t.Fatalf("save without a name should stay in the editor, got %v", out.screen)
	}
	if !strings.Contains(out.kitEditor.status, "name required") {
		t.Errorf("status = %q, want a name-required message", out.kitEditor.status)
	}

	out.kitEditor.kit.Name = "My Kit"
	out, cmd = update(out, ctrlS())
	if out.screen != screenKitPicker {
		t.Errorf("screen = %v, want a return to the picker after save", out.screen)
	}
	if cmd != nil {
		runCmd(cmd)
	}
	saved, err := out.kits.Get("my-kit")
	if err != nil {
		t.Fatalf("kit not saved under its slug: %v", err)
	}
	if saved.Kind != "mixin" || saved.SchemaVersion != "1" {
		t.Errorf("saved kit missing required identity: %+v", saved)
	}
}

// "A" on a sandbox row scopes the picker to attaching.
func TestAddKitKeyOpensAttachPicker(t *testing.T) {
	m := kitModel(t, &fakeDaemon{}, ruffKit())
	out, cmd := update(m, press("A"))
	if out.screen != screenKitPicker {
		t.Fatalf("screen = %v, want screenKitPicker", out.screen)
	}
	if out.kitPicker.attachTo != "sb-1" {
		t.Errorf("attachTo = %q, want the selected sandbox", out.kitPicker.attachTo)
	}
	out, _ = update(out, runCmd(cmd))
	if v := out.View(); !strings.Contains(v, "Attach kit to feature-work") {
		t.Errorf("picker should show the attach target; got:\n%s", v)
	}
}

// Attaching is destructive-ish (it restarts the sandbox), so it is gated too.
func TestAttachKitIsConfirmedThenSendsSpec(t *testing.T) {
	d := &fakeDaemon{}
	m := kitModel(t, d, ruffKit())
	out, cmd := update(m, press("A"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))
	if out.screen != screenConfirm {
		t.Fatalf("screen = %v, want a confirmation before attaching", out.screen)
	}
	if v := out.View(); !strings.Contains(v, "restarts") {
		t.Errorf("confirmation should warn about the restart; got:\n%s", v)
	}
	if d.addKitID != "" {
		t.Fatal("attach fired before confirmation")
	}

	out, cmd = update(out, press("y"))
	runCmd(cmd)
	if d.addKitID != "sb-1" {
		t.Errorf("attached to %q, want sb-1", d.addKitID)
	}
	// The kit travels as an inline spec the daemon materializes, not a bare name.
	spec := d.addKitRef.GetSpec()
	if spec.GetId() != "ruff" {
		t.Errorf("kit ref id = %q, want ruff", spec.GetId())
	}
	if !strings.Contains(spec.GetSpecYaml(), "pip install ruff") {
		t.Errorf("kit ref should carry the rendered spec.yaml; got:\n%s", spec.GetSpecYaml())
	}
}

func TestAttachKitCancelDoesNotSend(t *testing.T) {
	d := &fakeDaemon{}
	m := kitModel(t, d, ruffKit())
	out, cmd := update(m, press("A"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))
	_, cmd = update(out, press("n"))
	if cmd != nil {
		runCmd(cmd)
	}
	if d.addKitID != "" {
		t.Error("cancelled attach still called the daemon")
	}
}

// Validation goes to the host sbx, and its diagnostics are shown verbatim.
func TestValidateKitShowsDaemonDiagnostics(t *testing.T) {
	d := &fakeDaemon{validateResp: &pb.ValidateKitResponse{Ok: false, Errors: []string{"spec.yaml: unknown field 'foo'"}}}
	m := kitModel(t, d, ruffKit())
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))

	out, cmd = update(out, press("v"))
	if !out.kitEditor.validating {
		t.Error("expected the editor to show it is validating")
	}
	out, _ = update(out, runCmd(cmd))
	if out.kitEditor.validOK {
		t.Error("validOK should be false for a rejected kit")
	}
	if v := out.View(); !strings.Contains(v, "unknown field 'foo'") {
		t.Errorf("editor should surface sbx's diagnostics; got:\n%s", v)
	}
	if d.validatedSpec.GetId() != "ruff" {
		t.Errorf("validated %q, want the edited kit", d.validatedSpec.GetId())
	}
}

func TestValidateKitOK(t *testing.T) {
	d := &fakeDaemon{validateResp: &pb.ValidateKitResponse{Ok: true}}
	m := kitModel(t, d, ruffKit())
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))
	out, cmd = update(out, press("v"))
	out, _ = update(out, runCmd(cmd))
	if !out.kitEditor.validOK {
		t.Error("expected a valid kit to report OK")
	}
	if v := out.View(); !strings.Contains(v, "kit is valid") {
		t.Errorf("editor should confirm validity; got:\n%s", v)
	}
}

// Without a kit store the feature degrades to a message rather than panicking.
func TestKitsWithoutStore(t *testing.T) {
	d := &fakeDaemon{}
	d.sandboxes = []*pb.Sandbox{refreshableSandbox()}
	m := withSandboxes(sized(New(d, "test")), d.sandboxes)
	out, _ := update(m, press("K"))
	if out.screen != screenList {
		t.Errorf("screen = %v, want the list", out.screen)
	}
	if !strings.Contains(out.status, "no kit store") {
		t.Errorf("status = %q, want an explanation", out.status)
	}
}
