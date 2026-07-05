package store

import (
	"errors"
	"testing"
	"time"
)

func newHosts(t *testing.T) *HostStore {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s.Hosts()
}

func TestHostSaveValidateGetList(t *testing.T) {
	hs := newHosts(t)

	// Invalid kinds / missing targets are rejected.
	if _, err := hs.Save(KnownHost{Kind: "bogus", DisplayName: "x"}); err == nil {
		t.Error("expected error for invalid kind")
	}
	if _, err := hs.Save(KnownHost{Kind: "local", DisplayName: "x"}); err == nil {
		t.Error("local without socket_path should fail")
	}
	if _, err := hs.Save(KnownHost{Kind: "ssh", DisplayName: "x", SSHTarget: "h", SocketPath: "/s"}); err == nil {
		t.Error("ssh with socket_path should fail")
	}

	local, err := hs.Save(KnownHost{Kind: "local", DisplayName: "localhost", SocketPath: "/run/s.sock"})
	if err != nil {
		t.Fatal(err)
	}
	if local.ID != "localhost" {
		t.Errorf("derived id = %q", local.ID)
	}

	// ssh host with derived id from target.
	ssh, err := hs.Save(KnownHost{Kind: "ssh", SSHTarget: "user@build-box", SSHOptions: []string{"-i", "key"}})
	if err != nil {
		t.Fatal(err)
	}
	if ssh.ID != "userbuild-box" {
		t.Errorf("ssh derived id = %q", ssh.ID)
	}

	got, err := hs.Get("localhost")
	if err != nil || got.SocketPath != "/run/s.sock" {
		t.Fatalf("Get localhost = %v / %q", err, got.SocketPath)
	}
	if _, err := hs.Get("missing"); !errors.Is(err, ErrHostNotFound) {
		t.Errorf("Get missing = %v, want ErrHostNotFound", err)
	}

	list, err := hs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(list))
	}

	// Summaries render kind-appropriately.
	if got.Summary() != "local /run/s.sock" {
		t.Errorf("local summary = %q", got.Summary())
	}
	if ssh.Summary() != "ssh user@build-box -i key" {
		t.Errorf("ssh summary = %q", ssh.Summary())
	}
}

func TestHostLastConnectedZero(t *testing.T) {
	h := KnownHost{Kind: "local", SocketPath: "/s"}
	if !h.LastConnected().IsZero() {
		t.Error("never-connected host should report the zero time")
	}
}

func TestHostUpsertAndDelete(t *testing.T) {
	hs := newHosts(t)
	if _, err := hs.Save(KnownHost{ID: "h", Kind: "ssh", DisplayName: "box", SSHTarget: "a"}); err != nil {
		t.Fatal(err)
	}
	// Re-save same id updates in place (no duplicate).
	if _, err := hs.Save(KnownHost{ID: "h", Kind: "ssh", DisplayName: "box2", SSHTarget: "b"}); err != nil {
		t.Fatal(err)
	}
	list, _ := hs.List()
	if len(list) != 1 || list[0].SSHTarget != "b" || list[0].DisplayName != "box2" {
		t.Fatalf("upsert failed: %+v", list)
	}

	if err := hs.Delete("h"); err != nil {
		t.Fatal(err)
	}
	if list, _ := hs.List(); len(list) != 0 {
		t.Errorf("delete failed: %d remain", len(list))
	}
	if err := hs.Delete("h"); err != nil {
		t.Errorf("delete missing should be nil: %v", err)
	}
}

func TestHostTouchAndEnsureLocal(t *testing.T) {
	hs := newHosts(t)

	if err := hs.Touch("nope", time.Unix(1, 0)); !errors.Is(err, ErrHostNotFound) {
		t.Errorf("Touch missing = %v", err)
	}

	first, err := hs.EnsureLocal("/run/a.sock")
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "local" {
		t.Error("EnsureLocal should create a local host")
	}
	// Idempotent: a second call returns the existing local host.
	again, err := hs.EnsureLocal("/run/b.sock")
	if err != nil {
		t.Fatal(err)
	}
	if again.SocketPath != "/run/a.sock" {
		t.Errorf("EnsureLocal should not replace existing local host; got %q", again.SocketPath)
	}

	when := time.Unix(5000, 0)
	if err := hs.Touch(first.ID, when); err != nil {
		t.Fatal(err)
	}
	got, _ := hs.Get(first.ID)
	if !got.LastConnected().Equal(when) {
		t.Errorf("Touch did not record time: %v", got.LastConnected())
	}
}
