// Package config is the single typed, validated configuration surface for the
// TUI client (Rule VIII intent, adapted to Go). It parses process environment at
// startup, applies defaults, and fails fast naming any offending key. No other
// package reads os.Getenv directly.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type field struct {
	key      string
	required bool
	def      string
	doc      string
}

var schema = []field{
	{"SWITCHBOARD_CONFIG_DIR", false, "$XDG_CONFIG_HOME/switchboard", "Directory holding client TOML state (configs/groups/hosts)."},
	{"SWITCHBOARD_LOCAL_SOCKET", false, "$XDG_RUNTIME_DIR/switchboard.sock", "Default local daemon Unix socket."},
	{"SWITCHBOARD_CODE_BIN", false, "code", "VS Code CLI used to open sandboxes."},
	{"SWITCHBOARD_SBX_BIN", false, "sbx", "Host sandbox CLI used to open a sandbox's interactive agent terminal."},
	{"SWITCHBOARD_TERMINAL", false, "", "Terminal command prefix for the popout terminal (T); empty = system default (e.g. \"kitty -e\", \"gnome-terminal --\", \"tmux new-window\")."},
}

// Config is the parsed, typed client configuration.
type Config struct {
	ConfigDir   string
	LocalSocket string
	CodeBin     string
	SbxBin      string
	Terminal    string
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

// Load parses environment via getenv into a Config. Defaults that reference
// XDG_CONFIG_HOME fall back to ~/.config when that var is unset.
func Load(getenv func(string) string) (*Config, error) {
	expand := func(s string) string {
		return os.Expand(s, func(k string) string {
			if k == "XDG_CONFIG_HOME" && getenv(k) == "" {
				if home, err := os.UserHomeDir(); err == nil {
					return filepath.Join(home, ".config")
				}
			}
			return getenv(k)
		})
	}

	vals := map[string]string{}
	var missing []string
	for _, f := range schema {
		v := getenv(f.key)
		if v == "" {
			v = expand(f.def)
		}
		if v == "" && f.required {
			missing = append(missing, f.key)
		}
		vals[f.key] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}

	return &Config{
		ConfigDir:   filepath.Clean(vals["SWITCHBOARD_CONFIG_DIR"]),
		LocalSocket: vals["SWITCHBOARD_LOCAL_SOCKET"],
		CodeBin:     vals["SWITCHBOARD_CODE_BIN"],
		SbxBin:      vals["SWITCHBOARD_SBX_BIN"],
		Terminal:    defaultTerminal(vals["SWITCHBOARD_TERMINAL"], getenv),
	}, nil
}

// defaultTerminal resolves the popout terminal command prefix: the configured
// value wins; otherwise fall back to $TERMINAL, then the platform default
// (x-terminal-emulator on Linux/BSD, none on macOS where no reliable
// run-a-command terminal exists — the user must configure one).
func defaultTerminal(configured string, getenv func(string) string) string {
	if s := strings.TrimSpace(configured); s != "" {
		return s
	}
	if t := strings.TrimSpace(getenv("TERMINAL")); t != "" {
		return t + " -e"
	}
	if runtime.GOOS == "darwin" {
		return ""
	}
	return "x-terminal-emulator -e"
}
