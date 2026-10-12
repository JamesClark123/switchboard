//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTUISecretsE2E drives FR-104 through the real binaries: open the local
// daemon's Secrets option, store a secret through the form (the value reaches
// the stub's stdin and never its argv), then remove one behind the confirm.
func TestTUISecretsE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	p := spawnTUI(t, tui, sock, seedSource(t))
	p.expect(t, "Switchboard", 20*time.Second)

	p.send("\x1b[1;2C") // shift+→: daemon screen
	p.expect(t, "Daemons", 10*time.Second)
	p.send("\r") // options
	p.expect(t, "Secrets", 10*time.Second)
	p.send("j") // second option
	m := p.mark()
	p.send("\r")
	p.expectNew(t, m, "Secrets ·", 10*time.Second)
	p.expect(t, "github", 10*time.Second) // the stub's stored secret

	// Store: name, tab, value, ctrl+s.
	m = p.mark()
	p.send("a")
	p.expectNew(t, m, "Value", 10*time.Second)
	m = p.mark()
	p.send("mcp:gh:token")
	p.expectNew(t, m, "mcp:gh:token", 5*time.Second) // the name reached the field
	// Keys sent back-to-back can coalesce in the PTY; let the focus move land
	// before typing the masked value, and the value land before submitting.
	p.send("\t")
	time.Sleep(300 * time.Millisecond)
	p.send("s3cret-value")
	time.Sleep(300 * time.Millisecond)
	p.send("\x13") // ctrl+s
	p.expect(t, "stored mcp:gh:token globally", 20*time.Second)

	calls := sbxCalls(t, sbxDir)
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "secret set mcp:gh:token --force") {
		t.Fatalf("expected `secret set mcp:gh:token --force`; calls: %v", calls)
	}
	if strings.Contains(joined, "s3cret-value") {
		t.Error("the value must never appear in argv")
	}
	stdin, err := os.ReadFile(filepath.Join(sbxDir, "secret.stdin"))
	if err != nil || string(stdin) != "s3cret-value\n" {
		t.Errorf("stdin = %q, %v; want the value plus newline", stdin, err)
	}

	// Remove the highlighted (first) secret behind the confirm.
	m = p.mark()
	p.send("d")
	p.expectNew(t, m, "Remove secret?", 10*time.Second)
	p.send("y")
	p.expect(t, "removed github", 20*time.Second)
	if !strings.Contains(strings.Join(sbxCalls(t, sbxDir), "\n"), "secret rm github -f") {
		t.Errorf("expected `secret rm github -f`; calls: %v", sbxCalls(t, sbxDir))
	}
	p.send("\x1b")
	p.send("q")
}
