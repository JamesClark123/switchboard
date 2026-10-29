package agent

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// TestPTYFactoryRealSession exercises the production ptySession against a real
// PTY. Using "echo" as the "sbx" binary makes the child print and exit, so
// Read/Resize/Close all run without a sandbox runtime.
func TestPTYFactoryRealSession(t *testing.T) {
	sess, err := PTYFactory("echo", nil)("sb1", &pb.AgentSpec{}, Spawn{})
	if err != nil {
		t.Fatalf("PTYFactory start: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// Resize the PTY.
	if err := sess.Resize(80, 24); err != nil {
		t.Errorf("Resize: %v", err)
	}
	// Read the echoed command line (best-effort; the child may have exited).
	buf := make([]byte, 256)
	if _, err := sess.Read(buf); err != nil {
		// EOF/IO error after the child exits is acceptable; the call ran.
		t.Logf("read returned: %v", err)
	}
	// Write is a no-op against an exited child but must not panic.
	_, _ = sess.Write([]byte("x"))
}

func TestInjectHooksErrorsOnBadWorkspace(t *testing.T) {
	// Make the workspace path a regular file so MkdirAll(.claude) fails.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InjectHooks(f, "sb1", "http://x/hook"); err == nil {
		t.Error("expected InjectHooks error when the workspace is not a directory")
	}
}

// The token reaches the exec client's own environment only (research R13): the
// stub "sbx" sees it, its argv does not, and a host command started after the
// spawn (an escape-hatch command, an ON_HOST service) does not inherit it.
func TestPTYFactoryPassesTheTokenInTheClientEnvironment(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sbx")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"env=$SXB_HOOK_TOKEN/$SXB_AGENT_GEN argv=$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := PTYFactory(bin, nil)("sb1", &pb.AgentSpec{}, Spawn{Gen: 3, Token: "f00dfeed"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	out, _ := io.ReadAll(sess)
	if !strings.Contains(string(out), "env=f00dfeed/3 argv=exec -it -e SXB_HOOK_TOKEN -e SXB_AGENT_GEN sb1 bash -lc") {
		t.Errorf("client saw %q", out)
	}
	host, err := exec.Command("sh", "-c", `echo "[$SXB_HOOK_TOKEN]"`).Output()
	if err != nil || strings.TrimSpace(string(host)) != "[]" || os.Getenv("SXB_HOOK_TOKEN") != "" {
		t.Errorf("a host command after the spawn inherited %q (%v)", host, err)
	}
}
