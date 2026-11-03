//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SWI-24 (FR-504..FR-523): the editor `v` opens is chosen on the settings
// screen, started detached with the sandbox's workspace folder, and a non-zero
// exit soon after is reported. A stub editor on PATH logs its argv.

// stubEditor writes `webstorm`, `logedit` and `exit3` scripts into a temp dir
// and returns the dir and the log they append their arguments to.
func stubEditor(t *testing.T) (dir, logPath string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "editor.log")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"" + logPath + "\"\n"
	for _, name := range []string{"webstorm", "logedit"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "exit3"), []byte(body+"exit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, logPath
}

// waitLog waits for the editor log to appear and returns its lines.
func waitLog(t *testing.T, logPath string) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 {
			return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the stub editor never ran (no %s)", logPath)
	return nil
}

// pickEditor opens the settings screen, the Editor list, moves down n entries
// from VS Code and confirms.
func pickEditor(t *testing.T, p *ptyProcess, down int) {
	t.Helper()
	p.send(",")
	p.expect(t, "Editor: VS Code", 10*time.Second)
	p.send("j")
	p.send("\r")
	p.expect(t, "Custom", 10*time.Second) // the list is open
	for i := 0; i < down; i++ {
		p.send("j")
		time.Sleep(20 * time.Millisecond)
	}
	p.send("\r")
}

// TestTUIEditorPickAndOpenE2E: WebStorm chosen in the list, saved, and started
// by v with the daemon's workspace path (quickstart Scenario 1).
func TestTUIEditorPickAndOpenE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock, workspaceRoot := startDaemonAt(t, daemon, stubSbx(t))
	configDir := t.TempDir()
	dir, logPath := stubEditor(t)

	p := spawnTUIWithEnv(t, tui, sock, seedSource(t), configDir, []string{"PATH=" + dir + ":" + os.Getenv("PATH")})
	launchOne(t, p)

	m := p.mark()
	pickEditor(t, p, 5) // WebStorm
	p.expectNew(t, m, "saved settings", 10*time.Second)
	p.expect(t, "Editor: WebStorm", 10*time.Second)
	b, err := os.ReadFile(filepath.Join(configDir, "settings.toml"))
	if err != nil || !strings.Contains(string(b), "editor = 'webstorm'") {
		t.Fatalf("settings.toml = %q, %v", b, err)
	}
	p.send("\x1b") // back to the list
	m = p.mark()
	p.send("v")
	p.expectNew(t, m, "in WebStorm", 15*time.Second)
	lines := waitLog(t, logPath)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], workspaceRoot) {
		t.Errorf("webstorm argv = %q, want the workspace path under %s", lines, workspaceRoot)
	}
	p.send("q")
}

// TestTUIEditorCustomCommandE2E: Custom `logedit --open {path}` runs with the
// folder in place of the marker (quickstart Scenario 3).
func TestTUIEditorCustomCommandE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock, workspaceRoot := startDaemonAt(t, daemon, stubSbx(t))
	configDir := t.TempDir()
	dir, logPath := stubEditor(t)

	p := spawnTUIWithEnv(t, tui, sock, seedSource(t), configDir, []string{"PATH=" + dir + ":" + os.Getenv("PATH")})
	launchOne(t, p)

	pickEditor(t, p, 10) // Custom
	p.expect(t, "Custom command", 10*time.Second)
	p.expect(t, "{path} is the workspace folder", 10*time.Second)
	m := p.mark()
	p.send("logedit --open {path}")
	p.send("\r")
	p.expectNew(t, m, "saved settings", 10*time.Second)
	p.expect(t, "Editor: Custom: logedit", 10*time.Second)
	b, err := os.ReadFile(filepath.Join(configDir, "settings.toml"))
	if err != nil || !strings.Contains(string(b), "editor_custom_command = 'logedit --open {path}'") {
		t.Fatalf("settings.toml = %q, %v", b, err)
	}
	p.send("\x1b")
	m = p.mark()
	p.send("v")
	p.expectNew(t, m, "in Custom", 15*time.Second)
	lines := waitLog(t, logPath)
	if len(lines) != 2 || lines[0] != "--open" || !strings.HasPrefix(lines[1], workspaceRoot) {
		t.Errorf("logedit argv = %q", lines)
	}
	p.send("q")
}

// TestTUIEditorExitReportE2E: an override variable pointing at a script that
// exits 3 is reported with the status and the variable (quickstart Scenario 6).
func TestTUIEditorExitReportE2E(t *testing.T) {
	requireLocalTarget(t)
	tui, daemon := buildBinaries(t)
	sock := startDaemon(t, daemon, stubSbx(t))
	configDir := t.TempDir()
	dir, _ := stubEditor(t)

	p := spawnTUIWithEnv(t, tui, sock, seedSource(t), configDir, []string{
		"PATH=" + dir + ":" + os.Getenv("PATH"),
		"SWITCHBOARD_GOLAND_BIN=" + filepath.Join(dir, "exit3"),
	})
	launchOne(t, p)

	pickEditor(t, p, 8) // GoLand
	p.expect(t, "Editor: GoLand", 10*time.Second)
	p.send("\x1b")
	m := p.mark()
	p.send("v")
	p.expectNew(t, m, "exited with status 3", 15*time.Second)
	p.expectNew(t, m, "SWITCHBOARD_GOLAND_BIN", 5*time.Second)
	p.send("q")
}
