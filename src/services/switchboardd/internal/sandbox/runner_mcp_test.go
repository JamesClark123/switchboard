package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loggingSbx writes a stub `sbx` that records every argv line to <dir>/sbx.log.
func loggingSbx(t *testing.T) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "sbx")
	logPath = filepath.Join(dir, "sbx.log")
	script := "#!/usr/bin/env bash\necho \"$*\" >> \"" + logPath + "\"\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

// Feature 008: the exclusive attach mode rides `sbx create` as one --static-mcp
// flag after the kit flags; the additive mode uses the documented `mcp load`.
func TestSbxRunnerStaticMcpAndLoadArgv(t *testing.T) {
	bin, logPath := loggingSbx(t)
	r := &SbxRunner{Bin: bin}
	if _, err := r.Launch(context.Background(), LaunchSpec{Name: "sb", WorkspacePath: "/ws", KitSources: []string{"/k"}, StaticMcp: []string{"notion", "linear"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadMcp(context.Background(), "sb", "notion", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("argv log = %q", lines)
	}
	if lines[0] != "create --name sb claude /ws --kit /k --static-mcp notion,linear" {
		t.Errorf("create argv = %q", lines[0])
	}
	if lines[1] != "mcp load notion --sandbox sb" {
		t.Errorf("load argv = %q", lines[1])
	}
	// No static set => no flag at all.
	if _, err := r.Launch(context.Background(), LaunchSpec{Name: "sb2", WorkspacePath: "/ws"}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(logPath)
	if strings.Contains(strings.Split(strings.TrimSpace(string(b)), "\n")[2], "static-mcp") {
		t.Error("an empty StaticMcp must not render --static-mcp")
	}
}
