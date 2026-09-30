//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sbxCalls returns the stub sbx's recorded invocations (one argv per line).
func sbxCalls(t *testing.T, sbxDir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sbxDir, "sbx.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// assertNoStopSince fails if the stub sbx saw a lifecycle stop/rm/start since
// the given call count — the FR-054 guarantee that an edit never restarts.
func assertNoStopSince(t *testing.T, sbxDir string, since int, during string) {
	t.Helper()
	calls := sbxCalls(t, sbxDir)
	if len(calls) < since {
		return
	}
	for _, c := range calls[since:] {
		verb := strings.Fields(c)
		if len(verb) == 0 {
			continue
		}
		switch verb[0] {
		case "stop", "rm", "start", "create", "kit":
			t.Fatalf("%s must not touch the container, but sbx saw %q", during, c)
		}
	}
}

// onlyWorkspace returns the single sandbox workspace under the daemon's root.
func onlyWorkspace(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one workspace under %s, got %v (%v)", root, entries, err)
	}
	return filepath.Join(root, entries[0].Name())
}

// TestTUIEditSourcesE2E drives the full feature-007 arc through the real
// binaries: launch seeded {proj} → S add lib → F refresh re-seeds {proj, lib} →
// S remove proj behind the confirm → {lib} remains — asserting that neither edit
// ever stopped the sandbox (FR-054) and that the record drove the refresh (FR-064).
func TestTUIEditSourcesE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock, wsRoot := startDaemonAt(t, daemon, sbxDir)

	srcRoot := seedSource(t) // holds "proj"
	p := spawnTUI(t, tui, sock, srcRoot)
	launchOne(t, p)
	ws := onlyWorkspace(t, wsRoot)
	if _, err := os.Stat(filepath.Join(ws, "proj", "main.go")); err != nil {
		t.Fatalf("launch did not seed proj: %v", err)
	}

	// A second folder appears on the host after launch.
	if err := os.MkdirAll(filepath.Join(srcRoot, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "lib", "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// --- add lib on the live sandbox ---
	before := len(sbxCalls(t, sbxDir))
	p.send("S")
	p.expect(t, "Sources ·", 10*time.Second)
	p.expect(t, "/proj", 5*time.Second) // the recorded folder, with its path

	m := p.mark()
	p.send("a")
	p.expectNew(t, m, "Add folders from", 10*time.Second)
	p.expect(t, "lib/", 5*time.Second) // the browser lists the new directory (sorted first)
	p.send(" ")                        // select lib (the cursor starts on the first entry)
	m = p.mark()
	p.send("\r")
	p.expectNew(t, m, "added lib", 20*time.Second)

	if _, err := os.Stat(filepath.Join(ws, "lib", "lib.go")); err != nil {
		t.Fatalf("add did not land lib in the workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".switchboard", "staging")); !os.IsNotExist(err) {
		t.Error("staging area should be gone after a completed add")
	}
	if _, err := os.Stat(filepath.Join(ws, "proj", "main.go")); err != nil {
		t.Error("the original seeded folder must be untouched by an add")
	}
	assertNoStopSince(t, sbxDir, before, "adding a folder")
	p.send("\x1b") // close the overlay
	time.Sleep(300 * time.Millisecond)

	// --- refresh re-seeds exactly the edited set ---
	m = p.mark()
	p.send("F")
	p.expectNew(t, m, "Refresh sandbox?", 10*time.Second)
	p.expect(t, "lib", 5*time.Second) // the added folder is in the confirm's list
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "refreshed", 20*time.Second)
	for _, f := range []string{filepath.Join("proj", "main.go"), filepath.Join("lib", "lib.go")} {
		if _, err := os.Stat(filepath.Join(ws, f)); err != nil {
			t.Errorf("refresh should re-seed the edited set; missing %s: %v", f, err)
		}
	}

	// --- remove proj behind the confirm ---
	time.Sleep(300 * time.Millisecond)
	before = len(sbxCalls(t, sbxDir))
	m = p.mark()
	p.send("S")
	p.expectNew(t, m, "Sources ·", 10*time.Second)
	p.send(" ") // mark proj (first recorded folder)
	m = p.mark()
	p.send("d")
	p.expectNew(t, m, "Remove seeded folders?", 10*time.Second)
	p.expect(t, "/proj", 5*time.Second)
	p.expect(t, "uncommitted", 5*time.Second)
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "removed proj", 20*time.Second)

	if _, err := os.Stat(filepath.Join(ws, "proj")); !os.IsNotExist(err) {
		t.Errorf("removal should delete proj's copy, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "lib", "lib.go")); err != nil {
		t.Error("the surviving folder must be intact")
	}
	if _, err := os.Stat(filepath.Join(srcRoot, "proj", "main.go")); err != nil {
		t.Error("the origin must never be touched")
	}
	assertNoStopSince(t, sbxDir, before, "removing a folder")

	p.send("\x1b")
	p.send("q")
}
