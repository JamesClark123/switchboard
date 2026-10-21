package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// stubSbx writes a fake `sbx` whose behaviour per argv is the given bash
// `case "$*"` body. Every invocation is appended to argv.log.
func stubSbx(t *testing.T, cases string) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "argv.log")
	script := "#!/usr/bin/env bash\necho \"$*\" >> \"" + logPath + "\"\ncase \"$*\" in\n" + cases + "\n  *) exit 0 ;;\nesac\n"
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

// fakeManager is the Manager surface over the real registry, recording emits.
type fakeManager struct {
	reg   *registry.Registry
	mu    sync.Mutex
	emits []*pb.Sandbox
}

func (m *fakeManager) Get(id string) (*pb.Sandbox, error) { return m.reg.Get(id) }
func (m *fakeManager) List() ([]*pb.Sandbox, error)       { return m.reg.List() }
func (m *fakeManager) Emit(sb *pb.Sandbox) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emits = append(m.emits, sb)
}

func (m *fakeManager) emitted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.emits)
}

type env struct {
	svc *Service
	reg *registry.Registry
	mgr *fakeManager
	log string
	dir string
}

func newEnv(t *testing.T, cases string) *env {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	bin, logPath := stubSbx(t, cases)
	mgr := &fakeManager{reg: reg}
	svc := NewService(Deps{
		Manager: mgr, Registry: reg, Hub: agent.NewHub("host-n"), CLI: &mcp.CLI{Bin: bin},
		HookPort: "8765", HostID: "host-n", Version: "test", Logf: t.Logf,
		Clock: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	return &env{svc: svc, reg: reg, mgr: mgr, log: logPath, dir: dir}
}

// sandbox seeds a RUNNING sandbox with a workspace directory.
func (e *env) sandbox(t *testing.T, id, name string, state pb.SandboxState) *pb.Sandbox {
	t.Helper()
	ws := filepath.Join(e.dir, "ws", name)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &pb.Sandbox{Id: id, DisplayName: name, ContainerRef: name, State: state, WorkspacePath: ws,
		Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE}}
	if err := e.reg.Put(sb); err != nil {
		t.Fatal(err)
	}
	return sb
}

func controller(orchID, name string) *pb.SandboxController {
	return &pb.SandboxController{OrchestratorId: orchID, Name: name, HostId: "host-m"}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

var bg = context.Background()
