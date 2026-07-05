package config

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

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

// TestEnvExampleLockstep enforces Rule VIII §4.
func TestEnvExampleLockstep(t *testing.T) {
	got := strings.Join(exampleKeys(t), ",")
	want := strings.Join(SchemaKeys(), ",")
	if got != want {
		t.Fatalf(".env.example keys != schema keys\n  example: %s\n  schema:  %s", got, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return "/home/u/.config"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ConfigDir != "/home/u/.config/switchboard" {
		t.Errorf("ConfigDir = %q", cfg.ConfigDir)
	}
	if cfg.CodeBin != "code" {
		t.Errorf("CodeBin = %q, want code", cfg.CodeBin)
	}
}
