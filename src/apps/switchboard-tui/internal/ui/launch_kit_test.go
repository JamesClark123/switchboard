package ui

import (
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// miseKit is a second kit, so ordering across a multi-kit selection is testable.
func miseKit() *store.Kit {
	return &store.Kit{
		Name:        "mise",
		DisplayName: "Mise",
		Commands: &store.KitCommands{
			Install: []store.KitInstallCommand{{Command: "curl mise.run | sh"}},
		},
	}
}

// launchWithKits opens the launch wizard on a model that has saved kits.
func launchWithKits(t *testing.T, d *fakeDaemon) Model {
	t.Helper()
	m := kitModel(t, d, ruffKit(), miseKit())
	out, _ := update(m, press("n")) // open the launch wizard
	if out.screen != screenLaunch {
		t.Fatalf("screen = %v, want screenLaunch", out.screen)
	}
	return out
}

// Kits selected in the wizard must travel with the launch request: creation is the
// only point sbx accepts --kit.
func TestLaunchAttachesSelectedKits(t *testing.T) {
	d := &fakeDaemon{}
	out := launchWithKits(t, d)

	out, _ = update(out, press("K"))
	if !out.launch.kitPick {
		t.Fatal("K should open kit selection")
	}
	if len(out.launch.kitAll) != 2 {
		t.Fatalf("loaded %d kits, want 2", len(out.launch.kitAll))
	}
	if v := out.View(); !strings.Contains(v, "applied at creation") {
		t.Errorf("kit picker should explain when kits apply; got:\n%s", v)
	}

	out, _ = update(out, press(" ")) // select the first kit
	out, _ = update(out, press("enter"))
	if out.launch.kitPick {
		t.Error("enter should leave kit selection")
	}
	if len(out.launch.kitOrder) != 1 {
		t.Fatalf("selected %v, want one kit", out.launch.kitOrder)
	}
	// The chosen kits are visible before committing to the launch.
	if v := out.View(); !strings.Contains(v, "Kits: ") {
		t.Errorf("browser should show the chosen kits; got:\n%s", v)
	}

	refs, err := out.launch.selectedKitRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("selectedKitRefs = %d, want 1", len(refs))
	}
	if got := refs[0].GetSpec().GetSpecYaml(); !strings.Contains(got, "kind: mixin") {
		t.Errorf("kit ref should carry the rendered spec.yaml; got:\n%s", got)
	}
}

// sbx composes stacked kits in the order passed, so selection order must be kept
// and deselection must not corrupt it.
func TestLaunchKitSelectionPreservesOrder(t *testing.T) {
	out := launchWithKits(t, &fakeDaemon{})
	out, _ = update(out, press("K"))

	out, _ = update(out, press("j")) // move to the second kit
	out, _ = update(out, press(" ")) // select it first
	out, _ = update(out, press("k")) // back to the first
	out, _ = update(out, press(" ")) // select it second

	if len(out.launch.kitOrder) != 2 {
		t.Fatalf("kitOrder = %v, want 2 entries", out.launch.kitOrder)
	}
	second, first := out.launch.kitAll[1].ID(), out.launch.kitAll[0].ID()
	if out.launch.kitOrder[0] != second || out.launch.kitOrder[1] != first {
		t.Errorf("kitOrder = %v, want selection order [%s %s]", out.launch.kitOrder, second, first)
	}

	// Deselecting the first-selected kit leaves the other intact.
	out, _ = update(out, press(" "))
	if len(out.launch.kitOrder) != 1 || out.launch.kitOrder[0] != second {
		t.Errorf("after deselect kitOrder = %v, want [%s]", out.launch.kitOrder, second)
	}
}

func TestLaunchWithNoKitsSelected(t *testing.T) {
	out := launchWithKits(t, &fakeDaemon{})
	refs, err := out.launch.selectedKitRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("selectedKitRefs = %v, want none", refs)
	}
}

// With no kits saved, the picker points the user at where to make one.
func TestLaunchKitPickerEmpty(t *testing.T) {
	m := kitModel(t, &fakeDaemon{}) // no kits
	out, _ := update(m, press("n"))
	out, _ = update(out, press("K"))
	if !out.launch.kitPick {
		t.Fatal("K should still open kit selection")
	}
	if v := out.View(); !strings.Contains(v, "No kits saved") {
		t.Errorf("empty picker should explain how to create one; got:\n%s", v)
	}
	// Toggling with nothing to toggle is inert.
	out, _ = update(out, press(" "))
	if len(out.launch.kitOrder) != 0 {
		t.Error("toggling an empty list selected something")
	}
}

// Without a kit store, K is inert rather than a panic.
func TestLaunchKitPickWithoutStore(t *testing.T) {
	d := &fakeDaemon{}
	d.sandboxes = []*pb.Sandbox{refreshableSandbox()}
	m := withSandboxes(sized(New(d, "test")), d.sandboxes) // no WithKits
	out, _ := update(m, press("n"))
	out, _ = update(out, press("K"))
	if out.launch.kitPick {
		t.Error("kit selection should not open without a kit store")
	}
}

// The kit cursor must not run off either end.
func TestLaunchKitCursorBounds(t *testing.T) {
	out := launchWithKits(t, &fakeDaemon{})
	out, _ = update(out, press("K"))
	out, _ = update(out, press("k"))
	if out.launch.kitCursor != 0 {
		t.Errorf("cursor = %d, want it clamped at 0", out.launch.kitCursor)
	}
	for range 5 {
		out, _ = update(out, press("j"))
	}
	if out.launch.kitCursor != len(out.launch.kitAll)-1 {
		t.Errorf("cursor = %d, want it clamped at the last kit", out.launch.kitCursor)
	}
}

// Esc leaves kit selection without discarding the choices.
func TestLaunchKitPickEscKeepsSelection(t *testing.T) {
	out := launchWithKits(t, &fakeDaemon{})
	out, _ = update(out, press("K"))
	out, _ = update(out, press(" "))
	out, _ = update(out, press("esc"))
	if out.launch.kitPick {
		t.Error("esc should leave kit selection")
	}
	if len(out.launch.kitOrder) != 1 {
		t.Errorf("kitOrder = %v, want the selection kept", out.launch.kitOrder)
	}
}
