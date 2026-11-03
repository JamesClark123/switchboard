package editor

import (
	"errors"
	"reflect"
	"testing"
)

func mustEditor(t *testing.T, id string) Editor {
	t.Helper()
	e, ok := Lookup(id)
	if !ok {
		t.Fatalf("no editor %q", id)
	}
	return e
}

func refRule(ref *Refusal) string {
	if ref == nil {
		return ""
	}
	return ref.Rule
}

// TestSC003ArgvPerEditor asserts contracts/editor-launch.md §1 row by row:
// every catalogue editor × {local, remote}. VS Code's rows are byte-identical
// to today's codeArgs (SC-002).
func TestSC003ArgvPerEditor(t *testing.T) {
	const path = "/srv/ws/my proj"
	const target = "user@box"
	sshOption := func(bin string) []string { return []string{bin, "--remote", "ssh-remote+" + target, path} }
	cases := []struct {
		id     string
		local  []string
		remote []string // nil = refused with no-remote
	}{
		{"vscode", []string{"code", path}, sshOption("code")},
		{"code-insiders", []string{"code-insiders", path}, sshOption("code-insiders")},
		{"cursor", []string{"cursor", path}, sshOption("cursor")},
		{"windsurf", []string{"windsurf", path}, sshOption("windsurf")},
		{"zed", []string{"zed", path}, []string{"zed", "ssh://user@box/srv/ws/my%20proj"}},
		{"webstorm", []string{"webstorm", path}, nil},
		{"idea", []string{"idea", path}, nil},
		{"pycharm", []string{"pycharm", path}, nil},
		{"goland", []string{"goland", path}, nil},
		{"sublime", []string{"subl", path}, nil},
	}
	for _, c := range cases {
		e := mustEditor(t, c.id)
		plan, ref := Resolve(Request{Editor: e, WorkspacePath: path})
		if ref != nil || !reflect.DeepEqual(plan.Argv, c.local) {
			t.Errorf("%s local: argv %q, refusal %+v; want %q", c.id, plan.Argv, ref, c.local)
		}
		if ref == nil && (plan.Executable != c.local[0] || plan.EnvVar != e.EnvVar || plan.RemoteNoHost) {
			t.Errorf("%s local plan = %+v", c.id, plan)
		}
		plan, ref = Resolve(Request{Editor: e, WorkspacePath: path, SSHTarget: target})
		if c.remote == nil {
			if refRule(ref) != RuleNoRemote {
				t.Errorf("%s remote: want no-remote, got argv %q refusal %+v", c.id, plan.Argv, ref)
			}
			continue
		}
		if ref != nil || !reflect.DeepEqual(plan.Argv, c.remote) {
			t.Errorf("%s remote: argv %q, refusal %+v; want %q", c.id, plan.Argv, ref, c.remote)
		}
	}
	// Every catalogue row but Custom is in the table.
	if len(cases) != len(Catalogue())-1 {
		t.Errorf("table covers %d editors, catalogue has %d", len(cases), len(Catalogue())-1)
	}
}

