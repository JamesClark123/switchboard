package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirAccessor(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", s.Dir(), dir)
	}
}

func TestNewFailsWhenConfigDirIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(f); err == nil {
		t.Error("expected error when config dir is a regular file")
	}
}

func TestLoadTOMLParseError(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "bad.toml"), []byte("= not valid ="), 0o644); err != nil {
		t.Fatal(err)
	}
	var v sample
	if err := s.LoadTOML("bad.toml", &v); err == nil {
		t.Error("expected parse error for malformed TOML")
	}
}
