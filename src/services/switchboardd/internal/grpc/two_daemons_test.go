package grpc_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/orchestrator"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// twoDaemonsAgents is twoDaemons with N's agent sessions backed by a recording
// fake (what the daemon types into the agent lands in <nDir>/typed.txt) and a
// terminal registry, so delivery is observable end to end.
func twoDaemonsAgents(t *testing.T) (m *orchEnv, nClient pb.SwitchboardClient, nDir string, nMgr *sandbox.Manager) {
	t.Helper()
	nDir = t.TempDir()
	nReg, err := registry.Open(nDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nReg.Close() })
	nWs := filepath.Join(nDir, "ws")
	if err := os.MkdirAll(nWs, 0o755); err != nil {
		t.Fatal(err)
	}
	nMgr = sandbox.NewManager(nReg, &testRunner{running: map[string]bool{}}, nWs, "host-n")
	nHub := agent.NewHub("host-n")
	typed := filepath.Join(nDir, "typed.txt")
	// Like Claude Code, each fake agent fires SessionStart once it is up; the
	// hook server is wired as sxbd wires it (SWI-15).
	hooks := agent.NewHookServer(nHub, nMgr)
	hookSrv := httptest.NewServer(http.HandlerFunc(hooks.Handle))
	t.Cleanup(hookSrv.Close)
	agents := agent.NewRegistry(func(id string, _ *pb.AgentSpec) (agent.Session, error) {
		go reportStarted(nMgr, hookSrv.URL, id)
		return newFileSession(typed), nil
	})
	agents.SetStatusUpdater(nMgr)
	hooks.SetOnReady(agents.MarkReady)
	sbxBin := filepath.Join(nDir, "sbx")
	if err := os.WriteFile(sbxBin, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nWorker := worker.NewService(worker.Deps{Manager: nMgr, Registry: nReg, Agents: agents, Hub: nHub, CLI: &mcp.CLI{Bin: sbxBin}, HookPort: "18765", HostID: "host-n", Logf: t.Logf})
	nMgr.SetOnDestroy(nWorker.OnDestroy)
	nMgr.SetOnBringUp(nWorker.OnBringUp)
	nMgr.SetControllerApplier(nWorker.Apply)
	nSrv := sbgrpc.NewServer(sbgrpc.Config{Manager: nMgr, HostID: "host-n", WorkspaceRoot: nWs, Hub: nHub, Agents: agents, Worker: nWorker})
	nWorker.SetTerminals(nSrv.Terminals())
	nSock := filepath.Join(nDir, "n.sock")
	nLis, err := net.Listen("unix", nSock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = nSrv.ServeListener(nLis) }()
	t.Cleanup(nSrv.Stop)
	nConn, err := grpc.NewClient("unix:"+nSock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nConn.Close() })
	nClient = pb.NewSwitchboardClient(nConn)

	mDir := t.TempDir()
	mReg, err := registry.Open(mDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mReg.Close() })
	mWs := filepath.Join(mDir, "ws")
	mMgr := sandbox.NewManager(mReg, &testRunner{running: map[string]bool{}}, mWs, "host-m")
	mHub := agent.NewHub("host-m")
	mWorker := worker.NewService(worker.Deps{Manager: mMgr, Registry: mReg, Hub: mHub, HostID: "host-m", Logf: t.Logf})
	svc := orchestrator.NewService(orchestrator.Deps{
		Store: mReg, Hub: mHub, HostID: "host-m", Sandboxes: mMgr, Logf: t.Logf, DaemonVersion: "test",
		Local: worker.Local(mWorker, mHub),
		Dial: func(ctx context.Context, target string, _ []string) (*dial.Conn, error) {
			return dial.DialLocal(ctx, nSock)
		},
	})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	mSrv := sbgrpc.NewServer(sbgrpc.Config{Manager: mMgr, HostID: "host-m", WorkspaceRoot: mWs, Hub: mHub, Worker: mWorker, Orchestrator: svc})
	mSock := filepath.Join(mDir, "m.sock")
	mLis, err := net.Listen("unix", mSock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = mSrv.ServeListener(mLis) }()
	t.Cleanup(mSrv.Stop)
	mConn, err := grpc.NewClient("unix:"+mSock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mConn.Close() })
	return &orchEnv{client: pb.NewSwitchboardClient(mConn), svc: svc, reg: mReg, hub: mHub, mgr: mMgr, dir: mDir, nWorker: nWorker, nReg: nReg}, nClient, nDir, nMgr
}

// fileSession appends every write to a file and blocks reads until closed.
type fileSession struct {
	path   string
	closed chan struct{}
}

func newFileSession(path string) *fileSession {
	return &fileSession{path: path, closed: make(chan struct{})}
}

func (s *fileSession) Write(p []byte) (int, error) {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	return f.Write(p)
}
func (s *fileSession) Read([]byte) (int, error)    { <-s.closed; return 0, os.ErrClosed }
func (s *fileSession) Resize(uint16, uint16) error { return nil }
func (s *fileSession) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// reportStarted fires a fake agent's SessionStart hook once its session is
// recorded STARTING (the registry records that after the factory returns).
func reportStarted(mgr *sandbox.Manager, hookURL, id string) {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if sb, err := mgr.Get(id); err == nil && sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_STARTING {
			break
		}
	}
	body := `{"hook_event_name":"SessionStart","source":"startup"}`
	if resp, err := http.Post(hookURL+"?event=SessionStart&sandbox_id="+id, "application/json", strings.NewReader(body)); err == nil {
		_ = resp.Body.Close()
	}
}

// typedOn reads what N's agent sessions received.
func typedOn(nDir string) string {
	b, _ := os.ReadFile(filepath.Join(nDir, "typed.txt"))
	return string(b)
}
