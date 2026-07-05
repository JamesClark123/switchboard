package store

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// AgentSpec is the client-side agent selection embedded in a Configuration.
type AgentSpec struct {
	Kind  string   `toml:"kind"`
	Args  []string `toml:"args,omitempty"`
	Model string   `toml:"model,omitempty"`
}

// SourceRef is a client-side default source reference.
type SourceRef struct {
	Path   string `toml:"path"`
	IsRepo bool   `toml:"is_repo"`
}

// Configuration is a named, savable set of sbx options piped to a sandbox on
// launch (FR-013–FR-016b). Stored client-side as configs/<id>.toml — the client
// is the source of truth (FR-002c). KitOptions values are JSON-encoded scalars to
// preserve option fidelity over the wire (mirrors ConfigSnapshot.kit_options).
type Configuration struct {
	ID             string            `toml:"id"`
	Name           string            `toml:"name"`
	KitOptions     map[string]string `toml:"kit_options"`
	SeedingMode    string            `toml:"seeding_mode"`
	Agent          *AgentSpec        `toml:"agent,omitempty"`
	DefaultSources []SourceRef       `toml:"default_sources,omitempty"`
	UpdatedAt      time.Time         `toml:"updated_at"`
}

// ConfigStore persists Configurations under <configDir>/configs/.
type ConfigStore struct {
	s *Store
}

// Configs returns a ConfigStore backed by this Store.
func (s *Store) Configs() *ConfigStore { return &ConfigStore{s: s} }

func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "config"
	}
	return out
}

func (c *ConfigStore) file(id string) string { return filepath.Join("configs", id+".toml") }

// Save writes a configuration. If ID is empty it is derived from Name. UpdatedAt
// is refreshed so edits apply to future launches only (FR-016); the caller passes
// the timestamp (the runtime forbids time.Now in some contexts but the TUI may
// supply it).
func (c *ConfigStore) Save(cfg *Configuration, now time.Time) (*Configuration, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, errors.New("configuration name is required")
	}
	if cfg.ID == "" {
		cfg.ID = slug(cfg.Name)
	}
	if cfg.SeedingMode == "" {
		cfg.SeedingMode = "duplicate"
	}
	cfg.UpdatedAt = now
	if err := c.s.SaveTOML(c.file(cfg.ID), cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Get loads a configuration by id (ErrNotFound when absent).
func (c *ConfigStore) Get(id string) (*Configuration, error) {
	path := filepath.Join(c.s.Dir(), c.file(id))
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var cfg Configuration
	if err := c.s.LoadTOML(c.file(id), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// List returns all saved configurations, ordered by name.
func (c *ConfigStore) List() ([]*Configuration, error) {
	dir := filepath.Join(c.s.Dir(), "configs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Configuration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".toml")
		cfg, err := c.Get(id)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete removes a saved configuration. Deleting a missing id is not an error.
func (c *ConfigStore) Delete(id string) error {
	err := os.Remove(filepath.Join(c.s.Dir(), c.file(id)))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ErrNotFound is returned when a configuration id is absent.
var ErrNotFound = errors.New("configuration not found")

// ToSnapshot converts a Configuration to the wire ConfigSnapshot frozen into the
// daemon registry at launch (FR-002b/012d).
func (cfg *Configuration) ToSnapshot() *pb.ConfigSnapshot {
	snap := &pb.ConfigSnapshot{
		Name:        cfg.Name,
		KitOptions:  cfg.KitOptions,
		SeedingMode: pb.SeedingMode_SEEDING_MODE_DUPLICATE,
	}
	if cfg.SeedingMode == "clone" {
		snap.SeedingMode = pb.SeedingMode_SEEDING_MODE_CLONE
	}
	if cfg.Agent != nil && cfg.Agent.Kind != "" {
		snap.Agent = &pb.AgentSpec{Kind: cfg.Agent.Kind, Args: cfg.Agent.Args, Model: cfg.Agent.Model}
	}
	return snap
}
