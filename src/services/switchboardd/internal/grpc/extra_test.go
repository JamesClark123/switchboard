package grpc_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/resources"
)

func TestLaunchBlockedLowResources(t *testing.T) {
	// Force the resource probe to report almost no free space so the launch is
	// blocked (FR-012f) without override.
	orig := resources.AvailableBytesFunc
	resources.AvailableBytesFunc = func(string) (uint64, error) { return 1, nil }
	defer func() { resources.AvailableBytesFunc = orig }()

	client, dir := startServer(t)
	src := filepath.Join(dir, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "big"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	stream, err := client.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config:  &pb.ConfigSnapshot{Name: "x"},
		Sources: []*pb.SourceRef{{Path: src}},
		// OverrideResourceWarning deliberately false.
	})
	if err != nil {
		t.Fatal(err)
	}
	var blocked *pb.ResourceReport
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if b := msg.GetBlocked(); b != nil {
			blocked = b
		}
	}
	if blocked == nil || blocked.GetOk() {
		t.Fatal("expected a low-resource block")
	}
}

func TestRPCErrorsOnUnknownID(t *testing.T) {
	client, _ := startServer(t)
	ctx := context.Background()
	if _, err := client.StopSandbox(ctx, &pb.SandboxIdRequest{SandboxId: "nope"}); err == nil {
		t.Error("expected StopSandbox error for unknown id")
	}
	if _, err := client.DestroySandbox(ctx, &pb.SandboxIdRequest{SandboxId: "nope"}); err == nil {
		t.Error("expected DestroySandbox error for unknown id")
	}
	if _, err := client.RenameSandbox(ctx, &pb.RenameSandboxRequest{SandboxId: "nope", DisplayName: "x"}); err == nil {
		t.Error("expected RenameSandbox error for unknown id")
	}
}

func TestRestartUnknownIDErrors(t *testing.T) {
	client, _ := startServer(t)
	stream, err := client.RestartSandbox(context.Background(), &pb.SandboxIdRequest{SandboxId: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	// The error surfaces on the first Recv.
	_, rerr := stream.Recv()
	if rerr == nil {
		t.Error("expected RestartSandbox error for unknown id")
	}
}

func TestListSourceCandidatesDefaultRoot(t *testing.T) {
	client, _ := startServer(t)
	// Empty root defaults to "." (the test process cwd), which always exists.
	if _, err := client.ListSourceCandidates(context.Background(), &pb.ListSourceCandidatesRequest{}); err != nil {
		t.Fatalf("ListSourceCandidates default root: %v", err)
	}
}
