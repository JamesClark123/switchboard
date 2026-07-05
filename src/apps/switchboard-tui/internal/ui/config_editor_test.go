package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func testManifest() *pb.OptionManifest {
	return &pb.OptionManifest{
		SbxVersion: "1.0",
		Options: []*pb.OptionManifest_Option{
			{Key: "cpus", Type: "int", Description: "cpu count"},
			{Key: "network", Type: "string", Description: "net mode"},
			{Key: "privileged", Type: "bool", Description: "run privileged"},
		},
	}
}

func newConfigStore(t *testing.T) *store.ConfigStore {
	t.Helper()
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s.Configs()
}

// typeStr feeds a string one rune at a time (works for bubbles textinput/textarea
// and huh inputs alike).
func typeStr(m Model, s string) Model {
	for _, r := range s {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// TestConfigEditorCreateAndSave drives the huh editor end-to-end: name it, walk
// the option fields setting one of each type, then let the form complete → save.
func TestConfigEditorCreateAndSave(t *testing.T) {
	d := &fakeDaemon{manifest: testManifest()}
	cs := newConfigStore(t)
	tm := teatest.NewTestModel(t, New(d, "/work").WithConfigs(cs), teatest.WithInitialTermSize(90, 40))
	step()

	tm.Send(press("c")) // open editor; manifest loads and builds the form
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("myconfig")}) // name
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // -> seeding mode (Select)
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // keep "duplicate" -> cpus
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // -> network
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("host")})
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // -> privileged (Confirm)
	step()
	tm.Send(press("y")) // accept -> completes the form -> save
	step()
	step()

	_ = fullOutput(t, tm)

	got, err := cs.Get("myconfig")
	if err != nil {
		t.Fatalf("config not saved: %v", err)
	}
	if got.SeedingMode != "duplicate" {
		t.Errorf("seeding mode = %q", got.SeedingMode)
	}
	if got.KitOptions["cpus"] != "4" {
		t.Errorf("cpus = %q, want 4", got.KitOptions["cpus"])
	}
	if got.KitOptions["network"] != `"host"` {
		t.Errorf("network = %q, want JSON-quoted host", got.KitOptions["network"])
	}
	if got.KitOptions["privileged"] != "true" {
		t.Errorf("privileged = %q, want true", got.KitOptions["privileged"])
	}
}

func TestConfigEditorSaveRequiresName(t *testing.T) {
	d := &fakeDaemon{manifest: testManifest()}
	m := sized(New(d, "/work").WithConfigs(newConfigStore(t)))
	m, cmd := update(m, press("c"))
	if m.screen != screenConfigEditor {
		t.Fatal("c should open the config editor")
	}
	m, _ = update(m, runCmd(cmd)) // manifestMsg -> builds form
	if len(m.editor.options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(m.editor.options))
	}
	if !strings.Contains(m.View(), "network") {
		t.Error("editor should render manifest option titles (FR-014)")
	}

	// Ctrl+S with no name stays in the editor with a hint.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.screen != screenConfigEditor {
		t.Error("save without a name should stay in the editor")
	}
	if !strings.Contains(m.editor.status, "name required") {
		t.Errorf("status = %q", m.editor.status)
	}
	// Esc returns to the list.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should return to list")
	}
}

func TestConfigEditorLoadingAndNoOptions(t *testing.T) {
	// Loading state before the manifest arrives.
	m := sized(New(&fakeDaemon{manifest: testManifest()}, "/work").WithConfigs(newConfigStore(t)))
	m, _ = update(m, press("c"))
	if !strings.Contains(m.View(), "loading option manifest") {
		t.Error("editor should render a loading state")
	}

	// A host advertising no options still renders the editor.
	m2 := sized(New(&fakeDaemon{manifest: &pb.OptionManifest{}}, "/work").WithConfigs(newConfigStore(t)))
	m2, cmd := update(m2, press("c"))
	m2, _ = update(m2, runCmd(cmd))
	if !strings.Contains(m2.View(), "no sbx options") {
		t.Error("editor should note when the host advertised no options")
	}
}

func TestConfigPickerLaunchUsesSnapshot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{}
	cs := newConfigStore(t)
	if _, err := cs.Save(&store.Configuration{
		Name:       "saved-cfg",
		KitOptions: map[string]string{"network": `"host"`},
	}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}

	tm := teatest.NewTestModel(t, New(d, root).WithConfigs(cs), teatest.WithInitialTermSize(100, 32))
	step()
	tm.Send(press("C")) // open picker
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // launch from the saved config -> overlay
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeySpace}) // select "proj"
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // launch
	step()
	step()

	out := fullOutput(t, tm)
	assertContains(t, out, "saved-cfg")

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastConfig == nil || d.lastConfig.GetName() != "saved-cfg" {
		t.Fatalf("launch did not use the saved config snapshot: %+v", d.lastConfig)
	}
	if d.lastConfig.GetKitOptions()["network"] != `"host"` {
		t.Errorf("frozen kit options missing: %v", d.lastConfig.GetKitOptions())
	}
}

func TestConfigPickerDeleteAndEmpty(t *testing.T) {
	d := &fakeDaemon{}
	cs := newConfigStore(t)
	if _, err := cs.Save(&store.Configuration{Name: "todelete"}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	m := sized(New(d, "/work").WithConfigs(cs))
	m, cmd := update(m, press("C"))
	if m.screen != screenConfigPicker {
		t.Fatal("C should open the picker")
	}
	m, _ = update(m, runCmd(cmd)) // configsMsg
	if len(m.picker.configs) != 1 || m.pickerCurrent() == nil {
		t.Fatalf("expected 1 config, got %d", len(m.picker.configs))
	}
	if !strings.Contains(m.viewPicker(), "todelete") {
		t.Error("picker should render config names")
	}
	// Delete it.
	m, cmd = update(m, press("d"))
	m, _ = update(m, runCmd(cmd)) // reload -> configsMsg
	if len(m.picker.configs) != 0 || m.pickerCurrent() != nil {
		t.Errorf("config not deleted: %d remain", len(m.picker.configs))
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should return to list")
	}
}

func TestConfigFeaturesNoOpWithoutStore(t *testing.T) {
	m := New(&fakeDaemon{}, "/work") // no WithConfigs
	m, _ = update(m, press("c"))
	if m.screen != screenList || !strings.Contains(m.status, "no config store") {
		t.Errorf("editor without store should warn; status=%q screen=%v", m.status, m.screen)
	}
	m, _ = update(m, press("C"))
	if m.screen != screenList {
		t.Error("picker without store should stay on list")
	}
}

func TestEncodeDecodeOption(t *testing.T) {
	if encodeOption("int", "4") != "4" {
		t.Error("int should encode bare")
	}
	if encodeOption("int", "x") != `"x"` {
		t.Error("non-numeric int falls back to JSON string")
	}
	if encodeOption("bool", "true") != "true" {
		t.Error("bool true")
	}
	if encodeOption("bool", "garbage") != "false" {
		t.Error("invalid bool defaults false")
	}
	if encodeOption("string", "hi") != `"hi"` {
		t.Error("string JSON-quoted")
	}
}
