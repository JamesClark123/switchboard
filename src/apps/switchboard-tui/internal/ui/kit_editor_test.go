package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// editorOn opens the kit editor on a saved kit.
func editorOn(t *testing.T, k *store.Kit) Model {
	t.Helper()
	m := kitModel(t, &fakeDaemon{}, k)
	out, cmd := update(m, press("K"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))
	if out.screen != screenKitEditor {
		t.Fatalf("screen = %v, want screenKitEditor", out.screen)
	}
	return out
}

// pressCmd sends a key and pumps the resulting command back in. Opening a huh form
// returns form.Init(), which focuses the first field; bubbletea runs it in the real
// app, so tests must too or the form never receives typed input.
func pressCmd(m Model, key string) Model {
	m, cmd := update(m, press(key))
	if cmd != nil {
		if msg := runCmd(cmd); msg != nil {
			m, _ = update(m, msg)
		}
	}
	return m
}

// typeIn sends each rune of s to the model as a key message.
func typeIn(m Model, s string) Model {
	for _, r := range s {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestKitEditorSectionNavigation(t *testing.T) {
	out := editorOn(t, ruffKit())
	if out.kitEditor.section != secIdentity {
		t.Fatalf("section = %v, want secIdentity", out.kitEditor.section)
	}
	out, _ = update(out, press("j"))
	if out.kitEditor.section != secInstall {
		t.Errorf("j: section = %v, want secInstall", out.kitEditor.section)
	}
	out, _ = update(out, press("k"))
	if out.kitEditor.section != secIdentity {
		t.Errorf("k: section = %v, want secIdentity", out.kitEditor.section)
	}
	// Cursor must not run off either end.
	out, _ = update(out, press("k"))
	if out.kitEditor.section != secIdentity {
		t.Errorf("section moved above the first entry: %v", out.kitEditor.section)
	}
	for range int(secCount) + 3 {
		out, _ = update(out, press("j"))
	}
	if out.kitEditor.section != secCount-1 {
		t.Errorf("section ran past the last entry: %v", out.kitEditor.section)
	}
}

// The section list must show every section and its item count.
func TestKitEditorSectionListView(t *testing.T) {
	out := editorOn(t, ruffKit())
	v := out.View()
	for _, want := range []string{"Identity", "Install commands", "Startup commands", "Init files", "Network", "Environment", "Credentials", "Agent context", "kind: mixin"} {
		if !strings.Contains(v, want) {
			t.Errorf("section list missing %q; got:\n%s", want, v)
		}
	}
}

// Drilling into an itemized section lists its items.
func TestKitEditorDrillIntoInstallSection(t *testing.T) {
	out := editorOn(t, ruffKit())
	out, _ = update(out, press("j")) // -> Install
	out, _ = update(out, press("enter"))
	if !out.kitEditor.inSection {
		t.Fatal("enter should drill into an itemized section")
	}
	if v := out.View(); !strings.Contains(v, "pip install ruff") {
		t.Errorf("section view should list the install command; got:\n%s", v)
	}
	out, _ = update(out, press("esc"))
	if out.kitEditor.inSection {
		t.Error("esc should leave the section")
	}
}

// Adding an install command through its form, then confirming it lands on the kit.
func TestKitEditorAddInstallCommand(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out, _ = update(out, press("j")) // -> Install
	out, _ = update(out, press("enter"))
	out = pressCmd(out, "a") // add
	if out.kitEditor.form == nil {
		t.Fatal("a should open the item form")
	}
	if out.kitEditor.formItem != -1 {
		t.Errorf("formItem = %d, want -1 (appending)", out.kitEditor.formItem)
	}
	out = typeIn(out, "apt-get install -y jq")
	out, _ = update(out, ctrlS())
	if out.kitEditor.form != nil {
		t.Error("ctrl+s should close the item form")
	}
	got := out.kitEditor.kit.Commands.Install
	if len(got) != 1 || got[0].Command != "apt-get install -y jq" {
		t.Fatalf("install commands = %+v, want the typed command", got)
	}
	// "0" is sbx's default user, so it is omitted rather than restated.
	if got[0].User != "" {
		t.Errorf("user = %q, want it omitted when it equals sbx's default", got[0].User)
	}
}

// An install command with no text must be refused, not appended blank.
func TestKitEditorRejectsEmptyInstallCommand(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out, _ = update(out, press("j"))
	out, _ = update(out, press("enter"))
	out = pressCmd(out, "a")
	out, _ = update(out, ctrlS())
	if !strings.Contains(out.kitEditor.status, "command is required") {
		t.Errorf("status = %q, want a required-field message", out.kitEditor.status)
	}
	if out.kitEditor.kit.Commands != nil && len(out.kitEditor.kit.Commands.Install) != 0 {
		t.Error("a blank command was appended")
	}
}

// Editing an existing item replaces it rather than appending a duplicate.
func TestKitEditorEditsExistingItemInPlace(t *testing.T) {
	out := editorOn(t, ruffKit())
	out, _ = update(out, press("j"))
	out, _ = update(out, press("enter"))
	out = pressCmd(out, "enter") // edit item 0
	if out.kitEditor.formItem != 0 {
		t.Fatalf("formItem = %d, want 0", out.kitEditor.formItem)
	}
	out, _ = update(out, ctrlS())
	if got := out.kitEditor.kit.Commands.Install; len(got) != 1 {
		t.Errorf("editing item 0 produced %d commands, want 1", len(got))
	}
}

func TestKitEditorDeleteItem(t *testing.T) {
	out := editorOn(t, ruffKit())
	out, _ = update(out, press("j"))
	out, _ = update(out, press("enter"))
	out, _ = update(out, press("d"))
	if n := len(out.kitEditor.kit.Commands.Install); n != 0 {
		t.Errorf("install commands = %d, want 0 after delete", n)
	}
	if v := out.View(); !strings.Contains(v, "(none)") {
		t.Errorf("empty section should say so; got:\n%s", v)
	}
}

// Escaping a form discards the in-progress item.
func TestKitEditorCancelItemForm(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out, _ = update(out, press("j"))
	out, _ = update(out, press("enter"))
	out = pressCmd(out, "a")
	out = typeIn(out, "should not persist")
	out, _ = update(out, tea.KeyMsg{Type: tea.KeyEsc})
	if out.kitEditor.form != nil {
		t.Error("esc should close the form")
	}
	if out.kitEditor.kit.Commands != nil && len(out.kitEditor.kit.Commands.Install) != 0 {
		t.Error("cancelled item was still added")
	}
}

// Startup commands are argv arrays: one line per argument.
func TestKitEditorStartupArgvIsLineDelimited(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out.kitEditor.section = secStartup
	out, _ = update(out, press("enter"))
	out = pressCmd(out, "a")
	out = typeIn(out, "sh")
	// huh's Text field takes ctrl+j / alt+enter for a newline; plain enter advances
	// to the next field. The form's own help line tells the user this.
	out, _ = update(out, tea.KeyMsg{Type: tea.KeyCtrlJ})
	out = typeIn(out, "-c")
	out, _ = update(out, ctrlS())
	got := out.kitEditor.kit.Commands.Startup
	if len(got) != 1 {
		t.Fatalf("startup = %+v, want one command", got)
	}
	if len(got[0].Command) != 2 || got[0].Command[0] != "sh" || got[0].Command[1] != "-c" {
		t.Errorf("argv = %v, want [sh -c]", got[0].Command)
	}
}

// An init file needs a path; content alone is not enough.
func TestKitEditorInitFileRequiresPath(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out.kitEditor.section = secInitFiles
	out, _ = update(out, press("enter"))
	out = pressCmd(out, "a")
	out, _ = update(out, ctrlS())
	if !strings.Contains(out.kitEditor.status, "path is required") {
		t.Errorf("status = %q, want a path-required message", out.kitEditor.status)
	}
}

// Scalar sections open a form directly rather than an item list.
func TestKitEditorScalarSectionsOpenForms(t *testing.T) {
	for _, tc := range []struct {
		section kitSection
		kind    kitFormKind
	}{
		{secIdentity, formIdentity},
		{secNetwork, formNetwork},
		{secEnvironment, formEnvironment},
		{secCredentials, formCredentials},
		{secAgentContext, formAgentContext},
	} {
		out := editorOn(t, ruffKit())
		out.kitEditor.section = tc.section
		out, _ = update(out, press("enter"))
		if out.kitEditor.form == nil {
			t.Errorf("%v: enter should open a form", tc.section)
			continue
		}
		if out.kitEditor.formKind != tc.kind {
			t.Errorf("%v: formKind = %v, want %v", tc.section, out.kitEditor.formKind, tc.kind)
		}
		if out.kitEditor.inSection {
			t.Errorf("%v: a scalar section should not enter item mode", tc.section)
		}
	}
}

// Editing identity through the form updates the kit.
func TestKitEditorIdentityFormApplies(t *testing.T) {
	out := editorOn(t, &store.Kit{Name: "bare"})
	out = pressCmd(out, "enter") // identity
	out = typeIn(out, "-x")      // append to the name field
	out, _ = update(out, ctrlS())
	if got := out.kitEditor.kit.Name; got != "bare-x" {
		t.Errorf("name = %q, want the edited value", got)
	}
}

// A changed kit invalidates a previous validation result — it no longer describes it.
func TestKitEditorEditClearsStaleValidation(t *testing.T) {
	out := editorOn(t, ruffKit())
	out.kitEditor.validOK = true
	out.kitEditor.validation = []string{"stale"}
	out = pressCmd(out, "enter") // identity form
	out, _ = update(out, ctrlS())
	if out.kitEditor.validOK || out.kitEditor.validation != nil {
		t.Error("editing the kit should clear the previous validation result")
	}
}

func TestKitEditorEscReturnsToPicker(t *testing.T) {
	out := editorOn(t, ruffKit())
	out, cmd := update(out, press("q"))
	if out.screen != screenKitPicker {
		t.Errorf("screen = %v, want a return to the picker", out.screen)
	}
	if cmd != nil {
		runCmd(cmd)
	}
}

// ---------- pure helpers ----------

func TestSplitLines(t *testing.T) {
	got := splitLines("  a  \n\n b\n\t\n c ")
	want := []string{"a", "b", "c"}
	if len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
		t.Errorf("splitLines = %v, want %v", got, want)
	}
	if len(splitLines("   ")) != 0 {
		t.Error("blank input should yield no lines")
	}
}

func TestParseEnv(t *testing.T) {
	got, err := parseEnv("A=1\n B = two \nC=")
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] != "1" || got["B"] != "two" || got["C"] != "" {
		t.Errorf("parseEnv = %v", got)
	}
	if _, err := parseEnv("NOPE"); err == nil {
		t.Error("a line without '=' should be rejected")
	}
	if _, err := parseEnv("=v"); err == nil {
		t.Error("an empty key should be rejected")
	}
}

