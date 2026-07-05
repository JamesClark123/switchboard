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
