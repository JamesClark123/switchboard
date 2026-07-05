package resources

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckWarnsOnLowDisk(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "big"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := AvailableBytesFunc
	defer func() { AvailableBytesFunc = orig }()

	// Plenty of space -> OK.
	AvailableBytesFunc = func(string) (uint64, error) { return 1 << 30, nil }
	rep, err := Check([]string{src}, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || len(rep.Warnings) != 0 {
		t.Errorf("expected OK with no warnings, got %+v", rep)
	}

	// Tiny space -> warning, not OK.
	AvailableBytesFunc = func(string) (uint64, error) { return 10, nil }
	rep, err = Check([]string{src}, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || len(rep.Warnings) == 0 {
		t.Errorf("expected low-disk warning, got %+v", rep)
	}
	if rep.RequiredBytes == 0 {
		t.Error("expected non-zero required bytes")
	}
}

func TestCheckErrorsOnMissingSource(t *testing.T) {
	if _, err := Check([]string{"/no/such/path/xyz"}, t.TempDir()); err == nil {
		t.Error("expected Check error when a source is missing")
	}
}

// TestRealAvailableBytes exercises the production syscall-backed free-space probe
// against a real directory.
func TestRealAvailableBytes(t *testing.T) {
	n, err := availableBytes(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected non-zero free space on the temp filesystem")
	}
	if _, err := availableBytes("/definitely/not/a/real/path/xyz"); err == nil {
		t.Error("expected error for a non-existent path")
	}
}
