package grpc_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/escapehatch"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// startServerWithEscapeHatch wires a server with an escape-hatch service over a
// registry we can pre-populate, and returns the client + service for direct
// invocation.
func startServerWithEscapeHatch(t *testing.T) (pb.SwitchboardClient, *escapehatch.Service, *registry.Registry, string) {
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
	eh := escapehatch.New(mgr, hub, nil)
	srv := sbgrpc.NewServer(sbgrpc.Config{Manager: mgr, HostID: "host-1", WorkspaceRoot: ws, Hub: hub, EscapeHatch: eh})

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
	return pb.NewSwitchboardClient(conn), eh, reg, dir
}

func TestListEscapeHatchRunsRPC(t *testing.T) {
	client, eh, reg, dir := startServerWithEscapeHatch(t)
	ctx := context.Background()

	// Empty to start.
	resp, err := client.ListEscapeHatchRuns(ctx, &pb.ListEscapeHatchRunsRequest{SandboxId: "sb1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetRuns()) != 0 {
		t.Fatalf("want 0 runs, got %d", len(resp.GetRuns()))
	}

	// Seed a running sandbox with an auto-run command, then invoke it.
	ws := filepath.Join(dir, "ws")
	if err := reg.Put(&pb.Sandbox{
		Id:            "sb1",
		State:         pb.SandboxState_SANDBOX_STATE_RUNNING,
		WorkspacePath: ws,
		Agent:         &pb.AgentSession{Spec: &pb.AgentSpec{Kind: "claude"}},
		EscapeHatchCommands: []*pb.EscapeHatchCommand{
			{Name: "hello", Command: "echo hi", ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eh.Invoke("sb1", "hello"); err != nil {
		t.Fatal(err)
	}

	// The run shows up via the RPC.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, _ = client.ListEscapeHatchRuns(ctx, &pb.ListEscapeHatchRunsRequest{SandboxId: "sb1"})
		if len(resp.GetRuns()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(resp.GetRuns()) != 1 || resp.GetRuns()[0].GetCommandName() != "hello" {
		t.Fatalf("want 1 run named hello, got %+v", resp.GetRuns())
	}

	// Empty sandbox_id returns all runs on the host.
	all, err := client.ListEscapeHatchRuns(ctx, &pb.ListEscapeHatchRunsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.GetRuns()) != 1 {
		t.Errorf("empty filter should return all runs, got %d", len(all.GetRuns()))
	}
}

func TestDecideEscapeHatchRunRPC(t *testing.T) {
	client, _, _, _ := startServerWithEscapeHatch(t)
	// Deciding an unknown run is a harmless no-op that returns UNSPECIFIED.
	resp, err := client.DecideEscapeHatchRun(context.Background(), &pb.DecideEscapeHatchRunRequest{RunId: "ehr-nope", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStatus() != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_UNSPECIFIED {
		t.Errorf("unknown run should resolve UNSPECIFIED, got %v", resp.GetStatus())
	}
}
