package config

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// exampleKeys parses the variable keys declared in the package's .env.example.
func exampleKeys(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatalf("open .env.example: %v", err)
	}
	defer func() { _ = f.Close() }()
	var keys []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, _, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed .env.example line: %q", line)
		}
		keys = append(keys, strings.TrimSpace(k))
	}
	sort.Strings(keys)
	return keys
}

// TestEnvExampleLockstep enforces Rule VIII §4: the schema key-set MUST exactly
// equal the .env.example key-set.
func TestEnvExampleLockstep(t *testing.T) {
	got := strings.Join(exampleKeys(t), ",")
	want := strings.Join(SchemaKeys(), ",")
	if got != want {
		t.Fatalf(".env.example keys != schema keys\n  example: %s\n  schema:  %s", got, want)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	// No key is required today, so exercise the missing-required validation by
	// temporarily declaring one. schema is restored before the test returns.
	orig := schema
	defer func() { schema = orig }()
	schema = append(append([]field{}, orig...), field{"SWITCHBOARDD_TEST_REQUIRED", true, "", "test-only"})

	_, err := Load(func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "SWITCHBOARDD_TEST_REQUIRED") {
		t.Fatalf("expected a missing-required error naming the key, got %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	// No env set (only HOME, as in a real login) — nothing is required, so Load
	// succeeds and the workspace/data dirs default under $HOME/switchboard.
	env := map[string]string{"HOME": "/home/tester"}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error with only HOME set: %v", err)
	}
	if cfg.WorkspaceRoot != "/home/tester/switchboard/workspace" {
		t.Errorf("default WorkspaceRoot = %q, want /home/tester/switchboard/workspace", cfg.WorkspaceRoot)
	}
	if cfg.DataDir != "/home/tester/switchboard/data" {
		t.Errorf("default DataDir = %q, want /home/tester/switchboard/data", cfg.DataDir)
	}
	if cfg.SbxBin != "sbx" {
		t.Errorf("default SbxBin = %q, want sbx", cfg.SbxBin)
	}
	if cfg.HostID == "" {
		t.Error("HostID should default to hostname")
	}
}
