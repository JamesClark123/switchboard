//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openGateway takes a fresh TUI to the local daemon's MCP gateway view.
func openGateway(t *testing.T, p *ptyProcess) {
	t.Helper()
	p.expect(t, "Switchboard", 20*time.Second)
	p.send("\x1b[1;2C")
	p.expect(t, "Daemons", 10*time.Second)
	p.send("\r")
	p.expect(t, "MCP gateway", 10*time.Second)
	m := p.mark()
	p.send("\r")
	p.expectNew(t, m, "notion", 10*time.Second)
}

// editURL opens the edit form on the highlighted server and replaces its URL;
// the form opens on Kind, so one tab reaches the URL.
func editURL(t *testing.T, p *ptyProcess, url string) {
	t.Helper()
	m := p.mark()
	p.send("e")
	p.expectNew(t, m, "Edit notion", 10*time.Second)
	p.tab()
	p.send("\x15") // ctrl+u clears the URL
	p.send(url)
}

// expectText waits for want in the styling-stripped output produced after from
// (the outcome panel styles each line, so a raw match can miss it).
func expectText(t *testing.T, p *ptyProcess, from int, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if s := p.buf.String(); strings.Contains(ansiRe.ReplaceAllString(s[from:], ""), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; screen:\n%s", want, p.tail(3000))
}

// closeOutcome closes the outcome panel and declines the authorize offer that
// follows a registered remote save.
func closeOutcome(t *testing.T, p *ptyProcess, registered bool) {
	t.Helper()
	m := p.mark()
	p.send("x")
	if registered {
		expectText(t, p, m, "Authorize notion now?")
		p.send("n")
	}
	time.Sleep(500 * time.Millisecond)
}

func hasCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// TestTUIMcpEditURLAndHeaderSecretE2E (SWI-21 US1/US2): edit the URL, type a
// literal credential header, accept the one-step conversion, and save. The
// daemon removes, stores the secret on stdin, then adds with the placeholder;
// the value never reaches argv.
func TestTUIMcpEditURLAndHeaderSecretE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	p := spawnTUI(t, tui, sock, seedSource(t))
	openGateway(t, p)

	editURL(t, p, "https://new.example/mcp")
	p.tab()
	p.send("Authorization: Bearer e2e-literal-77")
	m := p.mark()
	p.send("\x13") // ctrl+s
	expectText(t, p, m, "Convert Authorization to a secret?")
	m = p.mark()
	p.send("y") // converting on save continues the save
	expectText(t, p, m, "secrets created: mcp:notion:authorization")

	calls := sbxCalls(t, sbxDir)
	rm, set, add := -1, -1, -1
	for i, c := range calls {
		switch {
		case c == "mcp rm notion --force":
			rm = i
		case c == "secret set mcp:notion:authorization --force":
			set = i
		case strings.HasPrefix(c, "mcp add notion --url https://new.example/mcp --header Authorization: Bearer ${authorization}"):
			add = i
		}
		if strings.Contains(c, "e2e-literal-77") {
			t.Errorf("the secret value reached argv: %q", c)
		}
	}
	if rm < 0 || set < rm || add < set {
		t.Fatalf("want rm → set → add; calls: %v", calls)
	}
	if b, _ := os.ReadFile(filepath.Join(sbxDir, "secret-state", "mcp:notion:authorization")); string(b) != "e2e-literal-77\n" {
		t.Errorf("stored value = %q", b)
	}
	closeOutcome(t, p, true)
	p.send("q")
}

// TestTUIMcpFailedReplacePendingRetryCancelE2E (FR-406/FR-426): a replace whose
// add fails leaves the name unlisted with a pending replace; a second client
// sees the pending row, retries the previous definition (registered), and a
// second failed replace is cancelled from its form.
func TestTUIMcpFailedReplacePendingRetryCancelE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sbxDir := stubSbx(t)
	sock := startDaemon(t, daemon, sbxDir)
	p := spawnTUI(t, tui, sock, seedSource(t))
	openGateway(t, p)

	editURL(t, p, "https://refuse.invalid/mcp")
	m := p.mark()
	p.send("\x13")
	expectText(t, p, m, "not registered")
	if _, err := os.Stat(filepath.Join(sbxDir, "mcp-state", "notion")); err == nil {
		t.Fatal("the failed replace left notion registered")
	}

	// Another client: the pending row, then Retry with the previous definition.
	q := spawnTUI(t, tui, sock, seedSource(t))
	openGateway(t, q)
	q.expect(t, "not registered (replace failed", 10*time.Second)
	m = q.mark()
	q.send("R")
	q.expectNew(t, m, "the previous definition", 10*time.Second)
	m = q.mark()
	q.send("p")
	q.expectNew(t, m, "Retry notion", 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	m = q.mark()
	q.send("\x13")
	expectText(t, q, m, "Saved notion")
	if !hasCall(sbxCalls(t, sbxDir), "mcp add notion --url https://mcp.notion.com/mcp") {
		t.Fatalf("the retry did not add the previous URL; calls: %v", sbxCalls(t, sbxDir))
	}
	if _, err := os.Stat(filepath.Join(sbxDir, "mcp-state", "notion")); err != nil {
		t.Fatal("the retry did not register notion")
	}
	closeOutcome(t, q, true)

	// A second failed replace, cancelled from the kept form.
	editURL(t, q, "https://refuse.invalid/v2")
	m = q.mark()
	q.send("\x13")
	expectText(t, q, m, "not registered")
	closeOutcome(t, q, false)
	m = q.mark()
	q.send("\x1b")
	expectText(t, q, m, "Cancel the replace of notion?")
	m = q.mark()
	q.send("y")
	expectText(t, q, m, "cancelled the replace of notion")
	if _, err := os.Stat(filepath.Join(sbxDir, "mcp-state", "notion")); err == nil {
		t.Error("cancel left a registration")
	}
	q.send("q")
	p.send("q")
}
