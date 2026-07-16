package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// openSection drills the editor into a section and returns the model.
func openSection(t *testing.T, k *store.Kit, s kitSection) Model {
	t.Helper()
	out := editorOn(t, k)
	out.kitEditor.section = s
	if s.itemized() {
		out, _ = update(out, press("enter"))
		return out
	}
	return pressCmd(out, "enter")
}

// Network domains are line-delimited and fold into the kit on apply.
func TestKitEditorNetworkFormApplies(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secNetwork)
	out = typeIn(out, "github.com")
	out, _ = update(out, tea.KeyMsg{Type: tea.KeyCtrlJ})
	out = typeIn(out, "pypi.org")
	out, _ = update(out, ctrlS())
	n := out.kitEditor.kit.Network
	if n == nil || len(n.AllowedDomains) != 2 {
		t.Fatalf("network = %+v, want two allowed domains", n)
	}
	if n.AllowedDomains[0] != "github.com" || n.AllowedDomains[1] != "pypi.org" {
		t.Errorf("allowed = %v", n.AllowedDomains)
	}
}

// Clearing every domain drops the section rather than rendering `network: {}`,
// which sbx would read as an explicit empty policy.
func TestKitEditorClearingNetworkDropsSection(t *testing.T) {
	k := &store.Kit{Name: "bare", Network: &store.KitNetwork{AllowedDomains: []string{"x.com"}}}
	out := openSection(t, k, secNetwork)
	// Clear the prefilled field.
	for range len("x.com") {
		out, _ = update(out, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	out, _ = update(out, ctrlS())
	if out.kitEditor.kit.Network != nil {
		t.Errorf("network = %+v, want nil once emptied", out.kitEditor.kit.Network)
	}
}

// The network form prefills from the kit, so an edit does not silently drop rules.
func TestKitEditorNetworkFormPrefills(t *testing.T) {
	k := &store.Kit{Name: "bare", Network: &store.KitNetwork{
		AllowedDomains: []string{"a.com"}, DeniedDomains: []string{"b.com"},
	}}
	out := openSection(t, k, secNetwork)
	if out.kitEditor.vals.allowed != "a.com" || out.kitEditor.vals.denied != "b.com" {
		t.Errorf("vals = %+v, want the existing rules prefilled", out.kitEditor.vals)
	}
}

func TestKitEditorEnvironmentFormApplies(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secEnvironment)
	out = typeIn(out, "MODEL=gemma")
	out, _ = update(out, ctrlS())
	e := out.kitEditor.kit.Environment
	if e == nil || e.Variables["MODEL"] != "gemma" {
		t.Fatalf("environment = %+v, want MODEL=gemma", e)
	}
}

// A malformed env line must report, not silently drop the variable.
func TestKitEditorEnvironmentRejectsBadLine(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secEnvironment)
	out = typeIn(out, "NOT_AN_ASSIGNMENT")
	out, _ = update(out, ctrlS())
	if !strings.Contains(out.kitEditor.status, "KEY=value") {
		t.Errorf("status = %q, want a parse error", out.kitEditor.status)
	}
	if out.kitEditor.form == nil {
		t.Error("form should stay open so the user can fix the line")
	}
}

func TestKitEditorEnvironmentPrefills(t *testing.T) {
	k := &store.Kit{Name: "bare", Environment: &store.KitEnvironment{
		Variables: map[string]string{"A": "1"}, ProxyManaged: []string{"TOKEN"},
	}}
	out := openSection(t, k, secEnvironment)
	if out.kitEditor.vals.vars != "A=1" || out.kitEditor.vals.proxied != "TOKEN" {
		t.Errorf("vals = %+v, want the env prefilled", out.kitEditor.vals)
	}
}

func TestKitEditorCredentialsFormApplies(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secCredentials)
	out = typeIn(out, "github=GH_TOKEN")
	out, _ = update(out, ctrlS())
	c := out.kitEditor.kit.Credentials
	if c == nil || len(c.Sources["github"].Env) != 1 {
		t.Fatalf("credentials = %+v, want a github source", c)
	}
}

func TestKitEditorCredentialsRejectsBadLine(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secCredentials)
	out = typeIn(out, "github")
	out, _ = update(out, ctrlS())
	if !strings.Contains(out.kitEditor.status, "service=ENV_VAR") {
		t.Errorf("status = %q, want a parse error", out.kitEditor.status)
	}
}

func TestKitEditorCredentialsPrefills(t *testing.T) {
	k := &store.Kit{Name: "bare", Credentials: &store.KitCredentials{
		Sources: map[string]store.KitCredentialSource{"github": {Env: []string{"GH_TOKEN"}}},
	}}
	out := openSection(t, k, secCredentials)
	if out.kitEditor.vals.credSources != "github=GH_TOKEN" {
		t.Errorf("vals.credSources = %q, want it prefilled", out.kitEditor.vals.credSources)
	}
}

func TestKitEditorAgentContextApplies(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secAgentContext)
	out = typeIn(out, "Ruff is preinstalled.")
	out, _ = update(out, ctrlS())
	if got := out.kitEditor.kit.AgentContext; got != "Ruff is preinstalled." {
		t.Errorf("agentContext = %q", got)
	}
}

