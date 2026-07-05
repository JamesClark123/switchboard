package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListSourceCandidates(t *testing.T) {
	dir := t.TempDir()
	// A plain dir, a git repo, and a file (ignored).
	if err := os.MkdirAll(filepath.Join(dir, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := ListSourceCandidates(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 dir candidates, got %d", len(all))
	}

	reposOnly, err := ListSourceCandidates(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(reposOnly) != 1 || !reposOnly[0].GetIsRepo() {
		t.Fatalf("expected 1 repo candidate, got %v", reposOnly)
	}

	if _, err := ListSourceCandidates(filepath.Join(dir, "nope"), false); err == nil {
		t.Error("expected error for a missing root")
	}
}

func TestWithinPathChecks(t *testing.T) {
	root := "/srv/ws"
	if !within(root, "/srv/ws/sandbox-1") {
		t.Error("a child path should be within root")
	}
	if within(root, "/etc/passwd") {
		t.Error("an unrelated absolute path must not be within root")
	}
	if within(root, "/srv/other") {
		t.Error("a sibling path must not be within root")
	}
}
