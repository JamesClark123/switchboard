package grpc_test

import (
	"context"
	"errors"
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
	"google.golang.org/protobuf/types/known/timestamppb"
)

// reconcileServer starts a server over runner with one RUNNING sandbox record
// (container "x") already in its registry.
func reconcileServer(t *testing.T, runner *testRunner) pb.SwitchboardClient {
	t.Helper()
	client, _ := reconcileServerWithManager(t, runner)
	return client
}

// reconcileServerWithManager is reconcileServer that also returns the manager.
func reconcileServerWithManager(t *testing.T, runner *testRunner) (pb.SwitchboardClient, *sandbox.Manager) {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	if err := reg.Put(&pb.Sandbox{
		Id: "id-x", DisplayName: "x", ContainerRef: "x",
		State: pb.SandboxState_SANDBOX_STATE_RUNNING, UpdatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := sandbox.NewManager(reg, runner, ws, "host-1")
	srv := sbgrpc.NewServer(sbgrpc.Config{
		Manager: mgr, HostID: "host-1",
		DaemonVersion: "test", WorkspaceRoot: ws, KitRoot: filepath.Join(dir, "kits"),
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
	return pb.NewSwitchboardClient(conn), mgr
}

func listedState(t *testing.T, client pb.SwitchboardClient) pb.SandboxState {
	t.Helper()
	list, err := client.ListSandboxes(context.Background(), &pb.ListSandboxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetSandboxes()) != 1 {
		t.Fatalf("listed %d sandboxes; want 1", len(list.GetSandboxes()))
	}
	return list.GetSandboxes()[0].GetState()
}

// SWI-13: a reconnecting client's list reflects the runtime, once per window.
func TestListSandboxesReconcilesWithTheRuntime(t *testing.T) {
	runner := &testRunner{running: map[string]bool{}} // "x" is not listed: it stopped
	client := reconcileServer(t, runner)

	if got := listedState(t, client); got != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Errorf("list after the container stopped = %v; want STOPPED", got)
	}
	if got := listedState(t, client); got != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Errorf("second list = %v; want STOPPED", got)
	}
	if runner.lists.Load() != 1 {
		t.Errorf("two lists within the window read the runtime %d times; want 1", runner.lists.Load())
	}
}

// SWI-13: an unreadable runtime never turns a running sandbox STOPPED; the
// list still answers with the last-known state.
func TestListSandboxesKeepsStateWhenTheRuntimeIsUnreadable(t *testing.T) {
	runner := &testRunner{running: map[string]bool{}, listErr: errors.New("sbx: command not found")}
	client := reconcileServer(t, runner)

	if got := listedState(t, client); got != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("list with an unreadable runtime = %v; want RUNNING kept", got)
	}
	if got := listedState(t, client); got != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("second list = %v; want RUNNING kept", got)
	}
	if runner.lists.Load() != 1 {
		t.Errorf("a failed reconcile was retried within the window (%d reads); want 1", runner.lists.Load())
	}
}

// SWI-13: a list that finds a sandbox stopped answers at once with the new
// state; the teardown its announcement runs (terminals, agent, services: up to
// tens of seconds) happens off the request path.
func TestListSandboxesDoesNotWaitForTheTeardown(t *testing.T) {
	runner := &testRunner{running: map[string]bool{}} // "x" stopped outside the daemon
	client, mgr := reconcileServerWithManager(t, runner)
	release := make(chan struct{})
	announced := make(chan *pb.Sandbox, 1)
	// Stands in for the server's change observer, blocked like a slow teardown.
	mgr.SetOnChange(func(sb *pb.Sandbox) {
		announced <- sb
		<-release
	})
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	list, err := client.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if err != nil {
		t.Fatalf("ListSandboxes while the teardown is blocked: %v", err)
	}
	if got := list.GetSandboxes()[0].GetState(); got != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Errorf("listed state = %v; want STOPPED", got)
	}
	select {
	case sb := <-announced:
		if sb.GetState() != pb.SandboxState_SANDBOX_STATE_STOPPED {
			t.Errorf("announced %v; want STOPPED", sb.GetState())
		}
	case <-time.After(2 * time.Second):
		t.Error("the change was never announced")
	}
}
