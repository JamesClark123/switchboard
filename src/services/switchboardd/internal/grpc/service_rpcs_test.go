package grpc_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/portforward"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// serviceServer starts a daemon wired with a port-forwarding supervisor.
func serviceServer(t *testing.T) (pb.SwitchboardClient, *sandbox.Manager, *portforward.Supervisor, string) {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	ws := filepath.Join(dir, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &testRunner{running: map[string]bool{}}
	mgr := sandbox.NewManager(reg, runner, ws, "host-1")
	hub := agent.NewHub("host-1")
	services := portforward.NewSupervisor(mgr, runner, hub)
	services.SetReadinessTimeout(500 * time.Millisecond)
	services.SetStopGrace(200 * time.Millisecond)

	srv := sbgrpc.NewServer(sbgrpc.Config{
		Manager: mgr, HostID: "host-1", DaemonVersion: "test", WorkspaceRoot: ws,
		KitRoot: filepath.Join(dir, "kits"), Hub: hub, Services: services,
	})
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
	return pb.NewSwitchboardClient(conn), mgr, services, dir
}

// launchWithServices creates a sandbox carrying declared services.
func launchWithServices(t *testing.T, mgr *sandbox.Manager, dir string, svcs ...*pb.KitService) *pb.Sandbox {
	t.Helper()
	src := filepath.Join(dir, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	sb, err := mgr.Launch(context.Background(), sandbox.LaunchRequest{
		Config:   &pb.ConfigSnapshot{Name: "svc-test"},
		Sources:  []*pb.SourceRef{{Path: src}},
		Services: svcs,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func TestListSandboxServicesOverGRPC(t *testing.T) {
	client, mgr, _, dir := serviceServer(t)
	sb := launchWithServices(t, mgr, dir,
		&pb.KitService{Name: "web", Command: "sleep 30", ListenPort: 3000, Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX},
	)

	resp, err := client.ListSandboxServices(context.Background(), &pb.ListSandboxServicesRequest{SandboxId: sb.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetServices()) != 1 {
		t.Fatalf("services = %d, want 1", len(resp.GetServices()))
	}
	row := resp.GetServices()[0]
	if row.GetDeclared().GetName() != "web" {
		t.Errorf("declared = %q, want web", row.GetDeclared().GetName())
	}
	// FR-045 / SC-005: listing starts nothing.
	if row.GetInstance() != nil {
		t.Error("listing must not create an instance")
	}
}

// The allowlist boundary, over the wire: an undeclared name starts nothing.
func TestStartSandboxServiceRejectsAnUndeclaredName(t *testing.T) {
	client, mgr, supervisor, dir := serviceServer(t)
	sb := launchWithServices(t, mgr, dir,
		&pb.KitService{Name: "web", Command: "sleep 30", ListenPort: 3000, Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX},
	)

	_, err := client.StartSandboxService(context.Background(), &pb.StartSandboxServiceRequest{
		SandboxId: sb.GetId(), ServiceName: "curl evil.example.com",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
	if got := len(supervisor.Instances().ListBySandbox(sb.GetId())); got != 0 {
		t.Errorf("a rejected start created %d instances, want 0", got)
	}
}

func TestStartAndStopSandboxServiceOverGRPC(t *testing.T) {
	client, mgr, _, dir := serviceServer(t)
	sb := launchWithServices(t, mgr, dir,
		&pb.KitService{Name: "web", Command: "sleep 30", ListenPort: 3000, Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX},
	)
	ctx := context.Background()

	started, err := client.StartSandboxService(ctx, &pb.StartSandboxServiceRequest{SandboxId: sb.GetId(), ServiceName: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if started.GetInstance().GetState() != pb.ServiceState_SERVICE_STATE_STARTING {
		t.Errorf("state = %v, want STARTING — readiness arrives on the event stream", started.GetInstance().GetState())
	}

	stopped, err := client.StopSandboxService(ctx, &pb.StopSandboxServiceRequest{SandboxId: sb.GetId(), ServiceName: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.GetInstance().GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("state = %v, want STOPPED", stopped.GetInstance().GetState())
	}
}

func TestUnknownSandboxIsNotFound(t *testing.T) {
	client, _, _, _ := serviceServer(t)
	ctx := context.Background()

	if _, err := client.ListSandboxServices(ctx, &pb.ListSandboxServicesRequest{SandboxId: "nope"}); status.Code(err) != codes.NotFound {
		t.Errorf("list: code = %v, want NotFound", status.Code(err))
	}
	if _, err := client.StartSandboxService(ctx, &pb.StartSandboxServiceRequest{SandboxId: "nope", ServiceName: "web"}); status.Code(err) != codes.NotFound {
		t.Errorf("start: code = %v, want NotFound", status.Code(err))
	}
}

// FR-050: forwarding a service that is not running is refused, so a stale client
// cannot present a dead address as working.
func TestForwardPortRefusesANonRunningInstance(t *testing.T) {
	client, mgr, _, dir := serviceServer(t)
	sb := launchWithServices(t, mgr, dir,
		&pb.KitService{Name: "web", Command: "sleep 30", ListenPort: 3000, Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX},
	)
	started, err := client.StartSandboxService(context.Background(), &pb.StartSandboxServiceRequest{
		SandboxId: sb.GetId(), ServiceName: "web",
	})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := client.ForwardPort(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: started.GetInstance().GetId(), LocalPort: 49221},
	}}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition for a service that is not RUNNING", status.Code(err))
	}
}

func TestForwardPortRejectsANonOpenFirstFrame(t *testing.T) {
	client, _, _, _ := serviceServer(t)
	stream, err := client.ForwardPort(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Data{Data: []byte("hi")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// FR-048 / US4-5: stopping the sandbox stops everything it was running.
func TestStoppingASandboxCascadesToItsServices(t *testing.T) {
	client, mgr, supervisor, dir := serviceServer(t)
	sb := launchWithServices(t, mgr, dir,
		&pb.KitService{Name: "web", Command: "sleep 30", ListenPort: 3000, Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX},
	)
	ctx := context.Background()

	if _, err := client.StartSandboxService(ctx, &pb.StartSandboxServiceRequest{SandboxId: sb.GetId(), ServiceName: "web"}); err != nil {
		t.Fatal(err)
	}
	if got := len(supervisor.Instances().ActiveBySandbox(sb.GetId())); got != 1 {
		t.Fatalf("active before stop = %d, want 1", got)
	}

	if _, err := client.StopSandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(supervisor.Instances().ActiveBySandbox(sb.GetId())) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("stopping the sandbox must stop every service it was running")
}
