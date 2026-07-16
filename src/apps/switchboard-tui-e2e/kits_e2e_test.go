//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedSource makes a directory whose child "proj" is a launch candidate.
func seedSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proj", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// launchOne drives the wizard to a running sandbox.
func launchOne(t *testing.T, p *ptyProcess) {
	t.Helper()
	p.expect(t, "Switchboard", 20*time.Second)
	p.send("n")
	p.expect(t, "Launch sandbox", 10*time.Second)
	p.expect(t, "proj", 10*time.Second)
	p.send(" ")
	p.send("\r")
	p.expect(t, "RUNNING", 15*time.Second)
}

// TestTUIRefreshConfirmationE2E drives the real binaries: F must open a
// confirmation naming the consequence, and cancelling must leave the sandbox alone
// (FR-030/031).
func TestTUIRefreshConfirmationE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock := startDaemon(t, daemon, stubSbx(t))

	p := spawnTUI(t, tui, sock, seedSource(t))
	launchOne(t, p)

	p.send("F") // refresh the selected sandbox
	p.expect(t, "Refresh sandbox?", 10*time.Second)
	p.expect(t, "uncommitted", 10*time.Second) // the consequence is spelled out
	p.expect(t, "proj", 10*time.Second)        // the repo being re-copied is named
	p.expect(t, "cannot be undone", 5*time.Second)

	// Cancel, then re-open. The dialog can only be opened from the list (inside it,
	// "F" is ignored), so the second dialog appearing is itself proof the cancel
	// returned us to the list. Asserting a repainted "RUNNING" would not work:
	// Bubble Tea only writes CHANGED lines, so returning to an identical list
	// screen produces no new output at all.
	p.send("n")
	time.Sleep(500 * time.Millisecond) // let the cancel render before marking

	m := p.mark()
	p.send("F")
	p.expectNew(t, m, "Refresh sandbox?", 10*time.Second)

	// Confirming runs the re-seed; the status line reports it.
	m = p.mark()
	p.send("y")
	p.expectNew(t, m, "refreshed", 20*time.Second)

	p.send("q")
}

// TestTUIKitAuthoringE2E authors a kit through the real TUI and asserts the file it
// writes is a valid kit artifact (FR-034).
func TestTUIKitAuthoringE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock := startDaemon(t, daemon, stubSbx(t))
	configDir := t.TempDir()

	p := spawnTUIWithConfig(t, tui, sock, seedSource(t), configDir)
	p.expect(t, "Switchboard", 20*time.Second)

	p.send("K") // kit manager
	p.expect(t, "Agent kits", 10*time.Second)

	p.send("n") // new kit
	p.expect(t, "New kit", 10*time.Second)
	p.expect(t, "Install commands", 5*time.Second)

	// Identity: name the kit. Each step waits for a FRESH redraw (expectNew) rather
	// than matching text still on screen from the previous one, which would let the
	// test race ahead of the app.
	p.send("\r") // open the Identity form
	p.expect(t, "Display name", 5*time.Second)

	m := p.mark()
	p.send("e2e-kit")
	p.expectNew(t, m, "e2e-kit", 5*time.Second) // the name reached the field

	m = p.mark()
	p.send("\x13") // ctrl+s applies the form
	p.expectNew(t, m, "Install commands", 5*time.Second)

	// Add an install command.
	p.send("j")  // -> Install commands
	p.send("\r") // drill in
	m = p.mark()
	p.send("a") // add
	p.expectNew(t, m, "run once at creation", 5*time.Second)

	m = p.mark()
	p.send("apt-get install -y jq")
	p.expectNew(t, m, "apt-get install -y jq", 5*time.Second)

	m = p.mark()
	p.send("\x13")                                            // apply the item
	p.expectNew(t, m, "apt-get install -y jq", 5*time.Second) // now listed as an item

	// ctrl+s saves from inside a section too — the user has just added their last
	// command and should not have to esc out first.
	m = p.mark()
	p.send("\x13")
	p.expectNew(t, m, "saved kit e2e-kit", 10*time.Second)

	// The stored artifact is a real spec.yaml the host sbx could consume.
	specPath := filepath.Join(configDir, "kits", "e2e-kit", "spec.yaml")
	b, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("kit not written to %s: %v", specPath, err)
	}
	spec := string(b)
	for _, want := range []string{`schemaVersion: "1"`, "kind: mixin", "name: e2e-kit", "apt-get install -y jq"} {
		if !strings.Contains(spec, want) {
			t.Errorf("spec.yaml missing %q; got:\n%s", want, spec)
		}
	}

	p.send("q")
}

// TestTUIKitAttachE2E attaches an authored kit to a running sandbox through the
// real binaries (FR-033).
func TestTUIKitAttachE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock := startDaemon(t, daemon, stubSbx(t))
	configDir := t.TempDir()

	// Pre-seed a kit rather than re-authoring it through the UI.
	kitDir := filepath.Join(configDir, "kits", "pre-made")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := "schemaVersion: \"1\"\nkind: mixin\nname: pre-made\ndisplayName: Pre Made\ncommands:\n  install:\n    - command: echo hi\n"
	if err := os.WriteFile(filepath.Join(kitDir, "spec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	p := spawnTUIWithConfig(t, tui, sock, seedSource(t), configDir)
	launchOne(t, p)

	p.send("A") // attach a kit to the selected sandbox
	p.expect(t, "Attach kit to", 10*time.Second)
	p.expect(t, "Pre Made", 5*time.Second)

	p.send("\r") // choose it
	p.expect(t, "Attach kit?", 10*time.Second)
	p.expect(t, "restarts", 5*time.Second) // the restart is disclosed

	p.send("y")
	p.expect(t, "attached kit pre-made", 20*time.Second)

	p.send("q")
}
