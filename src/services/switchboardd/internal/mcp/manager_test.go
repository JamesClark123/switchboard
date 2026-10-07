package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// scriptedSbx writes a fake `sbx` whose behaviour per argv is the given bash
// `case "$*"` body. Every invocation is appended to argv.log so tests can pin the
// exact argv the manager produces.
func scriptedSbx(t *testing.T, cases string) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "argv.log")
	script := "#!/usr/bin/env bash\necho \"$*\" >> \"" + logPath + "\"\ncase \"$*\" in\n" + cases + "\n  *) echo \"unexpected: $*\" >&2; exit 1 ;;\nesac\n"
	bin = filepath.Join(dir, "sbx")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

const twoServerTable = `printf 'NAME                 TYPE     URL/COMMAND\nnotion               remote   https://mcp.notion.com/mcp\npw                   command  npx @playwright/mcp@latest\n'`

type testEnv struct {
	m      *Manager
	reg    *registry.Registry
	log    string
	events int
}

func newEnv(t *testing.T, cases string) *testEnv {
	t.Helper()
	bin, logPath := scriptedSbx(t, cases)
	reg, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	env := &testEnv{reg: reg, log: logPath}
	env.m = NewManager(&CLI{Bin: bin}, reg, "host-1", func(ev *pb.Event) {
		if ev.GetMcpGatewayChanged().GetHostId() != "host-1" {
			t.Errorf("event host = %q", ev.GetMcpGatewayChanged().GetHostId())
		}
		env.events++
	})
	return env
}

func (e *testEnv) mark(t *testing.T, names ...string) {
	t.Helper()
	_, err := e.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		s.Marks = map[string]bool{}
		for _, n := range names {
			s.Marks[n] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestListDecoratesAndDropsStaleMarks(t *testing.T) {
	env := newEnv(t, `
  "mcp ls") `+twoServerTable+` ;;
  "mcp auth status notion --json") echo '{"authorized": true}' ;;`)
	env.mark(t, "notion", "stale")
	servers, settings, err := env.m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %+v", servers)
	}
	n, pw := servers[0], servers[1]
	if n.GetName() != "notion" || n.GetKind() != pb.McpServerKind_MCP_SERVER_KIND_REMOTE || n.GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED || !n.GetAttachByDefault() || n.GetTarget() != "https://mcp.notion.com/mcp" {
		t.Errorf("notion row = %+v", n)
	}
	if pw.GetKind() != pb.McpServerKind_MCP_SERVER_KIND_LOCAL || pw.GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE || pw.GetAttachByDefault() {
		t.Errorf("pw row = %+v", pw)
	}
	if settings.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Errorf("mode = %v, want ADDITIVE when unset", settings.GetDefaultAttachMode())
	}
	if _, stale := settings.GetMarks()["stale"]; stale {
		t.Error("stale mark should be dropped from the returned settings")
	}
	persisted, _ := env.reg.GetMcpSettings()
	if _, stale := persisted.GetMarks()["stale"]; stale || !persisted.GetMarks()["notion"] {
		t.Errorf("persisted marks = %v, want stale dropped and notion kept", persisted.GetMarks())
	}
	log := readLog(t, env.log)
	if log[0] != "mcp ls" || !contains(log, "mcp auth status notion --json") || contains(log, "mcp auth status pw --json") {
		t.Errorf("argv log = %v", log)
	}
	if env.events != 0 {
		t.Error("a list must not emit a change event")
	}
}

func TestListStatusFailureIsUnknownNeverAnError(t *testing.T) {
	env := newEnv(t, `
  "mcp ls") `+twoServerTable+` ;;
  "mcp auth status notion --json") echo "boom" >&2; exit 1 ;;`)
	servers, _, err := env.m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN {
		t.Errorf("auth state = %v, want UNKNOWN", servers[0].GetAuthState())
	}
}

func TestListFailurePropagatesHostDiagnostic(t *testing.T) {
	env := newEnv(t, `
  "mcp ls") echo "You are not authenticated" >&2; exit 1 ;;`)
	_, _, err := env.m.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("err = %v", err)
	}
}

