package ui

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// fakeDaemonServer serves a *fakeDaemon over gRPC so multi-host tests can wire
// several distinct fakes through the real client.Manager (each connected host
// then routes to its own fake, exactly as production routes to its own daemon).
// Only the RPCs the multi-host screens use are served.
type fakeDaemonServer struct {
	pb.UnimplementedSwitchboardServer
	d *fakeDaemon
}

func (s *fakeDaemonServer) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: s.d.HostID(), DaemonVersion: s.d.daemonVer, WorkspaceRoot: s.d.workspace}, nil
}

func (s *fakeDaemonServer) ListSandboxes(ctx context.Context, _ *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	sbs, err := s.d.List(ctx)
	return &pb.ListSandboxesResponse{Sandboxes: sbs}, err
}

func (s *fakeDaemonServer) Subscribe(_ *pb.SubscribeRequest, stream pb.Switchboard_SubscribeServer) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev, ok := <-s.d.events:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

func (s *fakeDaemonServer) AckNotification(ctx context.Context, req *pb.AckNotificationRequest) (*pb.AckNotificationResponse, error) {
	return &pb.AckNotificationResponse{Acked: uint32(len(req.GetNotificationIds()))}, s.d.AckNotifications(ctx, req.GetNotificationIds())
}

func (s *fakeDaemonServer) ListOrchestrators(ctx context.Context, _ *pb.ListOrchestratorsRequest) (*pb.ListOrchestratorsResponse, error) {
	list, err := s.d.ListOrchestrators(ctx)
	return &pb.ListOrchestratorsResponse{Orchestrators: list}, err
}

func (s *fakeDaemonServer) GetOrchestrator(ctx context.Context, r *pb.OrchestratorIdRequest) (*pb.OrchestratorDetail, error) {
	return s.d.GetOrchestrator(ctx, r.GetOrchestratorId())
}

func (s *fakeDaemonServer) CreateOrchestrator(ctx context.Context, r *pb.CreateOrchestratorRequest) (*pb.Orchestrator, error) {
	return s.d.CreateOrchestrator(ctx, r.GetName())
}

func (s *fakeDaemonServer) RenameOrchestrator(ctx context.Context, r *pb.RenameOrchestratorRequest) (*pb.Orchestrator, error) {
	return s.d.RenameOrchestrator(ctx, r.GetOrchestratorId(), r.GetName())
}

func (s *fakeDaemonServer) RemoveOrchestrator(ctx context.Context, r *pb.OrchestratorIdRequest) (*pb.RemoveOrchestratorResponse, error) {
	return s.d.RemoveOrchestrator(ctx, r.GetOrchestratorId())
}

func (s *fakeDaemonServer) SetOrchestratorHosts(ctx context.Context, r *pb.SetOrchestratorHostsRequest) (*pb.Orchestrator, error) {
	return s.d.SetOrchestratorHosts(ctx, r.GetOrchestratorId(), r.GetHosts())
}

func (s *fakeDaemonServer) GetWorkerBundle(ctx context.Context, r *pb.GetWorkerBundleRequest) (*pb.WorkerBundle, error) {
	return s.d.GetWorkerBundle(ctx, r.GetOrchestratorId())
}

func (s *fakeDaemonServer) AttachWorker(ctx context.Context, r *pb.AttachWorkerRequest) (*pb.WorkerView, error) {
	return s.d.AttachWorker(ctx, r.GetOrchestratorId(), r.GetHostId(), r.GetSandboxId())
}

func (s *fakeDaemonServer) DetachWorker(ctx context.Context, r *pb.DetachWorkerRequest) (*pb.DetachWorkerResponse, error) {
	return &pb.DetachWorkerResponse{}, s.d.DetachWorker(ctx, r.GetOrchestratorId(), r.GetSandboxId())
}

func (s *fakeDaemonServer) DetachController(ctx context.Context, r *pb.DetachControllerRequest) (*pb.Sandbox, error) {
	return s.d.DetachController(ctx, r.GetSandboxId(), r.GetOrchestratorId(), r.GetForce())
}

func (s *fakeDaemonServer) MarkWorkerFree(ctx context.Context, r *pb.MarkWorkerFreeRequest) (*pb.Run, error) {
	return s.d.MarkWorkerFree(ctx, r.GetOrchestratorId(), r.GetSandboxId(), r.GetFailed(), r.GetNote())
}

