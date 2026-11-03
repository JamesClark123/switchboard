package editor

import "testing"

// TestCatalogueRows pins contracts/editor-launch.md §1 and §5: every row's
// identifier, name, default command, override variable and remote kind, in
// FR-501's order.
func TestCatalogueRows(t *testing.T) {
	want := []Editor{
		{"vscode", "VS Code", "code", "SWITCHBOARD_CODE_BIN", RemoteSSHOption},
		{"code-insiders", "VS Code Insiders", "code-insiders", "SWITCHBOARD_CODE_INSIDERS_BIN", RemoteSSHOption},
		{"cursor", "Cursor", "cursor", "SWITCHBOARD_CURSOR_BIN", RemoteSSHOption},
		{"windsurf", "Windsurf", "windsurf", "SWITCHBOARD_WINDSURF_BIN", RemoteSSHOption},
		{"zed", "Zed", "zed", "SWITCHBOARD_ZED_BIN", RemoteSSHAddress},
		{"webstorm", "WebStorm", "webstorm", "SWITCHBOARD_WEBSTORM_BIN", RemoteNone},
		{"idea", "IntelliJ IDEA", "idea", "SWITCHBOARD_IDEA_BIN", RemoteNone},
		{"pycharm", "PyCharm", "pycharm", "SWITCHBOARD_PYCHARM_BIN", RemoteNone},
		{"goland", "GoLand", "goland", "SWITCHBOARD_GOLAND_BIN", RemoteNone},
		{"sublime", "Sublime Text", "subl", "SWITCHBOARD_SUBLIME_BIN", RemoteNone},
		{"custom", "Custom", "", "", RemoteCustom},
	}
	got := Catalogue()
	if len(got) != len(want) {
		t.Fatalf("catalogue has %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The copy is the caller's.
	got[0].Name = "changed"
	if Catalogue()[0].Name != "VS Code" {
		t.Error("Catalogue must return a copy")
	}
	if Default != "vscode" || Custom != "custom" {
		t.Errorf("Default/Custom = %q/%q", Default, Custom)
	}
}

func TestLookup(t *testing.T) {
	if e, ok := Lookup("webstorm"); !ok || e.Name != "WebStorm" {
		t.Errorf("Lookup(webstorm) = %+v, %v", e, ok)
	}
	if _, ok := Lookup("future-ide"); ok {
		t.Error("unknown identifier must not be found")
	}
	if _, ok := Lookup(""); ok {
		t.Error("the empty identifier must not be found")
	}
}

// TestCommand pins FR-502 and FR-517's meaning of "set": blank is unset, a set
// value is used raw as one executable, and a value equal to the default is
// not an override.
func TestCommand(t *testing.T) {
	ws, _ := Lookup("webstorm")
	cases := []struct {
		name       string
		bins       map[string]string
		wantBin    string
		overridden bool
	}{
		{"unset", nil, "webstorm", false},
		{"empty", map[string]string{"webstorm": ""}, "webstorm", false},
		{"blank", map[string]string{"webstorm": "  \t "}, "webstorm", false},
		{"set", map[string]string{"webstorm": "/opt/My Tools/ws"}, "/opt/My Tools/ws", true},
		{"set with spaces kept raw", map[string]string{"webstorm": " ws "}, " ws ", true},
		{"equal to default", map[string]string{"webstorm": "webstorm"}, "webstorm", false},
		{"another editor's variable", map[string]string{"zed": "/x/zed"}, "webstorm", false},
	}
	for _, c := range cases {
		bin, ov := Command(ws, c.bins)
		if bin != c.wantBin || ov != c.overridden {
			t.Errorf("%s: Command = %q, %v; want %q, %v", c.name, bin, ov, c.wantBin, c.overridden)
		}
	}
	// SWITCHBOARD_CODE_BIN keeps its meaning for VS Code.
	vs, _ := Lookup(Default)
	if bin, ov := Command(vs, map[string]string{"vscode": "code-insiders"}); bin != "code-insiders" || !ov {
		t.Errorf("vscode override = %q, %v", bin, ov)
	}
}
