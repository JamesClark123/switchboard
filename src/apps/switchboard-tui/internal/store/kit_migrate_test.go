package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A legacy (schema 1) kit as the previous release wrote it: every v1 section,
// plus a field with no schema 2 equivalent.
const legacyKitYAML = `schemaVersion: "1"
kind: mixin
name: legacy
displayName: Legacy
network:
  allowedDomains: [github.com, pypi.org]
  deniedDomains: [evil.example]
environment:
  variables:
    MODEL: gemma
  proxyManaged: [GITHUB_TOKEN, ORPHAN]
credentials:
  sources:
    github:
      env: [GITHUB_TOKEN, GH_TOKEN]
    stripe:
      env: []
commands:
  install:
    - command: pip install ruff
      user: "0"
  initFiles:
    - path: /home/agent/x.sh
      content: "echo hi"
      mode: "0755"
  startup:
    - command: [sh, -c, "echo up"]
      background: true
agentContext: |
  Ruff is preinstalled.
settings:
  persistence: true
`

func TestMigrateV1CarriesEverySectionAndReportsDrops(t *testing.T) {
	ks := newKitStore(t)
	dir := ks.Dir("legacy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), []byte(legacyKitYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	kit, mig, err := ks.GetWithMigration("legacy")
	if err != nil {
		t.Fatalf("a legacy kit must load: %v", err)
	}
	if mig == nil || mig.FromVersion != "1" {
		t.Fatalf("expected a migration report, got %+v", mig)
	}
	if kit.SchemaVersion != "2" || kit.Kind != "mixin" || kit.Name != "legacy" || kit.DisplayName != "Legacy" {
		t.Errorf("identity = %+v", kit)
	}
	if kit.Permissions == nil || strings.Join(kit.Permissions.Network.Allow, ",") != "github.com,pypi.org" || kit.Permissions.Network.Deny[0] != "evil.example" {
		t.Errorf("network permissions = %+v", kit.Permissions)
	}
	if kit.Environment == nil || kit.Environment.Variables["MODEL"] != "gemma" {
		t.Errorf("environment = %+v", kit.Environment)
	}
	if len(kit.Credentials) != 2 {
		t.Fatalf("credentials = %+v", kit.Credentials)
	}
	gh := kit.Credentials[0]
	if gh.Service != "github" || gh.APIKey == nil || gh.APIKey.Name != "GITHUB_TOKEN" || !gh.APIKey.ProxyManaged || len(gh.APIKey.Inject) != 0 {
		t.Errorf("github credential = %+v", gh)
	}
	if kit.Credentials[1].Service != "stripe" || kit.Credentials[1].APIKey != nil {
		t.Errorf("stripe credential = %+v", kit.Credentials[1])
	}
	if kit.Setup == nil || len(kit.Setup.Install) != 1 || kit.Setup.Install[0].Command != "pip install ruff" ||
		len(kit.Setup.Files) != 1 || kit.Setup.Files[0].Mode != "0755" ||
		len(kit.Setup.Startup) != 1 || !kit.Setup.Startup[0].Background || kit.Setup.Startup[0].Command[2] != "echo up" {
		t.Errorf("setup = %+v", kit.Setup)
	}
	if kit.AgentInstructions == nil || !strings.Contains(kit.AgentInstructions.Content, "Ruff is preinstalled") {
		t.Errorf("agent instructions = %+v", kit.AgentInstructions)
	}
	dropped := map[string]string{}
	for _, d := range mig.Dropped {
		dropped[d.Path] = d.Reason
	}
	for _, want := range []string{"credentials.sources.github.env[GH_TOKEN]", "environment.proxyManaged[ORPHAN]", "settings"} {
		if _, ok := dropped[want]; !ok {
			t.Errorf("migration should report dropping %q; got %v", want, mig.Dropped)
		}
	}
	notes := strings.Join(mig.Notes, "\n")
	if !strings.Contains(notes, "credential github: declare the domains") || !strings.Contains(notes, "credential stripe: add an API-key") {
		t.Errorf("notes = %q", notes)
	}
	if !strings.Contains(strings.Join(mig.Summary(), "\n"), "migrated from schema 1") {
		t.Errorf("summary = %v", mig.Summary())
	}

	// Nothing on disk changed until the kit is saved.
	b, _ := os.ReadFile(filepath.Join(dir, "spec.yaml"))
	if !strings.Contains(string(b), `schemaVersion: "1"`) {
		t.Error("loading must not rewrite the file")
	}
	if _, err := ks.Save(kit); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "spec.yaml"))
	y := string(b)
	for _, unwanted := range []string{`schemaVersion: "1"`, "allowedDomains", "commands:", "initFiles", "agentContext", "proxyManaged: [", "sources:"} {
		if strings.Contains(y, unwanted) {
			t.Errorf("saved kit still carries v1 grammar %q:\n%s", unwanted, y)
		}
	}
	for _, want := range []string{`schemaVersion: "2"`, "permissions:", "setup:", "files:", "agentInstructions:", "service: github"} {
		if !strings.Contains(y, want) {
			t.Errorf("saved kit should be schema 2 (%q):\n%s", want, y)
		}
	}
	// A second load is plain schema 2: no migration.
	if _, mig2, err := ks.GetWithMigration("legacy"); err != nil || mig2 != nil {
		t.Errorf("after save: mig = %+v, err = %v; want no migration", mig2, err)
	}
}