// joinEnv must be parseEnv's inverse and stable (map order is random otherwise).
func TestJoinEnvRoundTripsAndIsSorted(t *testing.T) {
	in := map[string]string{"B": "2", "A": "1"}
	s := joinEnv(in)
	if s != "A=1\nB=2" {
		t.Errorf("joinEnv = %q, want sorted lines", s)
	}
	back, err := parseEnv(s)
	if err != nil {
		t.Fatal(err)
	}
	if back["A"] != "1" || back["B"] != "2" {
		t.Errorf("round trip = %v", back)
	}
}

func TestParseCredentials(t *testing.T) {
	got, err := parseCredentials("github=GH_TOKEN,GITHUB_TOKEN\nanthropic=ANTHROPIC_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if len(got["github"].Env) != 2 || got["github"].Env[1] != "GITHUB_TOKEN" {
		t.Errorf("parseCredentials = %+v", got)
	}
	for _, bad := range []string{"github", "=GH_TOKEN", "github="} {
		if _, err := parseCredentials(bad); err == nil {
			t.Errorf("parseCredentials(%q) should be rejected", bad)
		}
	}
}

func TestJoinCredentialsRoundTrips(t *testing.T) {
	in := map[string]store.KitCredentialSource{"github": {Env: []string{"GH_TOKEN", "X"}}}
	s := joinCredentials(in)
	back, err := parseCredentials(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(back["github"].Env) != 2 {
		t.Errorf("round trip = %+v", back)
	}
}

func TestDefaultAndOmitDefault(t *testing.T) {
	if got := defaultStr("  ", "0"); got != "0" {
		t.Errorf("defaultStr = %q, want the default", got)
	}
	if got := defaultStr("5", "0"); got != "5" {
		t.Errorf("defaultStr = %q, want the value", got)
	}
	// A value equal to sbx's default is stored empty so spec.yaml omits it.
	if got := omitDefault("0", "0"); got != "" {
		t.Errorf("omitDefault = %q, want empty", got)
	}
	if got := omitDefault("1000", "0"); got != "1000" {
		t.Errorf("omitDefault = %q, want the value", got)
	}
}

func TestUserSuffix(t *testing.T) {
	if got := userSuffix("", "0"); !strings.Contains(got, "root") {
		t.Errorf("userSuffix(0) = %q, want root", got)
	}
	if got := userSuffix("1000", "0"); !strings.Contains(got, "agent") {
		t.Errorf("userSuffix(1000) = %q, want agent", got)
	}
	if got := userSuffix("42", "0"); !strings.Contains(got, "uid 42") {
		t.Errorf("userSuffix(42) = %q, want uid 42", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("a\nb", 10); got != "a b" {
		t.Errorf("truncate should flatten newlines, got %q", got)
	}
	got := truncate(strings.Repeat("x", 50), 10)
	if len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate = %q, want 10 chars ending in an ellipsis", got)
	}
}

func TestKitSectionTitlesAndBlurbs(t *testing.T) {
	for s := kitSection(0); s < secCount; s++ {
		if s.title() == "" {
			t.Errorf("section %d has no title", s)
		}
		if s.blurb() == "" {
			t.Errorf("section %d has no blurb", s)
		}
	}
	if !secInstall.itemized() || !secStartup.itemized() || !secInitFiles.itemized() {
		t.Error("command sections should be itemized")
	}
	if secIdentity.itemized() || secNetwork.itemized() {
		t.Error("scalar sections should not be itemized")
	}
}

func TestKitLineError(t *testing.T) {
	err := errKitLine("expected KEY=value", "bad")
	if !strings.Contains(err.Error(), "expected KEY=value") || !strings.Contains(err.Error(), `"bad"`) {
		t.Errorf("error = %q, want it to quote the offending line", err)
	}
}
