package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func newKitStore(t *testing.T) *KitStore {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s.Kits()
}

// A realistic kit exercising every section the editor exposes. Install commands and
// initFile contents are arbitrary shell and multi-line text, which is exactly where
// a hand-rolled YAML emitter would corrupt quoting — so the render must round-trip.
func sampleKit() *Kit {
	return &Kit{
		Name:        "Ruff Lint",
		DisplayName: "Ruff",
		Description: "Python linting",
		Commands: &KitCommands{
			Install: []KitInstallCommand{{
				Command:     `curl -fsSL https://x.dev/i.sh | sh && echo "done: 'quoted'"`,
				Description: "install ruff",
			}},
			InitFiles: []KitInitFile{{
				Path:    "/home/agent/.local/bin/start.sh",
				Content: "#!/usr/bin/env bash\nset -euo pipefail\nexec code-server --auth none \"${WORKDIR}\"\n",
				Mode:    "0755",
			}},
			Startup: []KitStartupCommand{{
				Command:    []string{"sh", "-c", "nohup /home/agent/.local/bin/start.sh &"},
				User:       "1000",
				Background: true,
			}},
		},
		Network:      &KitNetwork{AllowedDomains: []string{"code-server.dev"}, DeniedDomains: []string{"evil.example"}},
		Environment:  &KitEnvironment{Variables: map[string]string{"MODEL": "gemma:4b"}, ProxyManaged: []string{"ANTHROPIC_API_KEY"}},
		Credentials:  &KitCredentials{Sources: map[string]KitCredentialSource{"github": {Env: []string{"GH_TOKEN"}}}},
		AgentContext: "## Sandbox\nRuff is preinstalled.",
	}
}

func TestSpecYAMLRendersRequiredIdentity(t *testing.T) {
	y, err := sampleKit().SpecYAML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`schemaVersion: "1"`, "kind: mixin", "name: ruff-lint"} {
		if !strings.Contains(y, want) {
			t.Errorf("spec.yaml missing %q:\n%s", want, y)
		}
	}
}

// Octal modes and uids MUST render as strings — sbx's schema types them as strings,
// and an unquoted 0755 would be a YAML integer.
func TestSpecYAMLQuotesModesAndUsers(t *testing.T) {
	y, err := sampleKit().SpecYAML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`mode: "0755"`, `user: "1000"`} {
		if !strings.Contains(y, want) {
			t.Errorf("spec.yaml should quote %q:\n%s", want, y)
		}
	}
}

func TestSpecYAMLRoundTrips(t *testing.T) {
	src := sampleKit()
	y, err := src.SpecYAML()
	if err != nil {
		t.Fatal(err)
	}
	var back Kit
	if err := yaml.Unmarshal([]byte(y), &back); err != nil {
		t.Fatalf("rendered spec.yaml does not parse: %v\n%s", err, y)
	}
	if got, want := back.Commands.Install[0].Command, src.Commands.Install[0].Command; got != want {
		t.Errorf("install command mangled:\n got %q\nwant %q", got, want)
	}
	if got, want := back.Commands.InitFiles[0].Content, src.Commands.InitFiles[0].Content; got != want {
		t.Errorf("initFile content mangled:\n got %q\nwant %q", got, want)
	}
	if got := back.Commands.Startup[0].Command; len(got) != 3 || got[0] != "sh" {
		t.Errorf("startup argv mangled: %v", got)
	}
	if !back.Commands.Startup[0].Background {
		t.Error("startup background flag lost")
	}
	if back.Environment.Variables["MODEL"] != "gemma:4b" {
		t.Errorf("env var mangled: %v", back.Environment.Variables)
	}
	if back.AgentContext != src.AgentContext {
		t.Errorf("agentContext mangled: %q", back.AgentContext)
	}
}