func TestAddRemoteArgvMarkAndEvent(t *testing.T) {
	env := newEnv(t, `
  "mcp add notion --url https://mcp.notion.com/mcp --skip-auth") echo registered ;;
  "mcp ls") `+twoServerTable+` ;;
  "mcp auth status notion --json") echo '{"status":"unauthorized"}' ;;`)
	row, err := env.m.Add(context.Background(), &pb.AddMcpServerRequest{
		Name: "notion", Definition: &pb.AddMcpServerRequest_Url{Url: "https://mcp.notion.com/mcp"}, AttachByDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !row.GetAttachByDefault() || row.GetKind() != pb.McpServerKind_MCP_SERVER_KIND_REMOTE || row.GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED {
		t.Errorf("row = %+v", row)
	}
	if log := readLog(t, env.log); log[0] != "mcp add notion --url https://mcp.notion.com/mcp --skip-auth" {
		t.Errorf("first argv = %q", log[0])
	}
	persisted, _ := env.reg.GetMcpSettings()
	if !persisted.GetMarks()["notion"] {
		t.Error("attach_by_default should persist the mark")
	}
	if env.events != 1 {
		t.Errorf("events = %d, want 1", env.events)
	}
}

func TestAddLocalAcknowledgementAndArgv(t *testing.T) {
	env := newEnv(t, `
  "mcp add pw --command npx --args @playwright/mcp@latest,--headless --dir /srv --skip-auth") exit 0 ;;
  "mcp ls") printf 'NAME  TYPE  URL/COMMAND\npw  command  npx @playwright/mcp@latest --headless\n' ;;`)
	cmd := &pb.AddMcpServerRequest_LocalCommand{Command: "npx", Args: []string{"@playwright/mcp@latest", "--headless"}, Dir: "/srv"}
	_, err := env.m.Add(context.Background(), &pb.AddMcpServerRequest{Name: "pw", Definition: &pb.AddMcpServerRequest_Command{Command: cmd}})
	if !errors.Is(err, ErrLocalNotAcknowledged) {
		t.Fatalf("err = %v, want ErrLocalNotAcknowledged", err)
	}
	if log := readLog(t, env.log); len(log) != 0 {
		t.Fatalf("nothing may execute before acknowledgement; argv = %v", log)
	}
	row, err := env.m.Add(context.Background(), &pb.AddMcpServerRequest{Name: "pw", Definition: &pb.AddMcpServerRequest_Command{Command: cmd}, AcknowledgeLocalExecution: true})
	if err != nil {
		t.Fatal(err)
	}
	if row.GetKind() != pb.McpServerKind_MCP_SERVER_KIND_LOCAL || row.GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE || row.GetAttachByDefault() {
		t.Errorf("row = %+v", row)
	}
	if log := readLog(t, env.log); log[0] != "mcp add pw --command npx --args @playwright/mcp@latest,--headless --dir /srv --skip-auth" {
		t.Errorf("argv = %q", log[0])
	}
}

func TestAddRefusesBadInputBeforeExec(t *testing.T) {
	env := newEnv(t, ``)
	cases := []*pb.AddMcpServerRequest{
		{Name: "bad name", Definition: &pb.AddMcpServerRequest_Url{Url: "https://x"}},
		{Name: "ok", Definition: &pb.AddMcpServerRequest_Url{Url: "  "}},
		{Name: "ok"},
		{Name: "ok", AcknowledgeLocalExecution: true, Definition: &pb.AddMcpServerRequest_Command{Command: &pb.AddMcpServerRequest_LocalCommand{Command: "npx", Args: []string{"a,b"}}}},
		{Name: "ok", AcknowledgeLocalExecution: true, Definition: &pb.AddMcpServerRequest_Command{Command: &pb.AddMcpServerRequest_LocalCommand{Command: " "}}},
	}
	for i, req := range cases {
		if _, err := env.m.Add(context.Background(), req); !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("case %d: err = %v, want ErrInvalidArgs", i, err)
		}
	}
	if log := readLog(t, env.log); len(log) != 0 {
		t.Errorf("invalid requests must not reach the CLI; argv = %v", log)
	}
}

func TestAddHostErrorIsVerbatimAndSilent(t *testing.T) {
	env := newEnv(t, `
  "mcp add dup --url https://x --skip-auth") echo 'Error: MCP server "dup" is already registered' >&2; exit 1 ;;`)
	_, err := env.m.Add(context.Background(), &pb.AddMcpServerRequest{Name: "dup", Definition: &pb.AddMcpServerRequest_Url{Url: "https://x"}})
	if err == nil || !strings.Contains(err.Error(), `"dup" is already registered`) {
		t.Errorf("err = %v", err)
	}
	if env.events != 0 {
		t.Error("a failed add must not emit")
	}
}

func TestRemoveClearsMarkCollectsNotesAndMapsNotFound(t *testing.T) {
	env := newEnv(t, `
  "mcp rm notion") printf 'MCP server "notion" removed.\nTo remove its secrets run:\n  sbx secret rm mcp:notion:api-key\n' ;;
  "mcp rm ghost") echo 'Error: MCP server "ghost" is not registered' >&2; exit 1 ;;`)
	env.mark(t, "notion")
	notes, err := env.m.Remove(context.Background(), "notion")
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0] != "sbx secret rm mcp:notion:api-key" {
		t.Errorf("notes = %q", notes)
	}
	persisted, _ := env.reg.GetMcpSettings()
	if persisted.GetMarks()["notion"] {
		t.Error("remove should clear the mark")
	}
	if log := readLog(t, env.log); log[0] != "mcp rm notion" {
		t.Errorf("argv = %q", log[0])
	}
	if env.events != 1 {
		t.Errorf("events = %d, want 1", env.events)
	}
	if _, err := env.m.Remove(context.Background(), "ghost"); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("err = %v, want ErrNotRegistered", err)
	}
	if env.events != 1 {
		t.Error("a failed remove must not emit")
	}
}

