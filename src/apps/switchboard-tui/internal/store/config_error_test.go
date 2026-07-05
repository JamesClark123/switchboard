package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigGetAndListParseErrors(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cs := s.Configs()

	// A malformed config file makes Get (and List) surface a parse error.
	bad := filepath.Join(s.Dir(), "configs", "broken.toml")
	if err := os.WriteFile(bad, []byte("= nope ="), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Get("broken"); err == nil {
		t.Error("expected Get parse error for malformed config")
	}
	if _, err := cs.List(); err == nil {
		t.Error("expected List error when a config file is malformed")
	}
}

func TestConfigListEmptyWhenNoDir(t *testing.T) {
	// A Store whose configs dir was removed lists nothing without erroring.
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(s.Dir(), "configs")); err != nil {
		t.Fatal(err)
	}
	list, err := s.Configs().List()
	if err != nil {
		t.Fatalf("List on missing dir should be empty, got %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestConfigSaveExplicitID(t *testing.T) {
	cs := mustConfigs(t)
	saved, err := cs.Save(&Configuration{ID: "fixed-id", Name: "Name With Spaces"}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != "fixed-id" {
		t.Errorf("explicit ID should be preserved, got %q", saved.ID)
	}
}

func mustConfigs(t *testing.T) *ConfigStore {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s.Configs()
}
