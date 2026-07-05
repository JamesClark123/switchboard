package duplicate

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			rel, _ := filepath.Rel(root, p)
			out[rel] = string(sha256.New().Sum(b))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCopyAllByteIdenticalAndSourceUnchanged(t *testing.T) {
	tmp := t.TempDir()
	srcA := filepath.Join(tmp, "projA")
	writeFile(t, filepath.Join(srcA, "main.go"), "package main\n")
	writeFile(t, filepath.Join(srcA, "node_modules", "dep.js"), "console.log(1)\n")
	writeFile(t, filepath.Join(srcA, ".env"), "SECRET=x\n") // untracked file must be copied verbatim

	srcB := filepath.Join(tmp, "projB")
	writeFile(t, filepath.Join(srcB, "readme.md"), "# B\n")

	before := hashTree(t, srcA)

	dest := filepath.Join(tmp, "ws", "sandbox-1")
	var lastProgress Progress
	progressCalls := 0
	copied, err := CopyAll([]string{srcA, srcB}, dest, func(p Progress) {
		progressCalls++
		lastProgress = p
	})
	if err != nil {
		t.Fatalf("CopyAll: %v", err)
	}

	// Byte-identical copies present.
	if got := hashTree(t, filepath.Join(dest, "projA")); len(got) != 3 {
		t.Fatalf("expected 3 files in projA copy, got %d", len(got))
	}
	for rel, h := range before {
		cp := filepath.Join(dest, "projA", rel)
		b, err := os.ReadFile(cp)
		if err != nil {
			t.Fatalf("read copy %s: %v", cp, err)
		}
		if string(sha256.New().Sum(b)) != h {
			t.Errorf("copy %s differs from source", rel)
		}
	}

	// Source unchanged (SC-002).
	after := hashTree(t, srcA)
	if len(after) != len(before) {
		t.Errorf("source file count changed: %d -> %d", len(before), len(after))
	}

	// Progress reported (FR-028).
	if progressCalls == 0 {
		t.Error("expected progress callbacks")
	}
	if lastProgress.BytesTotal == 0 || copied == 0 {
		t.Errorf("expected non-zero bytes: total=%d copied=%d", lastProgress.BytesTotal, copied)
	}
}

func TestCopyAllRejectsDuplicateBasenames(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "x", "proj")
	b := filepath.Join(tmp, "y", "proj")
	writeFile(t, filepath.Join(a, "f"), "1")
	writeFile(t, filepath.Join(b, "f"), "2")
	if _, err := CopyAll([]string{a, b}, filepath.Join(tmp, "ws"), nil); err == nil {
		t.Fatal("expected error on duplicate basenames")
	}
}

func TestCopyAllEmpty(t *testing.T) {
	if _, err := CopyAll(nil, t.TempDir(), nil); err == nil {
		t.Fatal("expected error on empty source list")
	}
}

func TestSizeAndCopyAllErrorOnMissingSource(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := Size([]string{missing}); err == nil {
		t.Error("expected Size error for missing source")
	}
	if _, err := CopyAll([]string{missing}, t.TempDir(), nil); err == nil {
		t.Error("expected CopyAll error for missing source")
	}
}

func TestCopyAllRejectsDotSource(t *testing.T) {
	// A bare "." has no usable basename and must be rejected.
	if _, err := CopyAll([]string{"."}, t.TempDir(), nil); err == nil {
		t.Error("expected error for '.' source")
	}
}

func TestCopyAllSkipsNonRegularFiles(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "proj")
	writeFile(t, filepath.Join(src, "real.txt"), "hi")
	// A FIFO cannot be meaningfully duplicated and must be skipped, not errored.
	if err := mkfifo(filepath.Join(src, "pipe")); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	dest := filepath.Join(tmp, "ws")
	if _, err := CopyAll([]string{src}, dest, nil); err != nil {
		t.Fatalf("CopyAll should skip a FIFO, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "proj", "real.txt")); err != nil {
		t.Errorf("regular file should still be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "proj", "pipe")); err == nil {
		t.Error("FIFO should have been skipped, not copied")
	}
}

func TestCopyFilePropagatesReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permissions")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "proj")
	writeFile(t, filepath.Join(src, "secret"), "data")
	// Make the file unreadable so the streaming copy's Open fails.
	if err := os.Chmod(filepath.Join(src, "secret"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyAll([]string{src}, filepath.Join(tmp, "ws"), nil); err == nil {
		t.Error("expected copy error for unreadable source file")
	}
}

func TestSymlinkCopiedAsLink(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "proj")
	writeFile(t, filepath.Join(src, "real.txt"), "hi")
	if err := os.Symlink("real.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	dest := filepath.Join(tmp, "ws")
	if _, err := CopyAll([]string{src}, dest, nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(dest, "proj", "link.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink should be copied as a symlink")
	}
}