// Item labels must describe each entry well enough to pick from the list.
func TestKitItemLabels(t *testing.T) {
	k := &store.Kit{Name: "k", Commands: &store.KitCommands{
		Install:   []store.KitInstallCommand{{Command: "apt-get install jq"}},
		Startup:   []store.KitStartupCommand{{Command: []string{"sh", "-c", "run"}, Background: true, User: "1000"}},
		InitFiles: []store.KitInitFile{{Path: "/home/agent/x.sh", Mode: "0755"}},
	}}
	out := editorOn(t, k)

	out.kitEditor.section = secInstall
	if got := out.kitItemLabel(secInstall, 0); !strings.Contains(got, "apt-get install jq") || !strings.Contains(got, "root") {
		t.Errorf("install label = %q, want the command and its user", got)
	}
	out.kitEditor.section = secStartup
	got := out.kitItemLabel(secStartup, 0)
	if !strings.Contains(got, "sh -c run") || !strings.Contains(got, "&") || !strings.Contains(got, "agent") {
		t.Errorf("startup label = %q, want argv, background marker and user", got)
	}
	out.kitEditor.section = secInitFiles
	if got := out.kitItemLabel(secInitFiles, 0); !strings.Contains(got, "/home/agent/x.sh") || !strings.Contains(got, "0755") {
		t.Errorf("initFile label = %q, want the path and mode", got)
	}
}

// Every itemized section renders its own items.
func TestKitEditorSectionViewsForEachItemizedSection(t *testing.T) {
	k := &store.Kit{Name: "k", Commands: &store.KitCommands{
		Install:   []store.KitInstallCommand{{Command: "apt-get install jq"}},
		Startup:   []store.KitStartupCommand{{Command: []string{"serve"}}},
		InitFiles: []store.KitInitFile{{Path: "/etc/x.conf"}},
	}}
	for _, tc := range []struct {
		sec  kitSection
		want string
	}{
		{secInstall, "apt-get install jq"},
		{secStartup, "serve"},
		{secInitFiles, "/etc/x.conf"},
	} {
		out := openSection(t, k, tc.sec)
		if v := out.View(); !strings.Contains(v, tc.want) {
			t.Errorf("%v view missing %q; got:\n%s", tc.sec, tc.want, v)
		}
	}
}

// Deleting items must work in every itemized section, and the cursor must not be
// left pointing past the end.
func TestKitEditorDeleteAcrossSections(t *testing.T) {
	for _, sec := range []kitSection{secInstall, secStartup, secInitFiles} {
		k := &store.Kit{Name: "k", Commands: &store.KitCommands{
			Install:   []store.KitInstallCommand{{Command: "a"}, {Command: "b"}},
			Startup:   []store.KitStartupCommand{{Command: []string{"a"}}, {Command: []string{"b"}}},
			InitFiles: []store.KitInitFile{{Path: "/a"}, {Path: "/b"}},
		}}
		out := openSection(t, k, sec)
		out, _ = update(out, press("j")) // move to the last item
		out, _ = update(out, press("d"))
		if got := out.kitSectionLen(); got != 1 {
			t.Errorf("%v: len = %d, want 1 after delete", sec, got)
		}
		if out.kitEditor.item >= out.kitSectionLen() {
			t.Errorf("%v: cursor %d left past the end", sec, out.kitEditor.item)
		}
	}
}

// The item cursor must not run off either end.
func TestKitEditorItemCursorBounds(t *testing.T) {
	k := &store.Kit{Name: "k", Commands: &store.KitCommands{
		Install: []store.KitInstallCommand{{Command: "a"}, {Command: "b"}},
	}}
	out := openSection(t, k, secInstall)
	out, _ = update(out, press("k"))
	if out.kitEditor.item != 0 {
		t.Errorf("item = %d, want it clamped at 0", out.kitEditor.item)
	}
	for range 5 {
		out, _ = update(out, press("j"))
	}
	if out.kitEditor.item != 1 {
		t.Errorf("item = %d, want it clamped at the last entry", out.kitEditor.item)
	}
}

// Deleting from an empty section is a no-op, not a panic.
func TestKitEditorDeleteOnEmptySection(t *testing.T) {
	out := openSection(t, &store.Kit{Name: "bare"}, secInstall)
	out, _ = update(out, press("d"))
	if out.kitSectionLen() != 0 {
		t.Error("delete on an empty section should do nothing")
	}
}

// Renaming a kit must not leave the old directory behind as a duplicate.
func TestSaveKitRenameRemovesOldDir(t *testing.T) {
	out := editorOn(t, ruffKit())
	out.kitEditor.kit.Name = "renamed"
	out, cmd := update(out, ctrlS())
	if cmd != nil {
		runCmd(cmd)
	}
	if _, err := out.kits.Get("renamed"); err != nil {
		t.Fatalf("renamed kit not saved: %v", err)
	}
	if _, err := out.kits.Get("ruff"); err != store.ErrKitNotFound {
		t.Errorf("old kit dir survived a rename: %v", err)
	}
}

