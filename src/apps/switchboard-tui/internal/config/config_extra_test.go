package config

import "testing"

func TestLoadHomeFallbackAndExplicit(t *testing.T) {
	// XDG_CONFIG_HOME unset -> falls back to ~/.config.
	cfg, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigDir == "" || cfg.ConfigDir == "/switchboard" {
		t.Errorf("expected a home-based config dir, got %q", cfg.ConfigDir)
	}

	env := map[string]string{
		"SWITCHBOARD_CONFIG_DIR":   "/cfg",
		"SWITCHBOARD_LOCAL_SOCKET": "/run/s.sock",
		"SWITCHBOARD_CODE_BIN":     "code-insiders",
		"SWITCHBOARD_SBX_BIN":      "sbx-dev",
		"SWITCHBOARD_TERMINAL":     "kitty -e",
	}
	cfg, err = Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigDir != "/cfg" || cfg.CodeBin != "code-insiders" || cfg.SbxBin != "sbx-dev" || cfg.LocalSocket != "/run/s.sock" {
		t.Errorf("explicit values not applied: %+v", cfg)
	}
	if cfg.Terminal != "kitty -e" {
		t.Errorf("explicit terminal = %q, want kitty -e", cfg.Terminal)
	}
	// SBX_BIN defaults to "sbx" when unset.
	if def, _ := Load(func(string) string { return "" }); def.SbxBin != "sbx" {
		t.Errorf("SbxBin default = %q, want sbx", def.SbxBin)
	}
	// $TERMINAL drives the popout default when SWITCHBOARD_TERMINAL is unset.
	tdef, _ := Load(func(k string) string {
		if k == "TERMINAL" {
			return "wezterm"
		}
		return ""
	})
	if tdef.Terminal != "wezterm -e" {
		t.Errorf("terminal from $TERMINAL = %q, want 'wezterm -e'", tdef.Terminal)
	}
}

// TestLocalSocketFallback guards the client/daemon socket agreement: with
// XDG_RUNTIME_DIR unset, SWITCHBOARD_LOCAL_SOCKET MUST resolve to
// $HOME/.local/share/switchboard/switchboard.sock — the exact path switchboardd
// falls back to — rather than collapsing to "/switchboard.sock".
func TestLocalSocketFallback(t *testing.T) {
	// XDG_RUNTIME_DIR unset, only HOME present.
	env := map[string]string{"HOME": "/home/bob"}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if want := "/home/bob/.local/share/switchboard/switchboard.sock"; cfg.LocalSocket != want {
		t.Errorf("LocalSocket fallback = %q, want %q (must match switchboardd)", cfg.LocalSocket, want)
	}

	// XDG_RUNTIME_DIR set → honored unchanged.
	env2 := map[string]string{"HOME": "/home/bob", "XDG_RUNTIME_DIR": "/run/user/1000"}
	cfg2, err := Load(func(k string) string { return env2[k] })
	if err != nil {
		t.Fatal(err)
	}
	if want := "/run/user/1000/switchboard.sock"; cfg2.LocalSocket != want {
		t.Errorf("LocalSocket with XDG set = %q, want %q", cfg2.LocalSocket, want)
	}

	// Neither set → a real path, never root "/switchboard.sock".
	cfg3, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg3.LocalSocket == "/switchboard.sock" {
		t.Errorf("no-HOME fallback collapsed to root: %q", cfg3.LocalSocket)
	}
}

func TestSchemaKeysSorted(t *testing.T) {
	keys := SchemaKeys()
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Fatalf("SchemaKeys not sorted: %v", keys)
		}
	}
}
