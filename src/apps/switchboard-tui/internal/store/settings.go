package store

// Settings are the TUI-persisted, per-user preferences shown on the settings
// screen (feature 008, FR-101). Distinct from the environment-driven client
// configuration (internal/config), which is unchanged.
type Settings struct {
	// AutoConnectHosts drives the startup sign-in sequence over saved remote
	// hosts (FR-087). On by default.
	AutoConnectHosts bool
}

// DefaultSettings is what applies with no file (and for any absent key).
func DefaultSettings() Settings { return Settings{AutoConnectHosts: true} }

// settingsFile is the on-disk shape: pointer fields so an absent key keeps its
// default instead of reading as false (an older file never disables a
// default-on setting).
type settingsFile struct {
	AutoConnectHosts *bool `toml:"auto_connect_hosts"`
}

const settingsName = "settings.toml"

// SettingsStore persists Settings as settings.toml under the config dir.
type SettingsStore struct {
	s *Store
}

// Settings returns a SettingsStore backed by this Store.
func (s *Store) Settings() *SettingsStore { return &SettingsStore{s: s} }

// Load reads the settings. A missing file yields the defaults; an unreadable or
// malformed file yields the defaults AND the error, so the screen can say that
// defaults apply rather than fail (FR-101).
func (st *SettingsStore) Load() (Settings, error) {
	out := DefaultSettings()
	var f settingsFile
	if err := st.s.LoadTOML(settingsName, &f); err != nil {
		return out, err
	}
	if f.AutoConnectHosts != nil {
		out.AutoConnectHosts = *f.AutoConnectHosts
	}
	return out, nil
}

// Save writes the settings atomically.
func (st *SettingsStore) Save(v Settings) error {
	auto := v.AutoConnectHosts
	return st.s.SaveTOML(settingsName, settingsFile{AutoConnectHosts: &auto})
}