func TestMigrateDetectsLegacyByKeysAndMemory(t *testing.T) {
	raw := map[string]any{"kind": "mixin", "name": "x", "memory": "remember"}
	if !isLegacy(raw) {
		t.Error("a `memory:` kit without a schemaVersion is legacy")
	}
	kit, mig := migrateV1(raw)
	if kit.AgentInstructions == nil || kit.AgentInstructions.Content != "remember" || mig == nil {
		t.Errorf("memory should map to agent instructions: %+v", kit.AgentInstructions)
	}
	if isLegacy(map[string]any{"schemaVersion": "2", "kind": "mixin", "setup": map[string]any{}}) {
		t.Error("a schema 2 kit must not be treated as legacy")
	}
}

func TestSchema3KitLoadsAsUnsupported(t *testing.T) {
	ks := newKitStore(t)
	dir := ks.Dir("v3")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "spec.yaml"), []byte("schemaVersion: \"3\"\nkind: workload\nname: v3\n"), 0o644)
	kit, mig, err := ks.GetWithMigration("v3")
	if err != nil || mig != nil {
		t.Fatalf("v3 should load (as unsupported) without migration: %v / %+v", err, mig)
	}
	if kit.Unsupported == "" || !strings.Contains(kit.Unsupported, "schema 3") {
		t.Errorf("Unsupported = %q", kit.Unsupported)
	}
	if _, err := kit.SpecYAML(); err == nil {
		t.Error("an unsupported kit must not render")
	}
	list, err := ks.List()
	if err != nil || len(list) != 1 || list[0].Unsupported == "" {
		t.Errorf("v3 kits are listed (not editable): %v / %v", list, err)
	}
}

func TestAttachBlockers(t *testing.T) {
	ok := &Kit{Name: "k", Environment: &KitEnvironment{Variables: map[string]string{"A": "1"}},
		Setup:       &KitSetup{Install: []KitInstallCommand{{Command: "x"}}},
		Permissions: &KitPermissions{Network: &KitNetworkPerms{Allow: []string{"a.com"}}}}
	if got := ok.AttachBlockers(); len(got) != 0 {
		t.Errorf("env+install+allow must attach: %v", got)
	}
	bad := &Kit{Name: "k",
		Permissions:       &KitPermissions{Network: &KitNetworkPerms{Deny: []string{"b.com"}}},
		Setup:             &KitSetup{Files: []KitInitFile{{Path: "/x"}}, Startup: []KitStartupCommand{{Command: []string{"x"}}}},
		Credentials:       []KitCredential{{Service: "gh"}},
		AgentInstructions: &KitAgentInstructions{Content: "hi"}}
	want := "permissions.network.deny,setup.files,setup.startup,credentials,agentInstructions"
	if got := strings.Join(bad.AttachBlockers(), ","); got != want {
		t.Errorf("blockers = %q, want %q", got, want)
	}
}