// The section list shows a count per section so the kit is scannable.
func TestKitSectionCounts(t *testing.T) {
	k := &store.Kit{
		Name: "k",
		Commands: &store.KitCommands{
			Install:   []store.KitInstallCommand{{Command: "a"}, {Command: "b"}},
			InitFiles: []store.KitInitFile{{Path: "/a"}},
		},
		Network:      &store.KitNetwork{AllowedDomains: []string{"x.com"}},
		Environment:  &store.KitEnvironment{Variables: map[string]string{"A": "1"}},
		Credentials:  &store.KitCredentials{Sources: map[string]store.KitCredentialSource{"gh": {Env: []string{"T"}}}},
		AgentContext: "hi",
	}
	out := editorOn(t, k)
	if got := out.kitSectionCount(secInstall); got != "2" {
		t.Errorf("install count = %q, want 2", got)
	}
	if got := out.kitSectionCount(secStartup); got != "—" {
		t.Errorf("empty startup count = %q, want a dash", got)
	}
	if got := out.kitSectionCount(secIdentity); got != "✓" {
		t.Errorf("identity count = %q, want a tick", got)
	}
	if got := out.kitSectionCount(secAgentContext); got != "✓" {
		t.Errorf("agentContext count = %q, want a tick", got)
	}
	for _, s := range []kitSection{secNetwork, secEnvironment, secCredentials} {
		if got := out.kitSectionCount(s); got != "1" {
			t.Errorf("%v count = %q, want 1", s, got)
		}
	}
	// An empty kit reports nothing set. Built via the "new kit" path rather than the
	// store, which (rightly) refuses to save a nameless kit.
	blank := editorOn(t, ruffKit())
	blank.kitEditor.kit = store.Kit{}
	if got := blank.kitSectionCount(secIdentity); got != "—" {
		t.Errorf("blank identity count = %q, want a dash", got)
	}
	if got := blank.kitSectionCount(secAgentContext); got != "—" {
		t.Errorf("blank agentContext count = %q, want a dash", got)
	}
}

// kitSummary drives the picker rows.
func TestKitSummary(t *testing.T) {
	full := kitSummary(&store.Kit{
		Commands: &store.KitCommands{
			Install:   []store.KitInstallCommand{{Command: "a"}},
			Startup:   []store.KitStartupCommand{{Command: []string{"b"}}},
			InitFiles: []store.KitInitFile{{Path: "/c"}},
		},
		Network:     &store.KitNetwork{AllowedDomains: []string{"x.com"}},
		Environment: &store.KitEnvironment{Variables: map[string]string{"A": "1"}},
	})
	for _, want := range []string{"install", "startup", "file", "domain", "env var"} {
		if !strings.Contains(full, want) {
			t.Errorf("summary %q missing %q", full, want)
		}
	}
	if got := kitSummary(&store.Kit{Name: "x"}); !strings.Contains(got, "empty kit") {
		t.Errorf("summary = %q, want an empty-kit note", got)
	}
}

func TestKitLabelFallsBackToName(t *testing.T) {
	if got := kitLabel(&store.Kit{Name: "ruff"}); got != "ruff" {
		t.Errorf("kitLabel = %q, want the name when no display name", got)
	}
	if got := kitLabel(&store.Kit{Name: "ruff", DisplayName: "Ruff"}); !strings.Contains(got, "Ruff") {
		t.Errorf("kitLabel = %q, want the display name", got)
	}
}

// While attaching, kit management keys are inert — creating or deleting there would
// lose the attach target.
func TestKitPickerAttachModeDisablesManagement(t *testing.T) {
	d := &fakeDaemon{}
	m := kitModel(t, d, ruffKit())
	out, cmd := update(m, press("A"))
	out, _ = update(out, runCmd(cmd))

	out, _ = update(out, press("n"))
	if out.screen != screenKitPicker {
		t.Error("'n' should not create a kit while attaching")
	}
	out, _ = update(out, press("d"))
	if _, err := out.kits.Get("ruff"); err != nil {
		t.Error("'d' should not delete a kit while attaching")
	}
	// Editing is still allowed — it is how you fix a kit before attaching it.
	out, _ = update(out, press("e"))
	if out.screen != screenKitEditor {
		t.Errorf("'e' should open the editor, got %v", out.screen)
	}
}

// Keys on an empty picker must not panic on a nil selection.
func TestKitPickerEmptyListKeysAreSafe(t *testing.T) {
	m := kitModel(t, &fakeDaemon{})
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	for _, k := range []string{"enter", "e", "d"} {
		out, _ = update(out, press(k))
		if out.screen != screenKitPicker {
			t.Errorf("%q on an empty picker changed screen to %v", k, out.screen)
		}
	}
}

func TestKitPickerEscReturnsToList(t *testing.T) {
	m := kitModel(t, &fakeDaemon{}, ruffKit())
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("esc"))
	if out.screen != screenList {
		t.Errorf("screen = %v, want the sandbox list", out.screen)
	}
}
