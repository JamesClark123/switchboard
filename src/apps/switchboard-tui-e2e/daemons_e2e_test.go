//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// TestTUIDaemonScreenE2E reaches the daemon screen with shift+→ through the real
// binaries: the local daemon is listed as connected with its sbx version, its
// option list offers the MCP gateway, and shift+← returns to the sandbox list.
func TestTUIDaemonScreenE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock := startDaemon(t, daemon, stubSbx(t))
	p := spawnTUI(t, tui, sock, seedSource(t))
	launchOne(t, p)

	p.send("\x1b[1;2C") // shift+→
	p.expect(t, "Daemons", 10*time.Second)
	p.expect(t, "CONNECTED", 5*time.Second)
	p.expect(t, "sbx sbx version 0.46.0", 5*time.Second)
	p.send("\r")
	p.expect(t, "MCP gateway", 5*time.Second)
	m := p.mark()
	p.send("\x1b[1;2D") // shift+← works even with the option list open
	p.expectNew(t, m, "RUNNING", 10*time.Second)
	p.send("q")
}