func (s *fakeDaemonServer) PutAction(ctx context.Context, r *pb.PutActionRequest) (*pb.Action, error) {
	return s.d.PutAction(ctx, r.GetOrchestratorId(), r.GetAction())
}

func (s *fakeDaemonServer) DeleteAction(ctx context.Context, r *pb.DeleteActionRequest) (*pb.DeleteActionResponse, error) {
	ev, err := s.d.DeleteAction(ctx, r.GetOrchestratorId(), r.GetName())
	return &pb.DeleteActionResponse{DisabledEvents: ev}, err
}

func (s *fakeDaemonServer) PutTimedEvent(ctx context.Context, r *pb.PutTimedEventRequest) (*pb.TimedEvent, error) {
	return s.d.PutTimedEvent(ctx, r.GetOrchestratorId(), r.GetEvent())
}

func (s *fakeDaemonServer) DeleteTimedEvent(ctx context.Context, r *pb.DeleteTimedEventRequest) (*pb.DeleteTimedEventResponse, error) {
	return &pb.DeleteTimedEventResponse{}, s.d.DeleteTimedEvent(ctx, r.GetOrchestratorId(), r.GetEventId())
}

func (s *fakeDaemonServer) FireTimedEvent(ctx context.Context, r *pb.FireTimedEventRequest) (*pb.Run, error) {
	return s.d.FireTimedEvent(ctx, r.GetOrchestratorId(), r.GetEventId())
}

func (s *fakeDaemonServer) CallAction(ctx context.Context, r *pb.CallActionRequest) (*pb.Run, error) {
	return s.d.CallAction(ctx, r.GetOrchestratorId(), r.GetAction(), r.GetArguments())
}

func (s *fakeDaemonServer) ListRuns(ctx context.Context, r *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	return s.d.ListRuns(ctx, r.GetOrchestratorId())
}

func (s *fakeDaemonServer) GetRun(ctx context.Context, r *pb.GetRunRequest) (*pb.Run, error) {
	return s.d.GetRun(ctx, r.GetOrchestratorId(), r.GetRunId())
}

func (s *fakeDaemonServer) CancelRun(ctx context.Context, r *pb.CancelRunRequest) (*pb.Run, error) {
	return s.d.CancelRun(ctx, r.GetOrchestratorId(), r.GetRunId())
}

// serveFake starts a fake daemon on a unix socket and returns the path.
func serveFake(t *testing.T, d *fakeDaemon) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), d.HostID()+".sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, &fakeDaemonServer{d: d})
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return sock
}

// twoFakeHosts wires a model to two fake daemons ("hosta"/alpha and
// "hostb"/beta) served over gRPC and connected through a manager, with a host
// store holding both plus a third, unconnected ssh host "hostc"/gamma. The
// active daemon is alpha's fake.
func twoFakeHosts(t *testing.T, a, b *fakeDaemon) (Model, *client.Manager, *store.HostStore) {
	t.Helper()
	a.hostID, b.hostID = "hosta", "hostb"
	if a.events == nil {
		a.events = make(chan *pb.Event, 8)
	}
	if b.events == nil {
		b.events = make(chan *pb.Event, 8)
	}
	socks := map[string]string{"hosta": serveFake(t, a), "hostb": serveFake(t, b)}
	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	for _, h := range []store.KnownHost{
		{ID: "hosta", DisplayName: "alpha", Kind: "ssh", SSHTarget: "me@alpha"},
		{ID: "hostb", DisplayName: "beta", Kind: "ssh", SSHTarget: "me@beta", SSHOptions: []string{"-p", "2222"}},
		{ID: "hostc", DisplayName: "gamma", Kind: "ssh", SSHTarget: "me@gamma"},
	} {
		if _, err := hs.Save(h); err != nil {
			t.Fatal(err)
		}
	}
	mgr := client.NewManager()
	mgr.SetDialFunc(func(ctx context.Context, e client.HostEntry) (*client.Conn, error) {
		return client.DialLocal(ctx, socks[e.ID])
	})
	for _, id := range []string{"hosta", "hostb", "hostc"} {
		known, _ := hs.Get(id)
		mgr.Upsert(toEntry(*known))
	}
	for _, id := range []string{"hosta", "hostb"} {
		if err := mgr.Connect(context.Background(), id); err != nil {
			t.Fatalf("connect %s: %v", id, err)
		}
	}
	m := sized(New(a, "/work").WithHosts(mgr, hs, "hosta").WithNotifier(&recordNotifier{}))
	return m, mgr, hs
}
