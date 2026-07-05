package config

import "testing"

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

func TestSchemaKeysSorted(t *testing.T) {
	keys := SchemaKeys()
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Fatalf("SchemaKeys not sorted: %v", keys)
		}
	}
}
