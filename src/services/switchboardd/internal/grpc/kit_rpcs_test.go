package grpc_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

const testSpec = "schemaVersion: \"1\"\nkind: mixin\nname: ruff\n"

// launchOverGRPC creates a sandbox seeded from one source dir, returning it.
func launchOverGRPC(t *testing.T, cli pb.SwitchboardClient, dir string, kits ...*pb.KitRef) *pb.Sandbox {
	t.Helper()
	src := filepath.Join(dir, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	stream, err := cli.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config:  &pb.ConfigSnapshot{Name: "proj"},
		Sources: []*pb.SourceRef{{Path: src}},
		Kits:    kits,
	})
	if err != nil {
		t.Fatal(err)
	}
	sb := drain(t, stream)
	if sb == nil {
		t.Fatal("launch produced no sandbox")
	}
	return sb
}

// drain consumes a LaunchProgress stream and returns the terminal Sandbox.
func drain(t *testing.T, stream interface {
	Recv() (*pb.LaunchProgress, error)
}) *pb.Sandbox {
	t.Helper()
	var done *pb.Sandbox
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return done
		}
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if d := msg.GetDone(); d != nil {
			done = d
		}
	}
}

func TestRefreshSandboxOverGRPC(t *testing.T) {
	cli, dir := startServer(t)
	sb := launchOverGRPC(t, cli, dir)

	// Something the agent wrote must not survive the re-seed.
	scratch := filepath.Join(sb.GetWorkspacePath(), "proj", "scratch.txt")
	if err := os.WriteFile(scratch, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	stream, err := cli.RefreshSandbox(context.Background(), &pb.SandboxIdRequest{SandboxId: sb.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	out := drain(t, stream)
	if out == nil {
		t.Fatal("refresh produced no terminal sandbox")
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING", out.GetState())
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Error("refresh did not rebuild the workspace")
	}
}

func TestRefreshUnknownSandboxErrors(t *testing.T) {
	cli, _ := startServer(t)
	stream, err := cli.RefreshSandbox(context.Background(), &pb.SandboxIdRequest{SandboxId: "nope"})
	if err != nil {
		return // rejected at call time is fine
	}
	if _, err := stream.Recv(); err == nil {
		t.Error("expected refreshing an unknown sandbox to fail")
	}
}

// Kits passed at launch are materialized on the host and recorded on the sandbox.
func TestLaunchWithInlineKitMaterializes(t *testing.T) {
	cli, dir := startServer(t)
	sb := launchOverGRPC(t, cli, dir, &pb.KitRef{
		Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "ruff", SpecYaml: testSpec}},
	})
	kitDir := filepath.Join(dir, "kits", "ruff")
	got, err := os.ReadFile(filepath.Join(kitDir, "spec.yaml"))
	if err != nil {
		t.Fatalf("kit not materialized: %v", err)
	}
	if string(got) != testSpec {
		t.Errorf("spec.yaml = %q, want it written verbatim", got)
	}
	if len(sb.GetKits()) != 1 || sb.GetKits()[0] != kitDir {
		t.Errorf("sandbox kits = %v, want the materialized dir", sb.GetKits())
	}
}

// An external kit source is sbx's to resolve and must reach the runner untouched.
func TestLaunchWithExternalKitSource(t *testing.T) {
	cli, dir := startServer(t)
	const src = "git+https://github.com/docker/sbx-kits-contrib.git#dir=vale"
	sb := launchOverGRPC(t, cli, dir, &pb.KitRef{Ref: &pb.KitRef_Source{Source: src}})
	if len(sb.GetKits()) != 1 || sb.GetKits()[0] != src {
		t.Errorf("sandbox kits = %v, want the source passed through", sb.GetKits())
	}
}

// A bad kit must fail the launch up front, before anything is copied.
func TestLaunchWithBadKitIsRejected(t *testing.T) {
	cli, dir := startServer(t)
	src := filepath.Join(dir, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	stream, err := cli.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config:  &pb.ConfigSnapshot{Name: "proj"},
		Sources: []*pb.SourceRef{{Path: src}},
		Kits:    []*pb.KitRef{{Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "../evil", SpecYaml: testSpec}}}},
	})
	if err == nil {
		_, err = stream.Recv()
	}
	if err == nil {
		t.Fatal("expected an unsafe kit id to fail the launch")
	}
	if !strings.Contains(err.Error(), "kit") {
		t.Errorf("error %q should mention the kit", err)
	}
}

func TestAddSandboxKitOverGRPC(t *testing.T) {
	cli, dir := startServer(t)
	sb := launchOverGRPC(t, cli, dir)

	stream, err := cli.AddSandboxKit(context.Background(), &pb.AddSandboxKitRequest{
		SandboxId: sb.GetId(),
		Kit:       &pb.KitRef{Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "ruff", SpecYaml: testSpec}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := drain(t, stream)
	if out == nil {
		t.Fatal("kit add produced no terminal sandbox")
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING", out.GetState())
	}
	kitDir := filepath.Join(dir, "kits", "ruff")
	if len(out.GetKits()) != 1 || out.GetKits()[0] != kitDir {
		t.Errorf("kits = %v, want the attached kit recorded", out.GetKits())
	}
}

func TestAddSandboxKitRejectsBadRef(t *testing.T) {
	cli, dir := startServer(t)
	sb := launchOverGRPC(t, cli, dir)
	stream, err := cli.AddSandboxKit(context.Background(), &pb.AddSandboxKitRequest{
		SandboxId: sb.GetId(),
		Kit:       &pb.KitRef{},
	})
	if err == nil {
		_, err = stream.Recv()
	}
	if err == nil {
		t.Error("expected an empty kit ref to be rejected")
	}
}

// ValidateKit reports sbx's verdict in the response body — a rejected kit is a
// validation result, not an RPC failure.
func TestValidateKitOverGRPC(t *testing.T) {
	cli, _ := startServer(t)
	resp, err := cli.ValidateKit(context.Background(), &pb.ValidateKitRequest{
		Kit: &pb.KitSpec{Id: "ruff", SpecYaml: testSpec},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetOk() {
		t.Errorf("expected the stub runner to report the kit valid, got %+v", resp)
	}
}

func TestValidateKitRejectsUnsafeID(t *testing.T) {
	cli, _ := startServer(t)
	if _, err := cli.ValidateKit(context.Background(), &pb.ValidateKitRequest{
		Kit: &pb.KitSpec{Id: "../evil", SpecYaml: testSpec},
	}); err == nil {
		t.Error("expected an unsafe kit id to be rejected")
	}
}
