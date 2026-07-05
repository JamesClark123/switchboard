package registry

import (
	"errors"
	"os"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func timeUnix(sec int64) time.Time { return time.Unix(sec, 0) }

func writeFileForTest(path string) error { return os.WriteFile(path, []byte("x"), 0o644) }

func idsOf(list []*pb.Sandbox) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.GetId()
	}
	return out
}

func openTemp(t *testing.T) *Registry {
	t.Helper()
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestPutGetUpdateDeleteList(t *testing.T) {
	r := openTemp(t)

	if _, err := r.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
	if err := r.Put(&pb.Sandbox{}); err == nil {
		t.Fatal("expected error putting a sandbox with empty id")
	}

	a := &pb.Sandbox{Id: "a", DisplayName: "alpha", State: pb.SandboxState_SANDBOX_STATE_RUNNING, CreatedAt: timestamppb.New(timeUnix(1))}
	b := &pb.Sandbox{Id: "b", DisplayName: "beta", State: pb.SandboxState_SANDBOX_STATE_STOPPED, CreatedAt: timestamppb.New(timeUnix(2))}
	if err := r.Put(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(b); err != nil {
		t.Fatal(err)
	}

	got, err := r.Get("a")
	if err != nil || got.GetDisplayName() != "alpha" {
		t.Fatalf("Get a = %v / %q", err, got.GetDisplayName())
	}

	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].GetId() != "a" || list[1].GetId() != "b" {
		t.Fatalf("List ordering wrong: %v", idsOf(list))
	}

	upd, err := r.Update("a", func(s *pb.Sandbox) error {
		s.DisplayName = "alpha2"
		return nil
	})
	if err != nil || upd.GetDisplayName() != "alpha2" {
		t.Fatalf("Update = %v / %q", err, upd.GetDisplayName())
	}
	if _, err := r.Update("missing", func(*pb.Sandbox) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update missing = %v, want ErrNotFound", err)
	}

	if err := r.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete Get a = %v, want ErrNotFound", err)
	}
	// Deleting a missing id is not an error.
	if err := r.Delete("a"); err != nil {
		t.Fatalf("Delete missing should be nil, got %v", err)
	}
}

func TestOpenErrorOnUnwritableDir(t *testing.T) {
	// dataDir is actually a regular file, so creating registry.db under it fails.
	f := t.TempDir() + "/not-a-dir"
	if err := writeFileForTest(f); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(f); err == nil {
		t.Fatal("expected Open error when data dir is a file")
	}
}

// TestCorruptRecordsSurfaceErrors injects non-proto bytes directly into the
// bucket (white-box) so the unmarshal error branches of Get/List/Update run.
func TestCorruptRecordsSurfaceErrors(t *testing.T) {
	r := openTemp(t)
	if err := r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(sandboxBucket).Put([]byte("bad"), []byte("\xff\xff not a proto"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get("bad"); err == nil {
		t.Error("expected Get unmarshal error on corrupt record")
	}
	if _, err := r.List(); err == nil {
		t.Error("expected List unmarshal error on corrupt record")
	}
	if _, err := r.Update("bad", func(*pb.Sandbox) error { return nil }); err == nil {
		t.Error("expected Update unmarshal error on corrupt record")
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Put(&pb.Sandbox{Id: "keep", DisplayName: "k"}); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	r2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r2.Close() }()
	got, err := r2.Get("keep")
	if err != nil || got.GetDisplayName() != "k" {
		t.Fatalf("reopened Get = %v / %q", err, got.GetDisplayName())
	}
}
