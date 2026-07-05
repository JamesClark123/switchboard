package store

import (
	"path/filepath"
	"testing"
)

type sample struct {
	Name  string `toml:"name"`
	Count int    `toml:"count"`
}

func TestStoreRoundTripAndMissingFile(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Missing file -> zero value, no error.
	var got sample
	if err := s.LoadTOML("absent.toml", &got); err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got.Name != "" || got.Count != 0 {
		t.Errorf("expected zero value, got %+v", got)
	}

	// Save then load.
	want := sample{Name: "feature-work", Count: 3}
	if err := s.SaveTOML(filepath.Join("configs", "x.toml"), want); err != nil {
		t.Fatal(err)
	}
	var back sample
	if err := s.LoadTOML(filepath.Join("configs", "x.toml"), &back); err != nil {
		t.Fatal(err)
	}
	if back != want {
		t.Errorf("round-trip mismatch: %+v != %+v", back, want)
	}
}

func TestNewRequiresDir(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty config dir")
	}
}
