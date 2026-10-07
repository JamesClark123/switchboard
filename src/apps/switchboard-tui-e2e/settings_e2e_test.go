//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTUISettingsPersistE2E toggles the automatic host sign-in setting off, quits,
// relaunches with the same config dir and finds it still off — and the file the
// TUI persisted it in (FR-087/FR-101).
func TestTUISettingsPersistE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock := startDaemon(t, daemon, stubSbx(t))
	configDir := t.TempDir()

	p := spawnTUIWithConfig(t, tui, sock, seedSource(t), configDir)
	p.expect(t, "Switchboard", 20*time.Second)
	p.send(",")
	p.expect(t, "Automatic host sign-in", 10*time.Second)
	p.expect(t, "[on]", 5*time.Second)
	m := p.mark()
	p.send(" ")
	p.expectNew(t, m, "saved settings", 10*time.Second)
	p.send("\x1b")
	p.send("q")

	b, err := os.ReadFile(filepath.Join(configDir, "settings.toml"))
	if err != nil {
		t.Fatalf("settings.toml not written: %v", err)
	}
	if !strings.Contains(string(b), "auto_connect_hosts = false") {
		t.Errorf("settings.toml = %q", b)
	}

	p2 := spawnTUIWithConfig(t, tui, sock, seedSource(t), configDir)
	p2.expect(t, "Switchboard", 20*time.Second)
	p2.send(",")
	p2.expect(t, "[off]", 10*time.Second)
	p2.send("\x1b")
	p2.send("q")
}