func TestResolveCustom(t *testing.T) {
	custom := mustEditor(t, Custom)
	cases := []struct {
		name         string
		cmd          string
		target       string
		home         string
		want         []string
		wantExe      string
		remoteNoHost bool
		wantRule     string
	}{
		{"path as one argument", `~/bin/logedit --open {path} extra`, "", "/home/me", []string{"/home/me/bin/logedit", "--open", "/ws/a b", "extra"}, "/home/me/bin/logedit", false, ""},
		{"no marker: appended", `logedit`, "", "/home/me", []string{"logedit", "/ws/a b"}, "logedit", false, ""},
		{"host dropped locally", `logedit {host} {path}`, "", "", []string{"logedit", "/ws/a b"}, "logedit", false, ""},
		{"host substituted remotely", `logedit --host {host} {path}`, "u@box", "", []string{"logedit", "--host", "u@box", "/ws/a b"}, "logedit", false, ""},
		{"remote with no host marker warns", `logedit {path}`, "u@box", "", []string{"logedit", "/ws/a b"}, "logedit", true, ""},
		{"empty", ``, "", "", nil, "", false, RuleEmpty},
		{"assignment", `API_TOKEN=abc123 x {path}`, "", "", nil, "", false, RuleAssignment},
		{"shell syntax", `cd {path} && nvim .`, "", "", nil, "", false, RuleShellSyntax},
		{"no home", `~/bin/edit {path}`, "", "", nil, "", false, RuleNoHome},
		{"quoted home is literal", `'~/bin/edit' {path}`, "", "", []string{"~/bin/edit", "/ws/a b"}, "~/bin/edit", false, ""},
	}
	for _, c := range cases {
		req := Request{Editor: custom, Custom: c.cmd, SSHTarget: c.target, Home: c.home, WorkspacePath: "/ws/a b"}
		plan, ref := Resolve(req)
		if refRule(ref) != c.wantRule {
			t.Errorf("%s: rule %q, want %q (plan %+v)", c.name, refRule(ref), c.wantRule, plan)
			continue
		}
		if c.wantRule != "" {
			if ref.Where == "" {
				t.Errorf("%s: refusal has no Where", c.name)
			}
			exe, pref := Precheck(req)
			if exe != "" || refRule(pref) != c.wantRule {
				t.Errorf("%s: Precheck = %q, %q", c.name, exe, refRule(pref))
			}
			continue
		}
		if !reflect.DeepEqual(plan.Argv, c.want) || plan.Executable != c.wantExe || plan.RemoteNoHost != c.remoteNoHost || plan.EnvVar != "" {
			t.Errorf("%s: plan = %+v, want argv %q exe %q remoteNoHost %v", c.name, plan, c.want, c.wantExe, c.remoteNoHost)
		}
		if exe, pref := Precheck(req); exe != c.wantExe || pref != nil {
			t.Errorf("%s: Precheck = %q, %+v", c.name, exe, pref)
		}
	}
}

// TestRefusalOrder pins contracts/editor-launch.md §3's order: host-lookup beats
// everything, no-remote beats target-dash, a bad Custom command beats
// target-dash, and the path checks come only after the daemon call.
func TestRefusalOrder(t *testing.T) {
	ws := mustEditor(t, "webstorm")
	vs := mustEditor(t, Default)
	custom := mustEditor(t, Custom)
	lookupErr := errors.New("open hosts.toml: permission denied")
	if _, ref := Precheck(Request{Editor: ws, HostErr: lookupErr, SSHTarget: "-oX"}); refRule(ref) != RuleHostLookup || ref.Detail != lookupErr.Error() {
		t.Errorf("host-lookup must win: %+v", ref)
	}
	if _, ref := Precheck(Request{Editor: vs, HostErr: lookupErr}); refRule(ref) != RuleHostLookup {
		t.Errorf("host-lookup applies to VS Code too: %+v", ref)
	}
	if _, ref := Precheck(Request{Editor: ws, SSHTarget: "-oX"}); refRule(ref) != RuleNoRemote {
		t.Errorf("no-remote beats target-dash: %+v", ref)
	}
	if _, ref := Precheck(Request{Editor: custom, Custom: "cd {path} && x", SSHTarget: "-oX"}); refRule(ref) != RuleShellSyntax {
		t.Errorf("a bad Custom command beats target-dash: %+v", ref)
	}
	if _, ref := Precheck(Request{Editor: vs, SSHTarget: "-oProxyCommand=x"}); refRule(ref) != RuleTargetDash {
		t.Errorf("target-dash for VS Code: %+v", ref)
	}
	zed := mustEditor(t, "zed")
	if _, ref := Precheck(Request{Editor: zed, SSHTarget: "user@box/evil"}); refRule(ref) != RuleZedTarget {
		t.Errorf("zed-target: %+v", ref)
	}
	if _, ref := Precheck(Request{Editor: zed, SSHTarget: "ssh://-oX"}); refRule(ref) != RuleTargetDash {
		t.Errorf("normalised target-dash: %+v", ref)
	}
	// Precheck runs no path check; Resolve does.
	if exe, ref := Precheck(Request{Editor: vs, WorkspacePath: "relative"}); exe != "code" || ref != nil {
		t.Errorf("Precheck must not check the path: %q, %+v", exe, ref)
	}
	if _, ref := Resolve(Request{Editor: vs, WorkspacePath: "relative"}); refRule(ref) != RulePathUnsafe {
		t.Errorf("Resolve checks the path: %+v", ref)
	}
	if _, ref := Resolve(Request{Editor: vs, WorkspacePath: ""}); refRule(ref) != RulePathUnsafe {
		t.Errorf("an empty path is unsafe: %+v", ref)
	}
	if _, ref := Resolve(Request{Editor: zed, SSHTarget: "user@box", WorkspacePath: "work/space"}); refRule(ref) != RulePathUnsafe {
		t.Errorf("a relative path with Zed is path-unsafe, not zed-target: %+v", ref)
	}
}

