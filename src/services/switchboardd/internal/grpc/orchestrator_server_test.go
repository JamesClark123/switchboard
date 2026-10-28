package grpc_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// orchEnv is a daemon with the orchestrator side configured (feature 009).
type orchEnv struct {
	client pb.SwitchboardClient
	svc    *orchestrator.Service
	reg    *registry.Registry
	hub    *agent.Hub
	mgr    *sandbox.Manager
	dir    string
	// The remote daemon's worker side and registry (two-daemon envs only).
	nWorker *worker.Service
	nReg    *registry.Registry
}

// startOrchServer starts a daemon hosting orchestrators; dial never succeeds
// (every remote host is unreachable), the own host has no worker side yet.
func startOrchServer(t *testing.T) *orchEnv {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	ws := filepath.Join(dir, "ws")
	mgr := sandbox.NewManager(reg, &testRunner{running: map[string]bool{}}, ws, "host-1")
	hub := agent.NewHub("host-1")
	svc := orchestrator.NewService(orchestrator.Deps{Store: reg, Hub: hub, HostID: "host-1", Sandboxes: mgr, Logf: t.Logf, DaemonVersion: "test"})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := sbgrpc.NewServer(sbgrpc.Config{Manager: mgr, HostID: "host-1", WorkspaceRoot: ws, Hub: hub, Orchestrator: svc})
	sock := filepath.Join(dir, "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("unix:"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &orchEnv{client: pb.NewSwitchboardClient(conn), svc: svc, reg: reg, hub: hub, mgr: mgr, dir: dir}
}

func TestOrchestratorRPCsRoundTrip(t *testing.T) {
	env := startOrchServer(t)
	ctx := context.Background()
	c := env.client

	// Subscribe first so the orchestrator_changed arm is observed on the wire.
	stream := subscribe(t, c, &pb.SubscribeRequest{})

	o, err := c.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "review-bot"})
	if err != nil || o.GetName() != "review-bot" || o.GetHostId() != "host-1" {
		t.Fatalf("Create = %v / %v", o, err)
	}
	ev, err := stream.Recv()
	if err != nil || ev.GetOrchestratorChanged().GetOrchestratorId() != o.GetId() || ev.GetOrchestratorChanged().GetHostId() != "host-1" {
		t.Errorf("expected orchestrator_changed on the stream, got %v / %v", ev, err)
	}
	if _, err := c.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "review-bot"}); status.Code(err) != codes.AlreadyExists {
		t.Errorf("duplicate name = %v, want ALREADY_EXISTS", err)
	}
	if _, err := c.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: ""}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("empty name = %v, want INVALID_ARGUMENT", err)
	}

	list, err := c.ListOrchestrators(ctx, &pb.ListOrchestratorsRequest{})
	if err != nil || len(list.GetOrchestrators()) != 1 {
		t.Fatalf("List = %v / %v", list, err)
	}
	det, err := c.GetOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: o.GetId()})
	if err != nil || det.GetSummary().GetOrchestrator().GetId() != o.GetId() {
		t.Fatalf("Get = %v / %v", det, err)
	}
	if _, err := c.GetOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: "nope"}); status.Code(err) != codes.NotFound {
		t.Errorf("Get unknown = %v, want NOT_FOUND", err)
	}
	renamed, err := c.RenameOrchestrator(ctx, &pb.RenameOrchestratorRequest{OrchestratorId: o.GetId(), Name: "rb"})
	if err != nil || renamed.GetName() != "rb" {
		t.Fatalf("Rename = %v / %v", renamed, err)
	}
	if _, err := c.RenameOrchestrator(ctx, &pb.RenameOrchestratorRequest{OrchestratorId: "nope", Name: "x"}); status.Code(err) != codes.NotFound {
		t.Errorf("Rename unknown = %v", err)
	}

	hosts := []*pb.OrchestratorHost{{HostId: "box", DisplayName: "build-box", SshTarget: "me@box"}}
	rec, err := c.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: o.GetId(), Hosts: hosts})
	if err != nil || len(rec.GetHosts()) != 1 {
		t.Fatalf("SetHosts = %v / %v", rec, err)
	}
	if _, err := c.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: o.GetId(), Hosts: []*pb.OrchestratorHost{{HostId: "host-1", SshTarget: "x"}}}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("own host entry = %v, want INVALID_ARGUMENT", err)
	}
	// A worker on the host makes its removal FAILED_PRECONDITION naming it (the
	// runtime re-reads the persisted record under the update, so an out-of-band
	// roster entry is seen).
	if _, err := env.reg.UpdateOrchestrator(o.GetId(), func(x *pb.Orchestrator) error {
		x.Workers = []*pb.Worker{{SandboxId: "sb-on-box", HostId: "box"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = c.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: o.GetId()})
	if st := status.Convert(err); st.Code() != codes.FailedPrecondition || !strings.Contains(st.Message(), "sb-on-box") {
		t.Errorf("removing a host with workers = %v", err)
	}
	if _, err := env.reg.UpdateOrchestrator(o.GetId(), func(x *pb.Orchestrator) error { x.Workers = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: o.GetId()}); err != nil {
		t.Errorf("removing an unused host = %v", err)
	}
	resp, err := c.RemoveOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: o.GetId()})
	if err != nil || resp == nil {
		t.Fatalf("Remove = %v / %v", resp, err)
	}
	if list, _ := c.ListOrchestrators(ctx, &pb.ListOrchestratorsRequest{}); len(list.GetOrchestrators()) != 0 {
		t.Errorf("after remove: %v", list)
	}
	if _, err := c.RemoveOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: o.GetId()}); status.Code(err) != codes.NotFound {
		t.Errorf("Remove twice over the wire = %v, want NOT_FOUND", err)
	}
}

