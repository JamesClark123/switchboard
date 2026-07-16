package grpc_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// testRunner is a no-op Runner for the gRPC integration test.
type testRunner struct {
	running map[string]bool
	kitAdds []string // kit sources passed to `sbx kit add`
}

func (r *testRunner) Launch(_ context.Context, spec sandbox.LaunchSpec, _ func(string)) (string, error) {
	ref := spec.Name // sbx names by the human name (mirrors the real runner)
	if ref == "" {
		ref = spec.SandboxID
	}
	r.running[ref] = true
	return ref, nil
}
func (r *testRunner) Stop(_ context.Context, ref string) error    { r.running[ref] = false; return nil }
func (r *testRunner) Start(_ context.Context, ref string) error   { r.running[ref] = true; return nil }
func (r *testRunner) Destroy(_ context.Context, ref string) error { delete(r.running, ref); return nil }
func (r *testRunner) IsRunning(_ context.Context, ref string) (bool, error) {
	return r.running[ref], nil
}
func (r *testRunner) CloneRepo(_ context.Context, _, dest string, _ func(string)) error {
	return os.MkdirAll(dest, 0o755)
}
func (r *testRunner) AddKit(_ context.Context, _, kitSource string, _ func(string)) error {
	r.kitAdds = append(r.kitAdds, kitSource)
	return nil
}
func (r *testRunner) ValidateKit(_ context.Context, _ string) (string, error) { return "", nil }

func startServer(t *testing.T) (pb.SwitchboardClient, string) {
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
	mgr := sandbox.NewManager(reg, &testRunner{running: map[string]bool{}}, ws, "host-1")
	srv := sbgrpc.NewServer(sbgrpc.Config{
		Manager: mgr, HostID: "host-1", DaemonVersion: "test", WorkspaceRoot: ws,
		KitRoot: filepath.Join(dir, "kits"),
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
	return pb.NewSwitchboardClient(conn), dir
}

func launch(t *testing.T, client pb.SwitchboardClient, srcDir string) *pb.Sandbox {
	t.Helper()
	stream, err := client.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config:                  &pb.ConfigSnapshot{Name: "feature-work"},
		Sources:                 []*pb.SourceRef{{Path: srcDir}},
		OverrideResourceWarning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var done *pb.Sandbox
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if d := msg.GetDone(); d != nil {
			done = d
		}
	}
	if done == nil {
		t.Fatal("no terminal Sandbox received")
	}
	return done
}

func TestLaunchListStopRestartDestroyOverGRPC(t *testing.T) {
	client, dir := startServer(t)

	src := filepath.Join(dir, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sb := launch(t, client, src)
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("launched state = %v", sb.GetState())
	}

	// ListSandboxes shows it.
	list, err := client.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetSandboxes()) != 1 {
		t.Fatalf("expected 1 sandbox, got %d", len(list.GetSandboxes()))
	}

	// Stop retains.
	stopped, err := client.StopSandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.GetState() != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Errorf("stopped state = %v", stopped.GetState())
	}
	if _, err := os.Stat(sb.GetWorkspacePath()); err != nil {
		t.Errorf("workspace must be retained after stop: %v", err)
	}

	// Restart (streaming).
	rs, err := client.RestartSandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := rs.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}

	// Rename requires the sandbox to be stopped first.
	if _, err := client.StopSandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}
	rn, err := client.RenameSandbox(ctx, &pb.RenameSandboxRequest{SandboxId: sb.GetId(), DisplayName: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if rn.GetDisplayName() != "renamed" {
		t.Errorf("rename failed: %q", rn.GetDisplayName())
	}

	// Destroy deletes the copy.
	dr, err := client.DestroySandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if !dr.GetDeletedWorkspace() {
		t.Error("expected workspace deleted on destroy")
	}
	if _, err := os.Stat(sb.GetWorkspacePath()); !os.IsNotExist(err) {
		t.Errorf("workspace should be gone: %v", err)
	}
}

func TestGetDaemonInfoAndSources(t *testing.T) {
	client, dir := startServer(t)
	info, err := client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetHostId() != "host-1" {
		t.Errorf("host id = %q", info.GetHostId())
	}

	// A candidate directory under dir should be enumerated.
	if err := os.MkdirAll(filepath.Join(dir, "candidate"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := client.ListSourceCandidates(context.Background(), &pb.ListSourceCandidatesRequest{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetCandidates()) == 0 {
		t.Error("expected at least one source candidate")
	}
}
