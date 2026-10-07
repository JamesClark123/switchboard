//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUIMcpGatewayE2E drives feature 008 through the real binaries: open the
// daemon screen (shift+→), enter the local daemon's MCP gateway, mark the stub's
// registered server attach-by-default, launch a sandbox and assert the daemon
// loaded the server after create (additive); switch to exclusive and assert the
// next create carries --static-mcp.
func TestTUIMcpGatewayE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	p := spawnTUI(t, tui, sock, seedSource(t))
	p.expect(t, "Switchboard", 20*time.Second)

	// shift+→ (CSI 1;2C) opens the daemon screen; enter twice reaches the gateway.
	p.send("\x1b[1;2C")
	p.expect(t, "Daemons", 10*time.Second)
	p.send("\r")
	p.expect(t, "MCP gateway", 10*time.Second)
	p.send("\r")
	p.expect(t, "mcp.notion.com", 10*time.Second)

	m := p.mark()
	p.send(" ") // mark notion attach-by-default
	p.expectNew(t, m, "will attach to every new sandbox", 10*time.Second)
	// Footer text is styled, so navigation is confirmed by the re-rendered screen
	// title; shift+← works from the daemon screen even with the option list open.
	m = p.mark()
	p.send("\x1b") // back to the daemon screen
	p.expectNew(t, m, "Daemons", 5*time.Second)
	m = p.mark()
	p.send("\x1b[1;2D")                        // shift+← back to the sandbox list
	p.expectNew(t, m, "launch", 5*time.Second) // the list footer repaints with its own keys
	launchOne(t, p)
	p.expect(t, "mcp: notion (additive)", 15*time.Second)

	calls := sbxCalls(t, sbxDir)
	var sawCreate, sawLoad bool
	for _, c := range calls {
		switch {
		case strings.HasPrefix(c, "create "):
			sawCreate = true
			if strings.Contains(c, "static-mcp") {
				t.Errorf("additive mode must not pass --static-mcp: %q", c)
			}
		case strings.HasPrefix(c, "mcp load notion --sandbox "):
			if !sawCreate {
				t.Errorf("mcp load must follow create; calls: %v", calls)
			}
			sawLoad = true
		}
	}
	if !sawLoad {
		t.Fatalf("expected an `mcp load notion --sandbox` after create; calls: %v", calls)
	}

	// Exclusive mode: the next create pre-loads the set statically.
	p.send("\x1b[1;2C")
	p.expect(t, "Daemons", 10*time.Second)
	p.send("\r")
	p.send("\r")
	p.expect(t, "mcp.notion.com", 10*time.Second)
	m = p.mark()
	p.send("m")
	p.expectNew(t, m, "exclusive", 10*time.Second)
	m = p.mark()
	p.send("\x1b") // back to the daemon screen
	p.expectNew(t, m, "Daemons", 5*time.Second)
	m = p.mark()
	p.send("\x1b[1;2D") // shift+← back to the sandbox list
	p.expectNew(t, m, "launch", 5*time.Second)
	since := len(sbxCalls(t, sbxDir))
	p.send("n")
	p.expect(t, "Launch sandbox", 10*time.Second)
	p.send(" ")
	p.send("\r")
	p.expect(t, "mcp: notion (exclusive)", 20*time.Second)
	var sawStatic bool
	for _, c := range sbxCalls(t, sbxDir)[since:] {
		if strings.HasPrefix(c, "create ") && strings.Contains(c, "--static-mcp notion") {
			sawStatic = true
		}
		if strings.HasPrefix(c, "mcp load ") {
			t.Errorf("exclusive mode must not load after create: %q", c)
		}
	}
	if !sawStatic {
		t.Errorf("expected --static-mcp notion on the exclusive-mode create; calls: %v", sbxCalls(t, sbxDir)[since:])
	}
	p.send("q")
}
