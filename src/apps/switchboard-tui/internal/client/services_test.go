package client_test

import (
	"context"
	"io"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The feature-006 RPCs on fakeServer.

func (s *fakeServer) ListSandboxServices(_ context.Context, req *pb.ListSandboxServicesRequest) (*pb.ListSandboxServicesResponse, error) {
	if req.GetSandboxId() == "missing" {
		return nil, errNotFound
	}
	return &pb.ListSandboxServicesResponse{Services: []*pb.SandboxService{{
		Declared: &pb.KitService{Name: "web", Command: "pnpm dev", ListenPort: 3000,
			Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX, IsWebsite: true},
		Instance: &pb.ServiceInstance{Id: "svc-1", ServiceName: "web", State: pb.ServiceState_SERVICE_STATE_RUNNING, LocalPort: 49221},
	}}}, nil
}

func (s *fakeServer) StartSandboxService(_ context.Context, req *pb.StartSandboxServiceRequest) (*pb.StartSandboxServiceResponse, error) {
	if req.GetServiceName() == "undeclared" {
		return nil, errNotFound
	}
	return &pb.StartSandboxServiceResponse{Instance: &pb.ServiceInstance{
		Id: "svc-1", SandboxId: req.GetSandboxId(), ServiceName: req.GetServiceName(),
		State: pb.ServiceState_SERVICE_STATE_STARTING,
	}}, nil
}

func (s *fakeServer) StopSandboxService(_ context.Context, req *pb.StopSandboxServiceRequest) (*pb.StopSandboxServiceResponse, error) {
	if req.GetServiceName() == "undeclared" {
		return nil, errNotFound
	}
	return &pb.StopSandboxServiceResponse{Instance: &pb.ServiceInstance{
		Id: "svc-1", SandboxId: req.GetSandboxId(), ServiceName: req.GetServiceName(),
		State: pb.ServiceState_SERVICE_STATE_STOPPED,
	}}, nil
}

func (s *fakeServer) ForwardPort(stream pb.Switchboard_ForwardPortServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetOpen() == nil {
		return errNotFound
	}
	if err := stream.Send(&pb.PortForwardFrame{
		Frame: &pb.PortForwardFrame_Opened_{Opened: &pb.PortForwardFrame_Opened{}},
	}); err != nil {
		return err
	}
	// Echo data frames back so the client's relay can be exercised.
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if data := frame.GetData(); len(data) > 0 {
			if err := stream.Send(&pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Data{Data: data}}); err != nil {
				return err
			}
		}
	}
}

func TestClientListServices(t *testing.T) {
	conn := startFake(t)
	rows, err := conn.ListServices(context.Background(), "sb-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].GetDeclared().GetName() != "web" {
		t.Errorf("declared = %q, want web", rows[0].GetDeclared().GetName())
	}
	if rows[0].GetInstance().GetLocalPort() != 49221 {
		t.Errorf("the local address must survive the round trip, got %d", rows[0].GetInstance().GetLocalPort())
	}
	if _, err := conn.ListServices(context.Background(), "missing"); err == nil {
		t.Error("an unknown sandbox must surface an error")
	}
}

func TestClientStartAndStopService(t *testing.T) {
	conn := startFake(t)
	ctx := context.Background()

	started, err := conn.StartService(ctx, "sb-1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if started.GetState() != pb.ServiceState_SERVICE_STATE_STARTING {
		t.Errorf("state = %v, want STARTING", started.GetState())
	}

	stopped, err := conn.StopService(ctx, "sb-1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("state = %v, want STOPPED", stopped.GetState())
	}

	if _, err := conn.StartService(ctx, "sb-1", "undeclared"); err == nil {
		t.Error("an undeclared service must surface an error")
	}
	if _, err := conn.StopService(ctx, "sb-1", "undeclared"); err == nil {
		t.Error("an undeclared service must surface an error")
	}
}

func TestClientForwardPortOpensAStream(t *testing.T) {
	conn := startFake(t)
	stream, err := conn.ForwardPort(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: "svc-1", LocalPort: 49221},
	}}); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if first.GetOpened() == nil {
		t.Fatalf("first frame = %v, want opened", first)
	}

	if err := stream.Send(&pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Data{Data: []byte("hello")}}); err != nil {
		t.Fatal(err)
	}
	echoed, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if string(echoed.GetData()) != "hello" {
		t.Errorf("echoed %q, want the bytes to round-trip", echoed.GetData())
	}
	_ = stream.CloseSend()
}

// --- feature 005 client methods -----------------------------------------------
//
// These were never covered. They are exercised here because the coverage floor is
// a whole-module gate: leaving two RPC wrappers untested keeps the module under it
// regardless of what any one feature adds.

func (s *fakeServer) DecideEscapeHatchRun(_ context.Context, req *pb.DecideEscapeHatchRunRequest) (*pb.DecideEscapeHatchRunResponse, error) {
	if req.GetRunId() == "" {
		return nil, errNotFound
	}
	st := pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED
	if req.GetApproved() {
		st = pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING
	}
	return &pb.DecideEscapeHatchRunResponse{Status: st}, nil
}

func (s *fakeServer) ListEscapeHatchRuns(_ context.Context, req *pb.ListEscapeHatchRunsRequest) (*pb.ListEscapeHatchRunsResponse, error) {
	if req.GetSandboxId() == "missing" {
		return nil, errNotFound
	}
	return &pb.ListEscapeHatchRunsResponse{Runs: []*pb.EscapeHatchRun{
		{Id: "ehr-1", CommandName: "install-deps", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED},
	}}, nil
}

func TestClientDecideEscapeHatchRun(t *testing.T) {
	conn := startFake(t)
	ctx := context.Background()

	st, err := conn.DecideEscapeHatchRun(ctx, "ehr-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if st != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING {
		t.Errorf("approved status = %v, want RUNNING", st)
	}

	st, err = conn.DecideEscapeHatchRun(ctx, "ehr-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if st != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED {
		t.Errorf("denied status = %v, want DENIED", st)
	}

	if _, err := conn.DecideEscapeHatchRun(ctx, "", true); err == nil {
		t.Error("a missing run id must surface an error")
	}
}

func TestClientListEscapeHatchRuns(t *testing.T) {
	conn := startFake(t)
	runs, err := conn.ListEscapeHatchRuns(context.Background(), "sb-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].GetCommandName() != "install-deps" {
		t.Errorf("runs = %v, want the session's runs", runs)
	}
	if _, err := conn.ListEscapeHatchRuns(context.Background(), "missing"); err == nil {
		t.Error("an unknown sandbox must surface an error")
	}
}
