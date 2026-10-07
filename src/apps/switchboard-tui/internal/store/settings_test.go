package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsDefaultsWithoutFile(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings().Load()
	if err != nil || !got.AutoConnectHosts {
		t.Errorf("Load = %+v, %v; want defaults (auto-connect on) and no error", got, err)
	}
}

func TestSettingsRoundTripAndAbsentKeyKeepsDefault(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().Save(Settings{AutoConnectHosts: false}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir(), settingsName))
	if !strings.Contains(string(b), "auto_connect_hosts = false") {
		t.Errorf("settings.toml = %q", b)
	}
	got, err := s.Settings().Load()
	if err != nil || got.AutoConnectHosts {
		t.Errorf("Load after save = %+v, %v", got, err)
	}
	// A file that predates the key keeps the default.
	if err := os.WriteFile(filepath.Join(s.Dir(), settingsName), []byte("# nothing yet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(s.Dir(), settingsName), []byte("auto_connect_hosts = [not valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings().Load()
	if err == nil || !got.AutoConnectHosts {
		t.Errorf("corrupt file should yield defaults plus the error; got %+v, %v", got, err)
	}
}
