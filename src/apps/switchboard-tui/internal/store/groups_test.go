package store

import (
	"errors"
	"testing"
)

func newGroups(t *testing.T) *GroupStore {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s.Groups()
}

func TestGroupSaveGetListDelete(t *testing.T) {
	gs := newGroups(t)

	if _, err := gs.Save(Group{Name: "  "}); err == nil {
		t.Error("blank name should fail")
	}

	a, err := gs.Save(Group{Name: "Backend"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "backend" || a.Order != 0 {
		t.Errorf("save derived id/order wrong: %+v", a)
	}
	b, _ := gs.Save(Group{Name: "Frontend"})
	if b.Order != 1 {
		t.Errorf("second group order = %d, want 1", b.Order)
	}

	// Duplicate names are allowed (distinct ids).
	dup, err := gs.Save(Group{ID: "backend-2", Name: "Backend"})
	if err != nil {
		t.Fatal(err)
	}
	if dup.ID != "backend-2" {
		t.Errorf("explicit id = %q", dup.ID)
	}

	list, err := gs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != "backend" {
		t.Fatalf("list order wrong: %+v", list)
	}

	if _, err := gs.Get("missing"); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("Get missing = %v", err)
	}

	if err := gs.Delete("frontend"); err != nil {
		t.Fatal(err)
	}
	if l, _ := gs.List(); len(l) != 2 {
		t.Errorf("expected 2 after delete, got %d", len(l))
	}
	if err := gs.Delete("frontend"); err != nil {
		t.Errorf("delete missing should be nil: %v", err)
	}
}

func TestGroupMembershipCrossHost(t *testing.T) {
	gs := newGroups(t)
	if _, err := gs.Save(Group{ID: "g", Name: "Mixed"}); err != nil {
		t.Fatal(err)
	}

	m1 := GroupMember{HostID: "local", SandboxID: "sb1"}
	m2 := GroupMember{HostID: "build-box", SandboxID: "sb2"} // a different host (FR-002c)

	if err := gs.AddMember("g", m1); err != nil {
		t.Fatal(err)
	}
	if err := gs.AddMember("g", m2); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if err := gs.AddMember("g", m1); err != nil {
		t.Fatal(err)
	}
	got, _ := gs.Get("g")
	if len(got.Members) != 2 {
		t.Fatalf("expected 2 cross-host members, got %d", len(got.Members))
	}

	if err := gs.AddMember("missing", m1); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("AddMember missing = %v", err)
	}

	// Remove one member; the other (and the sandbox) is untouched.
	if err := gs.RemoveMember("g", m1); err != nil {
		t.Fatal(err)
	}
	got, _ = gs.Get("g")
	if len(got.Members) != 1 || got.Members[0] != m2 {
		t.Errorf("remove failed: %+v", got.Members)
	}
	if err := gs.RemoveMember("missing", m1); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("RemoveMember missing = %v", err)
	}
}

func TestGroupPruneStaleMembers(t *testing.T) {
	gs := newGroups(t)
	if _, err := gs.Save(Group{ID: "g", Name: "G", Members: []GroupMember{
		{HostID: "local", SandboxID: "alive"},
		{HostID: "local", SandboxID: "gone"},
	}}); err != nil {
		t.Fatal(err)
	}

	alive := map[string]bool{"alive": true}
	pruned, err := gs.Prune(func(m GroupMember) bool { return alive[m.SandboxID] })
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}
	got, _ := gs.Get("g")
	if len(got.Members) != 1 || got.Members[0].SandboxID != "alive" {
		t.Errorf("prune left wrong members: %+v", got.Members)
	}

	// A no-op prune writes nothing and reports zero.
	again, err := gs.Prune(func(GroupMember) bool { return true })
	if err != nil || again != 0 {
		t.Errorf("no-op prune = %d / %v", again, err)
	}
}
