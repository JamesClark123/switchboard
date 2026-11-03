// Package store is the client-side source of truth (FR-002c): saved
// configurations, groups, and known hosts persisted as human-editable TOML under
// the XDG config dir. This file holds the shared base (paths + generic TOML
// load/save); per-entity stores (configs, groups, hosts) build on it in later
// user stories.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Store roots all client TOML state at a config directory.
type Store struct {
	dir string
}

// New returns a Store rooted at configDir, creating it (and a configs/ subdir)
// if absent.
func New(configDir string) (*Store, error) {
	if configDir == "" {
		return nil, errors.New("config dir is required")
	}
	if err := os.MkdirAll(filepath.Join(configDir, "configs"), 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: configDir}, nil
}

// Dir returns the root config directory.
func (s *Store) Dir() string { return s.dir }

// path resolves a name relative to the store root.
func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// LoadTOML reads and decodes a TOML file (resolved under the store root) into v.
// A missing file is not an error (v is left at its zero value) so first-run reads
// succeed. Per-entity stores (configs/groups/hosts) build on this.
func (s *Store) LoadTOML(name string, v any) error {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := toml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}
	return nil
}

// SaveTOML encodes v and atomically writes it to name (write-temp-then-rename),
// resolved under the store root.
func (s *Store) SaveTOML(name string, v any) error {
	b, err := toml.Marshal(v)
	if err != nil {
		return err
	}
	full := s.path(name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, full)
}

// SaveTOMLPrivate encodes v and writes it to name with owner-only permissions
// (SWI-24, FR-527): the bytes go to a fresh temp file created with a unique
// name and mode 0600 in the same directory, which is renamed over the target,
// and the target is then chmod'ed to 0600 so an existing wider file is
// corrected. A leftover temp file from a crash is never reused, so it can't
// widen the mode, and two running TUIs never share one. A write, close or
// rename failure removes the temp file, because it can hold the Custom
// command. SaveTOML is unchanged: hosts, groups and configs keep their path.
func (s *Store) SaveTOMLPrivate(name string, v any) error {
	b, err := toml.Marshal(v)
	if err != nil {
		return err
	}
	full := s.path(name)
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(name)+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(b); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(full, 0o600)
}
