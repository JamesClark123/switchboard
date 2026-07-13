package agent

import (
	"os"
	"path/filepath"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// TestPTYFactoryRealSession exercises the production ptySession against a real
// PTY. Using "echo" as the "sbx" binary makes the child print and exit, so
// Read/Resize/Close all run without a sandbox runtime.
func TestPTYFactoryRealSession(t *testing.T) {
	sess, err := PTYFactory("echo", nil)("sb1", &pb.AgentSpec{})
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
