// Package config is the single typed, validated configuration surface for the
// daemon (Rule VIII intent, adapted to Go). It parses process environment at
// startup and fails fast with an error naming every offending key. No other
// package reads os.Getenv directly.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// field describes one environment variable the daemon reads.
type field struct {
	key      string
	required bool
	def      string
	doc      string
}

// schema is the authoritative list of env vars. It is the source of truth that
// the committed .env.example is kept in lockstep with (verified by env:check).
var schema = []field{
	{"SWITCHBOARDD_HOST_ID", false, "", "Stable id advertised to clients; defaults to the hostname."},
	{"SWITCHBOARDD_SOCKET", false, "$XDG_RUNTIME_DIR/switchboard.sock", "Unix socket path the daemon listens on."},
	{"SWITCHBOARDD_PID_FILE", false, "$XDG_RUNTIME_DIR/switchboard.pid", "PID file written while serving; used by `status`/`stop`."},
	{"SWITCHBOARDD_WORKSPACE_ROOT", false, "$HOME/switchboard/workspace", "Controlled folder for verbatim duplicates (FR-006)."},
	{"SWITCHBOARDD_DATA_DIR", false, "$HOME/switchboard/data", "Directory for the bbolt sandbox registry."},
	{"SWITCHBOARDD_SBX_BIN", false, "sbx", "Path/name of the host sandbox CLI."},
	{"SWITCHBOARDD_HOOK_ADDR", false, "127.0.0.1:8765", "Listen address for the agent hook callback HTTP server."},
}

// Config is the parsed, typed daemon configuration.
type Config struct {
	HostID        string
	Socket        string
	PidFile       string
	WorkspaceRoot string
	DataDir       string
	SbxBin        string
	HookAddr      string
}

// SchemaKeys returns the declared env keys (sorted) — used by env:check.
func SchemaKeys() []string {
	keys := make([]string, len(schema))
	for i, f := range schema {
		keys[i] = f.key
	}
	sort.Strings(keys)
	return keys
}

// Load parses the environment via getenv (os.Getenv in production) into a
// Config, applying defaults and validating required keys. It returns an error
// listing every missing required key rather than failing on the first.
func Load(getenv func(string) string) (*Config, error) {
	vals := map[string]string{}
	var missing []string
	for _, f := range schema {
		v := getenv(f.key)
		if v == "" {
			v = os.Expand(f.def, getenv)
		}
		if v == "" && f.required {
			missing = append(missing, f.key)
		}
		vals[f.key] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}

	hostID := vals["SWITCHBOARDD_HOST_ID"]
	if hostID == "" {
		if h, err := os.Hostname(); err == nil {
			hostID = h
		} else {
			hostID = "localhost"
		}
	}

	cfg := &Config{
		HostID:        hostID,
		Socket:        vals["SWITCHBOARDD_SOCKET"],
		PidFile:       vals["SWITCHBOARDD_PID_FILE"],
		WorkspaceRoot: filepath.Clean(vals["SWITCHBOARDD_WORKSPACE_ROOT"]),
		DataDir:       filepath.Clean(vals["SWITCHBOARDD_DATA_DIR"]),
		SbxBin:        vals["SWITCHBOARDD_SBX_BIN"],
		HookAddr:      vals["SWITCHBOARDD_HOOK_ADDR"],
	}
	return cfg, nil
}
