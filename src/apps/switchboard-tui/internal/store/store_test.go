package store

import (
	"os"
	"path/filepath"
	"strings"
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

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestSaveTOMLPrivate pins FR-527: the file is 0600, an existing wider file is
// corrected, a leftover temp file is never reused, and no temp file remains.
func TestSaveTOMLPrivate(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(s.Dir(), "settings.toml")
	if err := s.SaveTOMLPrivate("settings.toml", sample{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if m := modeOf(t, full); m != 0o600 {
		t.Errorf("mode = %o, want 600", m)
	}
	// A pre-existing 0644 file becomes 0600.
	if err := os.Chmod(full, 0o644); err != nil {
		t.Fatal(err)
	}
	// A leftover 0644 temp file with the old fixed name, and a stale one of the
	// new pattern, are not used (the review's case).
	stale := filepath.Join(s.Dir(), ".settings.toml-stale.tmp")
	for _, p := range []string{full + ".tmp", stale} {
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveTOMLPrivate("settings.toml", sample{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	if m := modeOf(t, full); m != 0o600 {
		t.Errorf("mode after a wider file = %o, want 600", m)
	}
	var back sample
	if err := s.LoadTOML("settings.toml", &back); err != nil || back.Name != "b" {
		t.Errorf("content = %+v, %v", back, err)
	}
	if b, _ := os.ReadFile(stale); string(b) != "stale" {
		t.Error("the stale temp file must be left alone")
	}
	// Only the two stale files remain: no temp file of this save.
	if tmps := tempFiles(t, s.Dir()); len(tmps) != 2 {
		t.Errorf("temp files after save = %v", tmps)
	}
	// A failing rename (a directory at the target path) returns the error and
	// leaves no temp file.
	if err := os.Mkdir(filepath.Join(s.Dir(), "dir.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTOMLPrivate("dir.toml", sample{Name: "c"}); err == nil {
		t.Error("expected an error renaming over a directory")
	}
	if tmps := tempFiles(t, s.Dir()); len(tmps) != 2 {
		t.Errorf("temp files after a failed save = %v", tmps)
	}
	if err := s.SaveTOMLPrivate("x.toml", make(chan int)); err == nil {
		t.Error("expected a marshal error")
	}
}
