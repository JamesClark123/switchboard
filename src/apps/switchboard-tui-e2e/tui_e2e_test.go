//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// E2E_TARGET selects the runtime per Rule VI: "local" (default) builds and runs
// the binaries locally; "remote" is reserved for driving a deployed build and is
// skipped here (it needs a provisioned remote, documented in the package README).
func requireLocalTarget(t *testing.T) {
	if tgt := os.Getenv("E2E_TARGET"); tgt != "" && tgt != "local" {
		t.Skipf("E2E_TARGET=%q: only the local target runs in this harness", tgt)
	}
}

// TestTUILaunchFlowE2E drives the US1 happy path through a real PTY: open the
// launch wizard, select a source, launch, and confirm a RUNNING sandbox appears.
func TestTUILaunchFlowE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)

	// A directory whose child "proj" is the launch candidate.
	srcRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "proj", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := spawnTUI(t, tui, sock, srcRoot)
	p.expect(t, "Switchboard", 20*time.Second) // initial render

	p.send("n") // open the launch overlay (directory browser over the list)
	p.expect(t, "Launch sandbox", 10*time.Second)
	p.expect(t, "proj", 10*time.Second) // the browser lists srcRoot's directories

	p.send(" ")  // select the "proj" directory (space toggles; multi-select capable)
	p.send("\r") // launch the selected source(s)

	// Back on the list, the running sandbox appears.
	p.expect(t, "RUNNING", 10*time.Second)

	p.send("q") // quit
}

// TestTUIHostsFlowE2E drives the US3 hosts screen: open it and confirm the local
// host is listed and connectable.
func TestTUIHostsFlowE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)

	srcRoot := t.TempDir()
	p := spawnTUI(t, tui, sock, srcRoot)
	p.expect(t, "Switchboard", 20*time.Second)

	p.send("h") // open hosts screen
	p.expect(t, "Hosts", 10*time.Second)
	p.expect(t, "localhost", 10*time.Second)

	p.send("\x1b") // esc back to list
	p.send("q")    // quit
}
