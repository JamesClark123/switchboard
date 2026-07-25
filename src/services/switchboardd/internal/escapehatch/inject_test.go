package escapehatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

const testCallbackURL = "http://host.docker.internal:8765/escape-hatch/run"

func TestInjectWritesWrapper(t *testing.T) {
	ws := t.TempDir()
	cmds := []*pb.EscapeHatchCommand{shCmd("install-deps", "pnpm install")}
	if err := Inject(ws, "sb-123", testCallbackURL, cmds); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws, wrapperRelPath)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("wrapper mode = %v, want 0755", info.Mode().Perm())
	}
	body, _ := os.ReadFile(path)
	s := string(body)
	if !strings.Contains(s, "sb-123") {
		t.Error("wrapper should embed the sandbox id")
	}
	if !strings.Contains(s, testCallbackURL) {
		t.Error("wrapper should embed the callback URL")
	}
	// It must forward only the NAME ($1), never a command string (SC-004).
	if !strings.Contains(s, `\"name\":\"$1\"`) {
		t.Errorf("wrapper should send only the command name:\n%s", s)
	}
	if strings.Contains(s, "pnpm install") {
		t.Error("wrapper must NOT contain any command string")
	}
}

func TestInjectRemovesWrapperWhenEmpty(t *testing.T) {
	ws := t.TempDir()
	if err := Inject(ws, "sb-1", testCallbackURL, []*pb.EscapeHatchCommand{shCmd("x", "true")}); err != nil {
		t.Fatal(err)
	}
	// Re-inject with an empty set -> wrapper + rule removed (FR-037).
	if err := Inject(ws, "sb-1", testCallbackURL, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, wrapperRelPath)); !os.IsNotExist(err) {
		t.Errorf("wrapper should be removed when no commands remain, err=%v", err)
	}
}