// --- two real daemons (research R16): M hosts the orchestrator, N owns the sandboxes ---

// twoDaemons starts N (a full daemon with the worker side) and M (hosting the
// orchestrator, dialing N through an injected dial.Func over N's socket).
func twoDaemons(t *testing.T) (m *orchEnv, nClient pb.SwitchboardClient, nDir string) {
	t.Helper()
	// N: owns sandboxes; its worker side runs a stub sbx for mcp add/load/rm.
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
	nMgr := sandbox.NewManager(nReg, &testRunner{running: map[string]bool{}}, nWs, "host-n")
	nHub := agent.NewHub("host-n")
	sbxBin := filepath.Join(nDir, "sbx")
	if err := os.WriteFile(sbxBin, []byte("#!/usr/bin/env bash\necho \"$*\" >> \""+filepath.Join(nDir, "sbx.log")+"\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nWorker := worker.NewService(worker.Deps{Manager: nMgr, Registry: nReg, Hub: nHub, CLI: &mcp.CLI{Bin: sbxBin}, HookPort: "18765", HostID: "host-n", Logf: t.Logf})
	nMgr.SetOnDestroy(nWorker.OnDestroy)
	nMgr.SetOnBringUp(nWorker.OnBringUp)
	nMgr.SetControllerApplier(nWorker.Apply)
	nSrv := sbgrpc.NewServer(sbgrpc.Config{Manager: nMgr, HostID: "host-n", WorkspaceRoot: nWs, Hub: nHub, Worker: nWorker})
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

	// M: hosts the orchestrator; "ssh me@n" dials N's socket.
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
			if target != "me@n" {
				return nil, &dial.DialError{Kind: dial.DialUnreachable, Stderr: "ssh: Could not resolve hostname " + target}
			}
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
	return &orchEnv{client: pb.NewSwitchboardClient(mConn), svc: svc, reg: mReg, hub: mHub, mgr: mMgr, dir: mDir, nWorker: nWorker, nReg: nReg}, nClient, nDir
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestTwoDaemonsAttachDetachAndLaunchAttach(t *testing.T) {
	m, n, nDir := twoDaemons(t)
	ctx := context.Background()

	// A sandbox on N.
	src := filepath.Join(nDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	sb := launch(t, n, src)

	o, err := m.client.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "review-bot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.client.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: o.GetId(), Hosts: []*pb.OrchestratorHost{{HostId: "host-n", DisplayName: "n", SshTarget: "me@n"}}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		det, err := m.client.GetOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: o.GetId()})
		return err == nil && len(det.GetHosts()) == 1 && det.GetHosts()[0].GetReachable()
	})

	// Attach through the link: N's record carries the controller, the stub sbx
	// saw add then load, and the three worker RPCs round-trip on N directly.
	view, err := m.client.AttachWorker(ctx, &pb.AttachWorkerRequest{OrchestratorId: o.GetId(), HostId: "host-n", SandboxId: sb.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if view.GetAvailability() != pb.WorkerAvailability_WORKER_AVAILABILITY_FREE {
		t.Errorf("view = %+v", view)
	}
	nList, _ := n.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if c := nList.GetSandboxes()[0].GetController(); c.GetOrchestratorId() != o.GetId() || c.GetName() != "review-bot" {
		t.Errorf("N record controller = %+v", c)
	}
	logb, _ := os.ReadFile(filepath.Join(nDir, "sbx.log"))
	logs := string(logb)
	if !strings.Contains(logs, "mcp add sxb-orch-"+sb.GetId()[:8]+" --url http://127.0.0.1:18765/orchestrator/w/") || !strings.Contains(logs, "mcp load sxb-orch-"+sb.GetId()[:8]+" --sandbox ") {
		t.Errorf("N sbx log = %q", logs)
	}
	// Another orchestrator on M is refused naming the controller.
	o2, _ := m.client.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "second"})
	if _, err := m.client.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: o2.GetId(), Hosts: []*pb.OrchestratorHost{{HostId: "host-n", SshTarget: "me@n"}}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		det, _ := m.client.GetOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: o2.GetId()})
		return len(det.GetHosts()) == 1 && det.GetHosts()[0].GetReachable()
	})
	_, err = m.client.AttachWorker(ctx, &pb.AttachWorkerRequest{OrchestratorId: o2.GetId(), HostId: "host-n", SandboxId: sb.GetId()})
	if st := status.Convert(err); st.Code() != codes.AlreadyExists || !strings.Contains(st.Message(), "controlled by review-bot") {
		t.Errorf("second orchestrator = %v", err)
	}
	// Worker RPCs directly on N: UpdateWorker by the wrong orchestrator is denied.
	if _, err := n.UpdateWorker(ctx, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: o2.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("foreign UpdateWorker = %v", err)
	}
	// Detach through the orchestrator; N's record is clean; `mcp rm --force` ran.
	if _, err := m.client.DetachWorker(ctx, &pb.DetachWorkerRequest{OrchestratorId: o.GetId(), SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}
	nList, _ = n.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if nList.GetSandboxes()[0].GetController() != nil {
		t.Error("N record should be detached")
	}
	logb, _ = os.ReadFile(filepath.Join(nDir, "sbx.log"))
	if !strings.Contains(string(logb), "mcp rm sxb-orch-"+sb.GetId()[:8]+" --force") {
		t.Errorf("N sbx log after detach = %q", logb)
	}
	// The TUI's direct path: DetachController on N when the orchestrator is unreachable.
	if _, err := m.client.AttachWorker(ctx, &pb.AttachWorkerRequest{OrchestratorId: o.GetId(), HostId: "host-n", SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.DetachController(ctx, &pb.DetachControllerRequest{SandboxId: sb.GetId(), OrchestratorId: "wrong"}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("DetachController by the wrong orchestrator = %v", err)
	}
	if _, err := n.DetachController(ctx, &pb.DetachControllerRequest{SandboxId: sb.GetId(), OrchestratorId: o.GetId()}); err != nil {
		t.Fatal(err)
	}

	// Launch-attach on N: the record has field 23 and the stub saw add + load
	// after create and before the record turned RUNNING.
	bundle, err := m.client.GetWorkerBundle(ctx, &pb.GetWorkerBundleRequest{OrchestratorId: o.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := n.LaunchSandbox(ctx, &pb.LaunchSandboxRequest{
		Config: &pb.ConfigSnapshot{Name: "launched-worker"}, Sources: []*pb.SourceRef{{Path: src}},
		OverrideResourceWarning: true, Controller: bundle,
	})
	if err != nil {
		t.Fatal(err)
	}
	var done *pb.Sandbox
	var lines []string
	for {
		p, err := stream.Recv()
		if err != nil {
			break
		}
		if l := p.GetLogLine(); l != "" {
			lines = append(lines, l)
		}
		if d := p.GetDone(); d != nil {
			done = d
		}
	}
	if done == nil || done.GetController().GetOrchestratorId() != o.GetId() || done.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("launch-attached record = %+v", done)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "orchestrator: attached to review-bot") {
		t.Errorf("launch log = %v", lines)
	}
	logb, _ = os.ReadFile(filepath.Join(nDir, "sbx.log"))
	logs = string(logb)
	addIdx := strings.LastIndex(logs, "mcp add sxb-orch-"+done.GetId()[:8])
	loadIdx := strings.LastIndex(logs, "mcp load sxb-orch-"+done.GetId()[:8])
	if addIdx < 0 || loadIdx < addIdx {
		t.Errorf("launch-attach should add then load: %q", logs)
	}
	// The orchestrator adopts it on its next reconcile (the link reconnects when
	// the entry is re-set); here the roster shows it after an explicit attach.
	if _, err := m.client.AttachWorker(ctx, &pb.AttachWorkerRequest{OrchestratorId: o.GetId(), HostId: "host-n", SandboxId: done.GetId()}); err != nil {
		t.Errorf("attach of a launch-attached sandbox is idempotent: %v", err)
	}
	det, _ := m.client.GetOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: o.GetId()})
	found := false
	for _, w := range det.GetWorkers() {
		if w.GetSandboxId() == done.GetId() {
			found = true
		}
	}
	if !found {
		t.Errorf("roster should list the launch-attached worker: %+v", det.GetWorkers())
	}
}

// twoDaemonsWithAgents is twoDaemons with N running a fake agent session and a
// terminal registry, so delivery types into something observable.
func TestTwoDaemonsCallDeliverReviewMarkFree(t *testing.T) {
	m, n, nDir, nMgr := twoDaemonsAgents(t)
	ctx := context.Background()
	src := filepath.Join(nDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	sb := launch(t, n, src)
	o, _ := m.client.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "review-bot"})
	if _, err := m.client.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: o.GetId(), Hosts: []*pb.OrchestratorHost{{HostId: "host-n", SshTarget: "me@n"}}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		det, _ := m.client.GetOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: o.GetId()})
		return len(det.GetHosts()) == 1 && det.GetHosts()[0].GetReachable()
	})
	if _, err := m.client.AttachWorker(ctx, &pb.AttachWorkerRequest{OrchestratorId: o.GetId(), HostId: "host-n", SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.client.PutAction(ctx, &pb.PutActionRequest{OrchestratorId: o.GetId(), Action: &pb.Action{
		Name: "review-pr", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "Review {number} in {repo}.",
		Arguments: []*pb.ActionArgument{{Name: "number", Required: true}, {Name: "repo", Required: true}, {Name: "focus"}},
	}, SetsLanes: true, Guard: &pb.LaneGuardConfirm{UnlanedActions: []string{"review-pr"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.client.PutAction(ctx, &pb.PutActionRequest{OrchestratorId: o.GetId(), Action: &pb.Action{Name: "bad"}, SetsLanes: true}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("invalid action = %v", err)
	}
	// Call: queued, then delivered on N (its record carries the run; the agent
	// session received the framed prompt with both values and the appended one).
	run, err := m.client.CallAction(ctx, &pb.CallActionRequest{OrchestratorId: o.GetId(), Action: "review-pr", Arguments: map[string]string{"number": "412", "repo": "acme/web", "focus": "tests"}})
	if err != nil || run.GetState() != pb.RunState_RUN_STATE_QUEUED {
		t.Fatalf("CallAction = %+v / %v", run, err)
	}
	if _, err := m.client.CallAction(ctx, &pb.CallActionRequest{OrchestratorId: o.GetId(), Action: "review-pr", Arguments: map[string]string{"number": "1"}}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("missing required = %v", err)
	}
	waitUntil(t, func() bool {
		got, _ := m.client.GetRun(ctx, &pb.GetRunRequest{OrchestratorId: o.GetId(), RunId: run.GetId()})
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING
	})
	nList, _ := n.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if r := nList.GetSandboxes()[0].GetController().GetRun(); r.GetId() != run.GetId() {
		t.Errorf("N record run = %+v", r)
	}
	waitUntil(t, func() bool { return strings.Contains(typedOn(nDir), "Review 412 in acme/web.\n\nfocus: tests") })
	if typed := typedOn(nDir); !strings.Contains(typed, `[switchboard orchestrator "review-bot" · run `+run.GetId()) {
		t.Errorf("typed = %q", typed)
	}
	// A second call waits at position 1 (one worker, one run).
	run2, _ := m.client.CallAction(ctx, &pb.CallActionRequest{OrchestratorId: o.GetId(), Action: "review-pr", Arguments: map[string]string{"number": "2", "repo": "r"}})
	if run2.GetPosition() != 1 {
		t.Errorf("second call position = %d", run2.GetPosition())
	}
	runs, _ := m.client.ListRuns(ctx, &pb.ListRunsRequest{OrchestratorId: o.GetId()})
	if len(runs.GetQueued()) != 1 || len(runs.GetInFlight()) != 1 {
		t.Errorf("ListRuns = %+v", runs)
	}
	// N's hook events: NEEDS_INPUT ⇒ awaiting input; IDLE without a report ⇒
	// awaiting review after the grace (real clock, 10 s is too long: use the
	// developer's mark free after a direct transition instead).
	if _, err := nMgr.SetAgentStatus(sb.GetId(), pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		got, _ := m.client.GetRun(ctx, &pb.GetRunRequest{OrchestratorId: o.GetId(), RunId: run.GetId()})
		return got.GetState() == pb.RunState_RUN_STATE_AWAITING_INPUT
	})
	if _, err := nMgr.SetAgentStatus(sb.GetId(), pb.AgentStatus_AGENT_STATUS_WORKING, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		got, _ := m.client.GetRun(ctx, &pb.GetRunRequest{OrchestratorId: o.GetId(), RunId: run.GetId()})
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING
	})
	// Mark free is refused while the run is not awaiting review; cancel of a
	// running run is refused; cancel of the queued one works.
	if _, err := m.client.MarkWorkerFree(ctx, &pb.MarkWorkerFreeRequest{OrchestratorId: o.GetId(), SandboxId: sb.GetId()}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("mark free on a running run = %v", err)
	}
	if _, err := m.client.CancelRun(ctx, &pb.CancelRunRequest{OrchestratorId: o.GetId(), RunId: run.GetId()}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("cancel running = %v", err)
	}
	if c, err := m.client.CancelRun(ctx, &pb.CancelRunRequest{OrchestratorId: o.GetId(), RunId: run2.GetId()}); err != nil || c.GetState() != pb.RunState_RUN_STATE_CANCELLED {
		t.Errorf("cancel queued = %+v / %v", c, err)
	}
	// The sandbox stops ⇒ the run fails, N's record clears the run, RUN_FAILED
	// notification raised on M.
	stream := subscribe(t, m.client, &pb.SubscribeRequest{})
	if _, err := n.StopSandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		got, _ := m.client.GetRun(ctx, &pb.GetRunRequest{OrchestratorId: o.GetId(), RunId: run.GetId()})
		return got.GetState() == pb.RunState_RUN_STATE_FAILED
	})
	for {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatalf("no RUN_FAILED notification: %v", err)
		}
		if nf := ev.GetNotification(); nf != nil {
			if nf.GetKind() != pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED || nf.GetSandboxId() != sb.GetId() {
				t.Errorf("notification = %+v", nf)
			}
			break
		}
	}
	// DeliverWorkerPrompt directly on N: refused on a stopped sandbox.
	if _, err := n.DeliverWorkerPrompt(ctx, &pb.DeliverWorkerPromptRequest{SandboxId: sb.GetId(), OrchestratorId: o.GetId(), RunId: "r", Action: "a", Prompt: "p"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("deliver on a stopped sandbox = %v", err)
	}
	// DeliverWorkerStep (SWI-17) is served alike, directly and as the
	// orchestrator's remote API sees it.
	step := &pb.DeliverWorkerStepRequest{SandboxId: sb.GetId(), OrchestratorId: o.GetId(), RunId: "r", Action: "a", First: true, StepIndex: 1,
		Step: &pb.RunStep{Kind: pb.StepKind_STEP_KIND_MODEL, Expect: "opus"}}
	if _, err := n.DeliverWorkerStep(ctx, step); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("step on a stopped sandbox = %v", err)
	}
	if _, err := worker.Remote(n).DeliverWorkerStep(ctx, step); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("remote step on a stopped sandbox = %v", err)
	}
	step.Step.Expect = "gpt"
	if _, err := n.DeliverWorkerStep(ctx, step); status.Code(err) != codes.InvalidArgument {
		t.Errorf("step with a bad expect = %v", err)
	}
	step.SandboxId, step.Step.Expect = "nope", "opus"
	if _, err := n.DeliverWorkerStep(ctx, step); status.Code(err) != codes.NotFound {
		t.Errorf("step on an unknown sandbox = %v", err)
	}
	// Delete the action: a (disabled) event names it.
	if _, err := m.client.PutTimedEvent(ctx, &pb.PutTimedEventRequest{OrchestratorId: o.GetId(), Event: &pb.TimedEvent{Name: "poll", Action: "review-pr", IntervalSeconds: 60, Enabled: true, Arguments: map[string]string{"number": "1", "repo": "r"}}}); err != nil && status.Code(err) != codes.Unavailable {
		t.Fatalf("PutTimedEvent = %v", err)
	}
	del, err := m.client.DeleteAction(ctx, &pb.DeleteActionRequest{OrchestratorId: o.GetId(), Name: "review-pr"})
	if err != nil {
		t.Fatal(err)
	}
	_ = del
}

