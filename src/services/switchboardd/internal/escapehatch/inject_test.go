package escapehatch

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	// It must forward the NAME (plus optional workspace/args), never a command string
	// (SC-004). The name and the agent-supplied fields are the only inputs.
	for _, field := range []string{`\"name\":\"$(esc "$name")\"`, `\"workspace\":\"$(esc "$workspace")\"`, `\"args\":\"$(esc "$args")\"`} {
		if !strings.Contains(s, field) {
			t.Errorf("wrapper missing field %q:\n%s", field, s)
		}
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

// The generated wrapper sends the agent's token from its environment, and a
// call from outside the agent (no token) is refused (SWI-17, research R13).
func TestWrapperCallCarriesTheAgentToken(t *testing.T) {
	ws := t.TempDir()
	svc, _ := serviceWith(t, ws, shCmd("ok", "true"))
	srv := httptest.NewServer(http.HandlerFunc(svc.HandleRun))
	defer srv.Close()
	if err := Inject(ws, "sb1", srv.URL, []*pb.EscapeHatchCommand{shCmd("ok", "true")}); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) string {
		cmd := exec.Command("sh", filepath.Join(ws, wrapperRelPath), "ok")
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wrapper: %v: %s", err, out)
		}
		return string(out)
	}
	if out := run("SXB_HOOK_TOKEN=tok-1", "SXB_AGENT_GEN=1"); !strings.Contains(out, `"run_id"`) {
		t.Errorf("with the agent's token = %q", out)
	}
	if out := run(); !strings.Contains(out, "no agent token in this environment") {
		t.Errorf("without a token = %q", out)
	}
	if body, _ := os.ReadFile(filepath.Join(ws, wrapperRelPath)); strings.Contains(string(body), "tok-1") {
		t.Error("the wrapper must not embed a token")
	}
}
