package store

import (
	"errors"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func newConfigs(t *testing.T) *ConfigStore {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s.Configs()
}

func TestConfigSaveGetListDelete(t *testing.T) {
	cs := newConfigs(t)
	now := time.Unix(1000, 0)

	if _, err := cs.Save(&Configuration{Name: "  "}, now); err == nil {
		t.Error("expected error saving config with blank name")
	}

	saved, err := cs.Save(&Configuration{
		Name:       "Feature Work",
		KitOptions: map[string]string{"network": `"host"`},
		Agent:      &AgentSpec{Kind: "claude-code"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != "feature-work" {
		t.Errorf("slug id = %q, want feature-work", saved.ID)
	}
	if saved.SeedingMode != "duplicate" {
		t.Errorf("default seeding mode = %q", saved.SeedingMode)
	}

	got, err := cs.Get("feature-work")
	if err != nil {
		t.Fatal(err)
	}
	if got.KitOptions["network"] != `"host"` || got.Agent.Kind != "claude-code" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	if _, err := cs.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}

	// A second config; List is name-ordered.
	if _, err := cs.Save(&Configuration{Name: "Alpha"}, now); err != nil {
		t.Fatal(err)
	}
	list, err := cs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "Alpha" || list[1].Name != "Feature Work" {
		t.Fatalf("List order wrong: %v", names(list))
	}

	if err := cs.Delete("feature-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Get("feature-work"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete = %v, want ErrNotFound", err)
	}
	// Delete missing is a no-op.
	if err := cs.Delete("feature-work"); err != nil {
		t.Errorf("delete missing should be nil: %v", err)
	}
}

func TestEditUpdatesTimestamp(t *testing.T) {
	cs := newConfigs(t)
	t1 := time.Unix(1000, 0)
	t2 := time.Unix(2000, 0)
	saved, _ := cs.Save(&Configuration{Name: "x"}, t1)
	saved.KitOptions = map[string]string{"cpus": "4"}
	again, err := cs.Save(saved, t2)
	if err != nil {
		t.Fatal(err)
	}
	if !again.UpdatedAt.Equal(t2) {
		t.Errorf("UpdatedAt not refreshed on edit: %v", again.UpdatedAt)
	}
	reloaded, _ := cs.Get(saved.ID)
	if reloaded.KitOptions["cpus"] != "4" {
		t.Errorf("edit not persisted: %+v", reloaded)
	}
}

func TestToSnapshot(t *testing.T) {
	cfg := &Configuration{
		Name:        "c",
		KitOptions:  map[string]string{"k": "1"},
		SeedingMode: "clone",
		Agent:       &AgentSpec{Kind: "claude-code", Model: "opus"},
	}
	snap := cfg.ToSnapshot()
	if snap.GetSeedingMode() != pb.SeedingMode_SEEDING_MODE_CLONE {
		t.Error("clone mode not mapped")
	}
	if snap.GetAgent().GetModel() != "opus" {
		t.Error("agent not mapped")
	}

	// No agent => snapshot agent is nil (user picks at launch, FR-016b).
	plain := (&Configuration{Name: "p"}).ToSnapshot()
	if plain.GetAgent() != nil {
		t.Error("expected nil agent when config omits one")
	}
	if plain.GetSeedingMode() != pb.SeedingMode_SEEDING_MODE_DUPLICATE {
		t.Error("default snapshot mode should be duplicate")
	}
}

func names(list []*Configuration) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.Name
	}
	return out
}
