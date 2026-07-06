package config

import (
	"strings"
	"testing"
)

func TestLoadExplicitValuesAndExpansion(t *testing.T) {
	env := map[string]string{
		"SWITCHBOARDD_HOST_ID":        "build-box",
		"XDG_RUNTIME_DIR":             "/run/user/1000",
		"SWITCHBOARDD_WORKSPACE_ROOT": "/srv/ws",
		"SWITCHBOARDD_DATA_DIR":       "/srv/data",
		"SWITCHBOARDD_SBX_BIN":        "/usr/local/bin/sbx",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HostID != "build-box" {
		t.Errorf("HostID = %q", cfg.HostID)
	}
	// Socket default expands $XDG_RUNTIME_DIR.
	if cfg.Socket != "/run/user/1000/switchboard.sock" {
		t.Errorf("Socket = %q", cfg.Socket)
	}
	if cfg.SbxBin != "/usr/local/bin/sbx" {
		t.Errorf("SbxBin = %q", cfg.SbxBin)
	}
}

// TestSocketPidFallback covers the read-only-filesystem bug: with XDG_RUNTIME_DIR
// unset (a bare SSH session / container), the socket and pid defaults MUST resolve
// under $HOME/.local/share/switchboard rather than collapsing to "/switchboard.*".
func TestSocketPidFallback(t *testing.T) {
	// XDG_RUNTIME_DIR unset, only HOME present.
	env := map[string]string{"HOME": "/home/bob"}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if want := "/home/bob/.local/share/switchboard/switchboard.sock"; cfg.Socket != want {
		t.Errorf("Socket fallback = %q, want %q", cfg.Socket, want)
	}
	if want := "/home/bob/.local/share/switchboard/switchboard.pid"; cfg.PidFile != want {
		t.Errorf("PidFile fallback = %q, want %q", cfg.PidFile, want)
	}

	// Neither XDG_RUNTIME_DIR nor HOME set → a writable temp dir, never root "/".
	cfg2, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(cfg2.Socket, "/switchboard.") || strings.HasPrefix(cfg2.PidFile, "/switchboard.") {
		t.Errorf("no-HOME fallback still resolved to root: socket=%q pid=%q", cfg2.Socket, cfg2.PidFile)
	}

	// When XDG_RUNTIME_DIR IS set, it is honored unchanged (no fallback).
	env3 := map[string]string{"HOME": "/home/bob", "XDG_RUNTIME_DIR": "/run/user/1000"}
	cfg3, err := Load(func(k string) string { return env3[k] })
	if err != nil {
		t.Fatal(err)
	}
	if want := "/run/user/1000/switchboard.sock"; cfg3.Socket != want {
		t.Errorf("Socket with XDG set = %q, want %q", cfg3.Socket, want)
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
