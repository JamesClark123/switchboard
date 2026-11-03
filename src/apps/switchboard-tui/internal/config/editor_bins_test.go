package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/editor"
)

// TestEditorVariablesDeclared: every built-in editor's override variable is in
// the declared schema with its default and the editor named (FR-503), and
// SWITCHBOARD_CODE_BIN keeps its row.
func TestEditorVariablesDeclared(t *testing.T) {
	keys := SchemaKeys()
	for _, e := range editor.Catalogue() {
		if e.EnvVar == "" {
			continue
		}
		if !slices.Contains(keys, e.EnvVar) {
			t.Errorf("%s is not declared", e.EnvVar)
		}
		doc := SchemaDoc(e.EnvVar)
		if !strings.Contains(doc, e.Name) || !strings.Contains(doc, "One executable") {
			t.Errorf("%s doc = %q; want the editor's name and the single-executable note", e.EnvVar, doc)
		}
		if e.ID != editor.Default && !strings.Contains(doc, e.Command) {
			t.Errorf("%s doc = %q; want the default %q", e.EnvVar, doc, e.Command)
		}
	}
	if SchemaDoc("NOPE") != "" {
		t.Error("unknown key must have no doc")
	}
	// The variables are declared in catalogue order after SWITCHBOARD_CODE_BIN.
	var order []string
	for _, f := range schema {
		if strings.HasSuffix(f.key, "_BIN") && f.key != "SWITCHBOARD_SBX_BIN" {
			order = append(order, f.key)
		}
	}
	if order[0] != "SWITCHBOARD_CODE_BIN" || len(order) != 10 {
		t.Errorf("editor variable order = %v", order)
	}
}

// TestEditorBinsRaw: the value is kept raw (one executable, never split, no
// expansion), blank reads as unset, and SWITCHBOARD_CODE_BIN is unchanged
// (FR-502).
func TestEditorBinsRaw(t *testing.T) {
	env := map[string]string{
		"SWITCHBOARD_CODE_BIN":     "code-insiders",
		"SWITCHBOARD_WEBSTORM_BIN": "/opt/My Tools/ws",
		"SWITCHBOARD_ZED_BIN":      "   ",
		"SWITCHBOARD_GOLAND_BIN":   "~/bin/goland $X",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"vscode": "code-insiders", "webstorm": "/opt/My Tools/ws", "zed": "", "goland": "~/bin/goland $X",
		"code-insiders": "", "cursor": "", "windsurf": "", "idea": "", "pycharm": "", "sublime": "",
	}
	for id, w := range want {
		if got, ok := cfg.EditorBins[id]; !ok || got != w {
			t.Errorf("EditorBins[%q] = %q (present %v), want %q", id, got, ok, w)
		}
	}
	if _, ok := cfg.EditorBins["custom"]; ok {
		t.Error("Custom has no variable")
	}
	// With nothing set every entry is unset, so editor.Command gives the default.
	def, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range editor.Catalogue() {
		if e.EnvVar == "" {
			continue
		}
		if def.EditorBins[e.ID] != "" {
			t.Errorf("EditorBins[%q] = %q with nothing set", e.ID, def.EditorBins[e.ID])
		}
		if bin, ov := editor.Command(e, def.EditorBins); bin != e.Command || ov {
			t.Errorf("Command(%s) = %q, %v with nothing set", e.ID, bin, ov)
		}
	}
}
