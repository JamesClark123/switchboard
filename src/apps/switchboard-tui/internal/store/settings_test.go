package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSettings(t *testing.T, s *Store) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.Dir(), settingsName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeSettings(t *testing.T, s *Store, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir(), settingsName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boolPtr(v bool) *bool    { return &v }
func strPtr(v string) *string { return &v }

func TestSettingsDefaultsWithoutFile(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings().Load()
	if err != nil || !got.AutoConnectHosts || got.Editor != "" || got.EditorCustomCommand != "" {
		t.Errorf("Load = %+v, %v; want defaults (auto-connect on, no editor) and no error", got, err)
	}
}

func TestSettingsRoundTripAndAbsentKeyKeepsDefault(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().Update(SettingsChange{AutoConnectHosts: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	b := readSettings(t, s)
	if !strings.Contains(b, "auto_connect_hosts = false") {
		t.Errorf("settings.toml = %q", b)
	}
	// Absent keys stay absent after a toggle save (FR-530).
	if strings.Contains(b, "editor") {
		t.Errorf("a toggle save must not write the editor keys: %q", b)
	}
	got, err := s.Settings().Load()
	if err != nil || got.AutoConnectHosts {
		t.Errorf("Load after save = %+v, %v", got, err)
	}
	// A file that predates the key keeps the default.
	writeSettings(t, s, "# nothing yet\n")
	got, err = s.Settings().Load()
	if err != nil || !got.AutoConnectHosts {
		t.Errorf("absent key should keep the default; got %+v, %v", got, err)
	}
}

func TestSettingsCorruptFileYieldsDefaultsAndError(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSettings(t, s, "auto_connect_hosts = [not valid")
	got, err := s.Settings().Load()
	if err == nil || !got.AutoConnectHosts {
		t.Errorf("corrupt file should yield defaults plus the error; got %+v, %v", got, err)
	}
	// A wrong type is a parse error too (research F2).
	writeSettings(t, s, "editor = 5\n")
	if _, err := s.Settings().Load(); err == nil {
		t.Error("editor = 5 must be a parse error")
	}
}

// TestSettingsUnknownEditorSurvivesToggle: an unknown saved editor is loaded
// raw and a toggle save leaves it in the file (FR-508, FR-530).
func TestSettingsUnknownEditorSurvivesToggle(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSettings(t, s, "editor = 'future-ide'\n")
	got, err := s.Settings().Load()
	if err != nil || got.Editor != "future-ide" || !got.AutoConnectHosts {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if err := s.Settings().Update(SettingsChange{AutoConnectHosts: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	b := readSettings(t, s)
	if !strings.Contains(b, "editor = 'future-ide'") || !strings.Contains(b, "auto_connect_hosts = false") || strings.Contains(b, "custom_command") {
		t.Errorf("settings.toml = %q", b)
	}
	// Confirming VS Code replaces it.
	if err := s.Settings().Update(SettingsChange{Editor: strPtr("vscode")}); err != nil {
		t.Fatal(err)
	}
	if b := readSettings(t, s); !strings.Contains(b, "editor = 'vscode'") || !strings.Contains(b, "auto_connect_hosts = false") {
		t.Errorf("settings.toml = %q", b)
	}
}

// TestSettingsCustomSaveWritesBothKeys and the command round-trips raw,
// including a single quote and a tab.
func TestSettingsCustomSaveWritesBothKeys(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := `kitty -e sh -c 'cd "$1" && nvim .' sh {path}`
	if err := s.Settings().Update(SettingsChange{Editor: strPtr("custom"), EditorCustomCommand: strPtr(cmd)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings().Load()
	if err != nil || got.Editor != "custom" || got.EditorCustomCommand != cmd || !got.AutoConnectHosts {
		t.Errorf("Load = %+v, %v", got, err)
	}
	// A built-in editor's save keeps the command for a later Custom (FR-530).
	if err := s.Settings().Update(SettingsChange{Editor: strPtr("zed")}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Settings().Load()
	if got.Editor != "zed" || got.EditorCustomCommand != cmd {
		t.Errorf("after Zed: %+v", got)
	}
}

// TestSettingsHandEditSurvivesSave (SC-007): a key edited between Load and
// Update is still there after the save.
func TestSettingsHandEditSurvivesSave(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().Update(SettingsChange{Editor: strPtr("vscode")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settings().Load(); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, s, "editor = 'goland'\n# a comment\nunknown = 1\n")
	if err := s.Settings().Update(SettingsChange{AutoConnectHosts: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	b := readSettings(t, s)
	if !strings.Contains(b, "editor = 'goland'") || !strings.Contains(b, "auto_connect_hosts = false") {
		t.Errorf("settings.toml = %q", b)
	}
	// Comments and unknown keys aren't kept (FR-530).
	if strings.Contains(b, "comment") || strings.Contains(b, "unknown") {
		t.Errorf("comments and unknown keys must be dropped: %q", b)
	}
}

// TestSettingsBrokenFileRefusesSave (SC-006): a file that can't be parsed is
// never written; the error is *SettingsUnreadableError.
func TestSettingsBrokenFileRefusesSave(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const broken = "editor = 'custom'\neditor_custom_command = 'x {path}'\nauto_connect_hosts = [broken"
	writeSettings(t, s, broken)
	err = s.Settings().Update(SettingsChange{AutoConnectHosts: boolPtr(false)})
	var ue *SettingsUnreadableError
	if !errors.As(err, &ue) || ue.Unwrap() == nil || !strings.Contains(ue.Error(), "couldn't be read") {
		t.Fatalf("Update on a broken file = %v, want *SettingsUnreadableError", err)
	}
	if b := readSettings(t, s); b != broken {
		t.Errorf("the broken file was changed: %q", b)
	}
	// An unreadable (not just unparseable) file: a directory in its place.
	s2, _ := New(t.TempDir())
	if err := os.Mkdir(filepath.Join(s2.Dir(), settingsName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s2.Settings().Update(SettingsChange{Editor: strPtr("zed")}); !errors.As(err, &ue) {
		t.Errorf("Update on an unreadable file = %v", err)
	}
}

// TestSettingsOlderFile (Edge case): a file written before this feature gives
// VS Code and the saved toggle; unknown keys from a newer version are read fine.
func TestSettingsOlderFile(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSettings(t, s, "auto_connect_hosts = false\n")
	got, err := s.Settings().Load()
	if err != nil || got.AutoConnectHosts || got.Editor != "" || got.EditorCustomCommand != "" {
		t.Errorf("older file: %+v, %v", got, err)
	}
	writeSettings(t, s, "auto_connect_hosts = false\neditor = 'zed'\nfuture_key = 'x'\n[future]\nk = 1\n")
	got, err = s.Settings().Load()
	if err != nil || got.Editor != "zed" {
		t.Errorf("newer file: %+v, %v", got, err)
	}
}
