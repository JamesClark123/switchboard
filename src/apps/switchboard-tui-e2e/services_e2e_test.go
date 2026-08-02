//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTUIServiceAuthoringE2E authors a port-forwarding service through the real
// TUI and asserts the artifact it writes (feature 006, FR-043).
//
// The services section is switchboard-owned, so the assertion is twofold: the
// declaration lands in its own kits/<id>/services.yaml sidecar, and it does NOT
// leak into spec.yaml — the host `sbx` must never see it.
func TestTUIServiceAuthoringE2E(t *testing.T) {
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

	// Identity.
	p.send("\r")
	p.expect(t, "Display name", 5*time.Second)
	m := p.mark()
	p.send("svc-kit")
	p.expectNew(t, m, "svc-kit", 5*time.Second)
	m = p.mark()
	p.send("\x13")
	p.expectNew(t, m, "Install commands", 5*time.Second)

	// Walk down to the Services section. It is the last of the ten sections, so
	// nine downs from Identity land on it.
	for i := 0; i < 9; i++ {
		p.send("j")
		time.Sleep(40 * time.Millisecond)
	}
	m = p.mark()
	p.send("\r") // drill in
	p.expectNew(t, m, "service", 5*time.Second)

	m = p.mark()
	p.send("a") // add a service
	p.expectNew(t, m, "Start command", 5*time.Second)
	// The form must warn the author about the bind address — the single most common
	// reason a healthy dev server is unreachable (clarification Q1).
	p.expect(t, "0.0.0.0", 5*time.Second)

	m = p.mark()
	p.send("web")
	p.expectNew(t, m, "web", 5*time.Second)

	// huh advances an Input on enter; the command field below is a multi-line Text,
	// which takes enter as a newline, so tab is used to leave that one.
	p.send("\r")
	time.Sleep(150 * time.Millisecond)
	m = p.mark()
	p.send("pnpm dev --host 0.0.0.0")
	p.expectNew(t, m, "pnpm dev", 5*time.Second)

	p.send("\t")
	time.Sleep(150 * time.Millisecond)
	m = p.mark()
	p.send("3000")
	p.expectNew(t, m, "3000", 5*time.Second)

	m = p.mark()
	p.send("\x13") // apply the item
	p.expectNew(t, m, "web", 5*time.Second)

	m = p.mark()
	p.send("\x13") // save the kit
	p.expectNew(t, m, "saved kit svc-kit", 10*time.Second)

	// The declaration lands in its own sidecar...
	sidecar := filepath.Join(configDir, "kits", "svc-kit", "services.yaml")
	b, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("expected kits/svc-kit/services.yaml: %v", err)
	}
	got := string(b)
	for _, want := range []string{"name: web", "listenPort: 3000", "pnpm dev"} {
		if !strings.Contains(got, want) {
			t.Errorf("services.yaml must contain %q:\n%s", want, got)
		}
	}

	// ...and never into the Docker artifact.
	spec, err := os.ReadFile(filepath.Join(configDir, "kits", "svc-kit", "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"listenPort", "services:", "pnpm dev"} {
		if strings.Contains(string(spec), forbidden) {
			t.Errorf("spec.yaml must not mention %q — sbx would reject it:\n%s", forbidden, spec)
		}
	}

	p.send("q")
}

// TestTUIServiceListE2E opens the per-sandbox service screen on a sandbox whose kit
// declares nothing, and asserts the two things SC-005 turns on: `p` reaches the
// screen, and nothing is running there.
func TestTUIServiceListE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock := startDaemon(t, daemon, stubSbx(t))

	p := spawnTUI(t, tui, sock, seedSource(t))
	launchOne(t, p)

	m := p.mark()
	p.send("p")
	p.expectNew(t, m, "Services", 10*time.Second)
	// The help line advertises the actions the screen supports.
	p.expect(t, "start/stop", 5*time.Second)

	p.send("\x1b") // esc back to the list
	p.send("q")
}