// PR #4: FireTimedEvent over the wire — queues as the event, dedupe refusal is
// FAILED_PRECONDITION naming the run, unknown event / orchestrator are NOT_FOUND.
func TestFireTimedEventRPC(t *testing.T) {
	env := startOrchServer(t)
	ctx := context.Background()
	c := env.client
	o, err := c.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "review-bot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutAction(ctx, &pb.PutActionRequest{OrchestratorId: o.GetId(), Action: &pb.Action{
		Name: "check", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "check {repo}",
		Arguments: []*pb.ActionArgument{{Name: "repo", Required: true}},
	}, SetsLanes: true, Guard: &pb.LaneGuardConfirm{UnlanedActions: []string{"check"}}}); err != nil {
		t.Fatal(err)
	}
	ev, err := c.PutTimedEvent(ctx, &pb.PutTimedEventRequest{OrchestratorId: o.GetId(), Event: &pb.TimedEvent{
		Name: "poll", Action: "check", IntervalSeconds: 3600, Enabled: true, Arguments: map[string]string{"repo": "acme/web"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	run, err := c.FireTimedEvent(ctx, &pb.FireTimedEventRequest{OrchestratorId: o.GetId(), EventId: ev.GetId()})
	if err != nil || run.GetEventKind() != pb.EventKind_EVENT_KIND_TIMED || run.GetOrigin() != ev.GetId() || run.GetOriginLabel() != "poll" || run.GetPosition() < 1 {
		t.Fatalf("FireTimedEvent = %+v / %v", run, err)
	}
	_, err = c.FireTimedEvent(ctx, &pb.FireTimedEventRequest{OrchestratorId: o.GetId(), EventId: ev.GetId()})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), run.GetId()) {
		t.Errorf("dedupe refusal = %v, want FAILED_PRECONDITION naming %s", err, run.GetId())
	}
	if _, err := c.FireTimedEvent(ctx, &pb.FireTimedEventRequest{OrchestratorId: o.GetId(), EventId: "tev-404"}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown event = %v, want NOT_FOUND", err)
	}
	if _, err := c.FireTimedEvent(ctx, &pb.FireTimedEventRequest{OrchestratorId: "nope", EventId: ev.GetId()}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown orchestrator = %v, want NOT_FOUND", err)
	}
}

// SWI-18: the lane RPCs over gRPC, with the open-lane guard's refusal carrying
// its impact as a status detail.
func TestLaneRPCs(t *testing.T) {
	env := startOrchServer(t)
	ctx := context.Background()
	c := env.client
	o, err := c.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "review-bot"})
	if err != nil {
		t.Fatal(err)
	}
	id := o.GetId()
	ln, err := c.PutLane(ctx, &pb.PutLaneRequest{OrchestratorId: id, Name: "frontend", Description: "UI"})
	if err != nil || ln.GetName() != "frontend" {
		t.Fatalf("PutLane = %+v / %v", ln, err)
	}
	if _, err := c.PutLane(ctx, &pb.PutLaneRequest{OrchestratorId: id, Name: "frontend"}); status.Code(err) != codes.AlreadyExists {
		t.Errorf("duplicate = %v", err)
	}
	// No open-lane worker: saving an un-laned action is guarded.
	put := &pb.PutActionRequest{OrchestratorId: id, SetsLanes: true, Action: &pb.Action{Name: "check", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "check"}}
	_, err = c.PutAction(ctx, put)
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition || len(st.Details()) != 1 {
		t.Fatalf("guarded = %v %v", st.Code(), st.Details())
	}
	if impact, ok := st.Details()[0].(*pb.LaneChangeImpact); !ok || !slices.Equal(impact.GetUnlanedActions(), []string{"check"}) {
		t.Errorf("detail = %+v", st.Details()[0])
	}
	impact, err := c.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{OrchestratorId: id, Change: &pb.CheckLaneChangeRequest_PutAction{PutAction: put}})
	if err != nil || !slices.Equal(impact.GetUnlanedActions(), []string{"check"}) {
		t.Fatalf("CheckLaneChange = %+v / %v", impact, err)
	}
	put.Guard = &pb.LaneGuardConfirm{UnlanedActions: impact.GetUnlanedActions()}
	if _, err := c.PutAction(ctx, put); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutAction(ctx, &pb.PutActionRequest{OrchestratorId: id, Action: put.GetAction()}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("old client = %v", err)
	}
	if _, err := c.SetWorkerLanes(ctx, &pb.SetWorkerLanesRequest{OrchestratorId: id, SandboxId: "nope"}); status.Code(err) != codes.Aborted {
		t.Errorf("SetWorkerLanes stale = %v", err)
	}
	if _, err := c.SetLaneMembers(ctx, &pb.SetLaneMembersRequest{OrchestratorId: id, LaneId: ln.GetId()}); err != nil {
		t.Errorf("SetLaneMembers = %v", err)
	}
	del, err := c.DeleteLane(ctx, &pb.DeleteLaneRequest{OrchestratorId: id, LaneId: ln.GetId()})
	if err != nil || del.GetApplied() == nil {
		t.Errorf("DeleteLane = %+v / %v", del, err)
	}
	if _, err := c.DeleteLane(ctx, &pb.DeleteLaneRequest{OrchestratorId: id, LaneId: ln.GetId()}); status.Code(err) != codes.NotFound {
		t.Errorf("DeleteLane again = %v", err)
	}
	if out, err := c.DismissDroppedWorkerTags(ctx, &pb.OrchestratorIdRequest{OrchestratorId: id}); err != nil || out.GetId() != id {
		t.Errorf("Dismiss = %+v / %v", out, err)
	}
	if _, err := c.GetWorkerBundle(ctx, &pb.GetWorkerBundleRequest{OrchestratorId: id, HostId: "elsewhere", LaneIds: []string{"ln-x"}}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("bundle unknown host = %v", err)
	}
}
