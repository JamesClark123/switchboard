package grpc_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestGetVSCodeTarget(t *testing.T) {
	client, dir := startServer(t)
	src := filepath.Join(dir, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	// Launch a sandbox so it has a container_ref.
	stream, err := client.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config:                  &pb.ConfigSnapshot{Name: "x"},
		Sources:                 []*pb.SourceRef{{Path: src}},
		OverrideResourceWarning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if d := msg.GetDone(); d != nil {
			id = d.GetId()
		}
	}

	tgt, err := client.GetVSCodeTarget(context.Background(), &pb.SandboxIdRequest{SandboxId: id})
	if err != nil {
		t.Fatal(err)
	}
	// The target is the sandbox's controlled workspace folder on the host (the
	// retained verbatim copy), not the running container: an absolute path whose
	// final segment is the sandbox NAME (config label "x" here).
	wp := tgt.GetWorkspacePath()
	if !filepath.IsAbs(wp) {
		t.Errorf("workspace path should be absolute: %q", wp)
	}
	if filepath.Base(wp) != "x" {
		t.Errorf("workspace path %q should end with the sandbox name %q", wp, "x")
	}
	_ = id

	// Unknown id errors.
	if _, err := client.GetVSCodeTarget(context.Background(), &pb.SandboxIdRequest{SandboxId: "nope"}); err == nil {
		t.Error("expected error for unknown sandbox")
	}
}
