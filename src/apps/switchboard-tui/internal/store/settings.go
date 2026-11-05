package store

import "fmt"

// Settings are the TUI-persisted, per-user preferences shown on the settings
// screen (feature 008, FR-101; SWI-24 adds the editor). Distinct from the
// environment-driven client configuration (internal/config), which is
// unchanged.
type Settings struct {
	// AutoConnectHosts drives the startup sign-in sequence over saved remote
	// hosts (FR-087). On by default.
	AutoConnectHosts bool
	// Editor is the saved editor identifier, raw (SWI-24, FR-501): "" when the
	// key is absent (VS Code). An unknown value is kept as read, so the
	// session can show FR-508's notice and a save of another key never
	// replaces it with the fallback.
	Editor string
	// EditorCustomCommand is the Custom command line as saved, raw; "" = none.
	EditorCustomCommand string
}

// DefaultSettings is what applies with no file (and for any absent key).
func DefaultSettings() Settings { return Settings{AutoConnectHosts: true} }

// settingsFile is the on-disk shape: pointer fields so an absent key keeps its
// default instead of reading as false (an older file never disables a
// default-on setting) and is written back absent.
type settingsFile struct {
	AutoConnectHosts    *bool   `toml:"auto_connect_hosts"`
	Editor              *string `toml:"editor"`
	EditorCustomCommand *string `toml:"editor_custom_command"`
}

const settingsName = "settings.toml"

// SettingsStore persists Settings as settings.toml under the config dir.
type SettingsStore struct {
	s *Store
}

// Settings returns a SettingsStore backed by this Store.
func (s *Store) Settings() *SettingsStore { return &SettingsStore{s: s} }

// SettingsChange names the keys one save writes (SWI-24, FR-530): a non-nil
// field is written, a nil one leaves the file's value as just re-read.
type SettingsChange struct {
	AutoConnectHosts    *bool
	Editor              *string
	EditorCustomCommand *string
}

// SettingsUnreadableError is a save refused because settings.toml couldn't be
// read or parsed when the save re-read it (FR-525): nothing was written.
type SettingsUnreadableError struct {
	Err error
}

func (e *SettingsUnreadableError) Error() string {
	return fmt.Sprintf("settings.toml couldn't be read: %v", e.Err)
}

func (e *SettingsUnreadableError) Unwrap() error { return e.Err }

// Load reads the settings. A missing file yields the defaults; an unreadable or
// malformed file (including a wrong type such as `editor = 5`) yields the
// defaults AND the error, so the screen can say that defaults apply rather
// than fail (FR-101, FR-525).
func (st *SettingsStore) Load() (Settings, error) {
	out := DefaultSettings()
	var f settingsFile
	if err := st.s.LoadTOML(settingsName, &f); err != nil {
		return out, err
	}
	if f.AutoConnectHosts != nil {
		out.AutoConnectHosts = *f.AutoConnectHosts
	}
	if f.Editor != nil {
		out.Editor = *f.Editor
	}
	if f.EditorCustomCommand != nil {
		out.EditorCustomCommand = *f.EditorCustomCommand
	}
	return out, nil
}

// Update re-reads the file (missing = empty), applies only the keys c names,
// and writes the result with owner-only permissions (FR-527, FR-530): a hand
// edit to any other key survives, an absent unchanged key stays absent, and
// an unknown editor value is never replaced by a save of another key. A
// read or parse failure returns *SettingsUnreadableError and writes nothing
// (FR-525, SC-006). Comments and unknown keys are not kept.
func (st *SettingsStore) Update(c SettingsChange) error {
	var f settingsFile
	if err := st.s.LoadTOML(settingsName, &f); err != nil {
		return &SettingsUnreadableError{Err: err}
	}
	if c.AutoConnectHosts != nil {
		v := *c.AutoConnectHosts
		f.AutoConnectHosts = &v
	}
	if c.Editor != nil {
		v := *c.Editor
		f.Editor = &v
	}
	if c.EditorCustomCommand != nil {
		v := *c.EditorCustomCommand
		f.EditorCustomCommand = &v
	}
	return st.s.SaveTOMLPrivate(settingsName, f)
}