func TestSetDefaultAndSetMode(t *testing.T) {
	env := newEnv(t, `
  "mcp ls") `+twoServerTable+` ;;
  "mcp auth status notion --json") echo '{"authorized": true}' ;;`)
	if _, err := env.m.SetDefault(context.Background(), "ghost", true); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("err = %v, want ErrNotRegistered", err)
	}
	row, err := env.m.SetDefault(context.Background(), "notion", true)
	if err != nil {
		t.Fatal(err)
	}
	if !row.GetAttachByDefault() || row.GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED {
		t.Errorf("row = %+v", row)
	}
	persisted, _ := env.reg.GetMcpSettings()
	if !persisted.GetMarks()["notion"] {
		t.Error("mark should persist")
	}
	row, _ = env.m.SetDefault(context.Background(), "notion", false)
	if row.GetAttachByDefault() {
		t.Error("mark should clear")
	}

	s, err := env.m.SetMode(context.Background(), pb.McpAttachMode_MCP_ATTACH_MODE_UNSPECIFIED)
	if err != nil || s.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Errorf("SetMode(UNSPECIFIED) = %v, %v; want ADDITIVE", s.GetDefaultAttachMode(), err)
	}
	s, _ = env.m.SetMode(context.Background(), pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE)
	persisted, _ = env.reg.GetMcpSettings()
	if s.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE || persisted.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
		t.Errorf("mode = %v / persisted %v, want EXCLUSIVE", s.GetDefaultAttachMode(), persisted.GetDefaultAttachMode())
	}
	if env.events != 4 {
		t.Errorf("events = %d, want 4 (two SetDefault, two SetMode)", env.events)
	}
}

func TestResolveAttachAndLoad(t *testing.T) {
	env := newEnv(t, `
  "mcp ls") printf 'NAME  TYPE  URL/COMMAND\nzed  remote  https://z\na  remote  https://a\nunmarked  remote  https://u\n' ;;
  "mcp load a --sandbox my-sb") exit 0 ;;
  "mcp load zed --sandbox my-sb") echo "load failed: gateway busy" >&2; exit 1 ;;`)
	env.mark(t, "zed", "a", "stale")
	att, err := env.m.ResolveAttach(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(att.Servers, ",") != "a,zed" || strings.Join(att.Skipped, ",") != "stale" || att.Mode != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Errorf("attach = %+v", att)
	}
	persisted, _ := env.reg.GetMcpSettings()
	if _, stale := persisted.GetMarks()["stale"]; stale {
		t.Error("stale mark should be dropped on resolve")
	}
	if err := env.m.Load(context.Background(), "a", "my-sb"); err != nil {
		t.Errorf("Load a: %v", err)
	}
	if err := env.m.Load(context.Background(), "zed", "my-sb"); err == nil || !strings.Contains(err.Error(), "gateway busy") {
		t.Errorf("Load zed: %v, want the host diagnostic", err)
	}
	if log := readLog(t, env.log); !contains(log, "mcp load a --sandbox my-sb") {
		t.Errorf("argv = %v", log)
	}
	if env.events != 0 {
		t.Error("resolve/load must not emit")
	}

	// A list failure is returned so the caller launches without MCP.
	broken := newEnv(t, `
  "mcp ls") exit 1 ;;`)
	if _, err := broken.m.ResolveAttach(context.Background()); err == nil {
		t.Error("expected the list failure")
	}
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}