// TestOverrideVariables: the raw value is one executable (never split), and the
// FR-517 limit applies only for Custom or when the selected editor's own
// variable is set to something other than its default (FR-502, FR-517, T029).
func TestOverrideVariables(t *testing.T) {
	vs := mustEditor(t, Default)
	ws := mustEditor(t, "webstorm")
	custom := mustEditor(t, Custom)
	const bad = "/srv/ws/a(b"
	// Raw value with spaces: one executable.
	plan, ref := Resolve(Request{Editor: ws, Bins: map[string]string{"webstorm": "/opt/My Tools/ws"}, WorkspacePath: "/ws"})
	if ref != nil || !reflect.DeepEqual(plan.Argv, []string{"/opt/My Tools/ws", "/ws"}) || plan.Executable != "/opt/My Tools/ws" {
		t.Errorf("override plan = %+v, %+v", plan, ref)
	}
	cases := []struct {
		name     string
		e        Editor
		bins     map[string]string
		custom   string
		target   string
		wantRule string
	}{
		{"local, variable set: no limit", vs, map[string]string{"vscode": "/x/code"}, "", "", ""},
		{"remote, no variable: no limit", vs, nil, "", "u@b", ""},
		{"remote, variable set: limited", vs, map[string]string{"vscode": "/x/code"}, "", "u@b", RulePathLimited},
		{"remote, variable equal to the default: no limit", vs, map[string]string{"vscode": "code"}, "", "u@b", ""},
		{"remote, spaces-only variable: no limit", vs, map[string]string{"vscode": "   "}, "", "u@b", ""},
		{"remote, another editor's variable: no limit", vs, map[string]string{"zed": "/x/zed"}, "", "u@b", ""},
		{"remote, Custom: limited", custom, nil, "logedit {host} {path}", "u@b", RulePathLimited},
		{"local, Custom: no limit", custom, nil, "logedit {path}", "", ""},
	}
	for _, c := range cases {
		_, ref := Resolve(Request{Editor: c.e, Bins: c.bins, Custom: c.custom, SSHTarget: c.target, WorkspacePath: bad})
		if refRule(ref) != c.wantRule {
			t.Errorf("%s: rule %q, want %q", c.name, refRule(ref), c.wantRule)
			continue
		}
		if ref == nil {
			continue
		}
		if ref.Detail != "'('" {
			t.Errorf("%s: detail %q", c.name, ref.Detail)
		}
		wantWhere := WhereSettings
		if c.e.ID != Custom {
			wantWhere = c.e.EnvVar
		}
		if ref.Where != wantWhere {
			t.Errorf("%s: where %q, want %q", c.name, ref.Where, wantWhere)
		}
	}
}