// Empty sections must be omitted entirely rather than rendered as `network: {}`,
// which sbx would read as an explicit (empty) policy.
func TestSpecYAMLOmitsEmptySections(t *testing.T) {
	k := &Kit{Name: "bare", Commands: &KitCommands{}, Network: &KitNetwork{}, Environment: &KitEnvironment{}, Credentials: &KitCredentials{}}
	y, err := k.SpecYAML()
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"commands:", "network:", "environment:", "credentials:"} {
		if strings.Contains(y, unwanted) {
			t.Errorf("empty section %q should be omitted:\n%s", unwanted, y)
		}
	}
}

func TestSpecYAMLRequiresName(t *testing.T) {
	if _, err := (&Kit{Name: "  "}).SpecYAML(); err == nil {
		t.Error("expected a kit with no name to be rejected")
	}
}

// The id is the directory name on both sides of the wire, so it must be a slug the
// daemon's materializer accepts.
func TestKitIDIsSlugged(t *testing.T) {
	if got := (&Kit{Name: "Ruff Lint"}).ID(); got != "ruff-lint" {
		t.Errorf("ID() = %q, want %q", got, "ruff-lint")
	}
}

func TestSaveGetListDelete(t *testing.T) {
	ks := newKitStore(t)
	if _, err := ks.Save(sampleKit()); err != nil {
		t.Fatal(err)
	}
	// Stored as the real artifact: kits/<id>/spec.yaml.
	if _, err := os.Stat(filepath.Join(ks.Dir("ruff-lint"), "spec.yaml")); err != nil {
		t.Fatalf("kit not stored as kits/<id>/spec.yaml: %v", err)
	}
	got, err := ks.Get("ruff-lint")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Ruff" || len(got.Commands.Install) != 1 {
		t.Errorf("round-tripped kit lost data: %+v", got)
	}
	list, err := ks.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List() = %d kits, want 1", len(list))
	}
	if err := ks.Delete("ruff-lint"); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Get("ruff-lint"); err != ErrKitNotFound {
		t.Errorf("Get after Delete = %v, want ErrKitNotFound", err)
	}
	if err := ks.Delete("ruff-lint"); err != nil {
		t.Errorf("deleting a missing kit should be a no-op, got %v", err)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	if _, err := newKitStore(t).Get("nope"); err != ErrKitNotFound {
		t.Errorf("Get(missing) = %v, want ErrKitNotFound", err)
	}
}

func TestListEmptyStore(t *testing.T) {
	got, err := newKitStore(t).List()
	if err != nil {
		t.Fatalf("List on an empty store should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List() = %v, want empty", got)
	}
}

// Kits are hand-editable files; one bad kit must not make the whole picker fail.
func TestListSkipsUnreadableKits(t *testing.T) {
	ks := newKitStore(t)
	if _, err := ks.Save(sampleKit()); err != nil {
		t.Fatal(err)
	}
	broken := ks.Dir("broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "spec.yaml"), []byte("\tnot: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := ks.List()
	if err != nil {
		t.Fatalf("List should skip a broken kit, not fail: %v", err)
	}
	if len(list) != 1 || list[0].Name != "ruff-lint" {
		t.Errorf("List() = %v, want only the valid kit", list)
	}
}

func TestToSpecAndRefs(t *testing.T) {
	spec, err := sampleKit().ToSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.GetId() != "ruff-lint" || !strings.Contains(spec.GetSpecYaml(), "kind: mixin") {
		t.Errorf("ToSpec = %+v", spec)
	}
	ref, err := sampleKit().ToRef()
	if err != nil {
		t.Fatal(err)
	}
	if ref.GetSpec().GetId() != "ruff-lint" {
		t.Errorf("ToRef lost the spec: %+v", ref)
	}
	if got := KitSourceRef("  ghcr.io/org/kit:1.0 ").GetSource(); got != "ghcr.io/org/kit:1.0" {
		t.Errorf("KitSourceRef = %q, want it trimmed", got)
	}
}
