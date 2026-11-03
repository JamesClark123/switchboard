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

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/editor"
)

type field struct {
	key      string
	required bool
	def      string
	doc      string
	// raw: the value is kept as given, with no default substituted, so a
	// caller can tell set from unset (the editor override variables, FR-502;
	// editor.Command applies the default).
	raw bool
}

var schema = buildSchema()

// buildSchema lists the keys in documentation order: the editor override
// variables come from the catalogue (SWI-24, FR-503), SWITCHBOARD_CODE_BIN
// first among them with its existing row.
func buildSchema() []field {
	s := []field{
		{key: "SWITCHBOARD_CONFIG_DIR", def: "$XDG_CONFIG_HOME/switchboard", doc: "Directory holding client TOML state (configs/groups/hosts)."},
		{key: "SWITCHBOARD_LOCAL_SOCKET", def: "$XDG_RUNTIME_DIR/switchboard.sock", doc: "Default local daemon Unix socket. When XDG_RUNTIME_DIR is unset it falls back to $HOME/.local/share/switchboard, matching switchboardd."},
		{key: "SWITCHBOARD_CODE_BIN", def: "code", doc: "VS Code CLI used to open sandboxes (the default editor). One executable; a wrapper script or Custom covers a command with arguments.", raw: true},
	}
	for _, e := range editor.Catalogue() {
		if e.EnvVar == "" || e.ID == editor.Default {
			continue
		}
		s = append(s, field{key: e.EnvVar, def: e.Command, doc: e.Name + " CLI used to open sandboxes (default " + e.Command + "). One executable; a wrapper script or Custom covers a command with arguments.", raw: true})
	}
	return append(s,
		field{key: "SWITCHBOARD_SBX_BIN", def: "sbx", doc: "Host sandbox CLI used to open a sandbox's interactive agent terminal."},
		field{key: "SWITCHBOARD_TERMINAL", def: "", doc: "Terminal command prefix for the popout terminal (T); empty = system default (e.g. \"kitty -e\", \"gnome-terminal --\", \"tmux new-window\")."},
	)
}

// Config is the parsed, typed client configuration.
type Config struct {
	ConfigDir   string
	LocalSocket string
	// EditorBins holds each built-in editor's override variable by editor ID,
	// raw: "" when unset or blank (FR-502). The default command is applied by
	// editor.Command, the one place that decides whether the variable counts
	// as an override (FR-517).
	EditorBins map[string]string
	SbxBin     string
	Terminal   string
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

// SchemaDoc returns a key's documentation line ("" for an unknown key).
func SchemaDoc(key string) string {
	for _, f := range schema {
		if f.key == key {
			return f.doc
		}
	}
	return ""
}

// Load parses environment via getenv into a Config. Defaults that reference
// XDG_CONFIG_HOME fall back to ~/.config when that var is unset.
func Load(getenv func(string) string) (*Config, error) {
	expand := func(s string) string {
		return os.Expand(s, func(k string) string {
			switch {
			case k == "XDG_CONFIG_HOME" && getenv(k) == "":
				if home, err := os.UserHomeDir(); err == nil {
					return filepath.Join(home, ".config")
				}
			case k == "XDG_RUNTIME_DIR" && getenv(k) == "":
				// Mirror switchboardd's socket fallback EXACTLY (see that package's
				// config.Load) so the client and daemon agree on the local socket
				// path when XDG_RUNTIME_DIR is unset — e.g. a bare `ssh host`
				// session, cron, or a container. Diverging here reintroduces the
				// "dial unix /switchboard.sock: no such file" mismatch.
				if home := getenv("HOME"); home != "" {
					return filepath.Join(home, ".local", "share", "switchboard")
				}
				return filepath.Join(os.TempDir(), "switchboard")
			}
			return getenv(k)
		})
	}

	vals := map[string]string{}
	var missing []string
	for _, f := range schema {
		v := getenv(f.key)
		if f.raw {
			// Kept as given; blank reads as unset (FR-502).
			if strings.TrimSpace(v) == "" {
				v = ""
			}
			vals[f.key] = v
			continue
		}
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

	bins := map[string]string{}
	for _, e := range editor.Catalogue() {
		if e.EnvVar != "" {
			bins[e.ID] = vals[e.EnvVar]
		}
	}
	return &Config{
		ConfigDir:   filepath.Clean(vals["SWITCHBOARD_CONFIG_DIR"]),
		LocalSocket: vals["SWITCHBOARD_LOCAL_SOCKET"],
		EditorBins:  bins,
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
