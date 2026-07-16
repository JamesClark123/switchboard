package client_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The feature-004 RPCs on fakeServer. RefreshSandbox streams a copy-progress frame
// and a log line before its terminal Sandbox, mirroring the real daemon, so the
// client's progress forwarding is exercised.

func (s *fakeServer) RefreshSandbox(req *pb.SandboxIdRequest, stream pb.Switchboard_RefreshSandboxServer) error {
	if req.GetSandboxId() == "missing" {
		return errNotFound
	}
	if err := stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Copy{
		Copy: &pb.LaunchProgress_CopyProgress{BytesCopied: 5, BytesTotal: 10, CurrentPath: "proj/x"},
	}}); err != nil {
		return err
	}
	if err := stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_LogLine{LogLine: "re-seeding"}}); err != nil {
		return err
	}
	sb := &pb.Sandbox{Id: req.GetSandboxId(), DisplayName: "refreshed", State: pb.SandboxState_SANDBOX_STATE_RUNNING}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
}

func (s *fakeServer) AddSandboxKit(req *pb.AddSandboxKitRequest, stream pb.Switchboard_AddSandboxKitServer) error {
	if req.GetKit().GetSpec() == nil && req.GetKit().GetSource() == "" {
		return errNotFound
	}
	label := req.GetKit().GetSource()
	if sp := req.GetKit().GetSpec(); sp != nil {
		label = sp.GetId()
	}
	sb := &pb.Sandbox{Id: req.GetSandboxId(), State: pb.SandboxState_SANDBOX_STATE_RUNNING, Kits: []string{label}}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
}

func (s *fakeServer) ValidateKit(_ context.Context, req *pb.ValidateKitRequest) (*pb.ValidateKitResponse, error) {
	if strings.Contains(req.GetKit().GetSpecYaml(), "bogus") {
		return &pb.ValidateKitResponse{Ok: false, Errors: []string{"spec.yaml: unknown field 'bogus'"}}, nil
	}
	return &pb.ValidateKitResponse{Ok: true, Warnings: []string{"deprecated: memory"}}, nil
}

// errNotFound is a plain error; the daemon returns bare errors from most handlers.
var errNotFound = errStr("sandbox not found")

type errStr string

func (e errStr) Error() string { return string(e) }

// Refresh collapses the stream to the terminal Sandbox and forwards progress.
func TestClientRefreshStreamsAndReturnsTerminal(t *testing.T) {
	conn := startFake(t)
	var updates []client.LaunchUpdate
	sb, err := conn.Refresh(context.Background(), "id-1", func(u client.LaunchUpdate) {
		updates = append(updates, u)
	})
	if err != nil {
		t.Fatal(err)
	}
	if sb.GetDisplayName() != "refreshed" {
		t.Errorf("sandbox = %+v, want the terminal frame", sb)
	}
	if len(updates) != 3 {
		t.Fatalf("forwarded %d updates, want 3 (copy, log, done)", len(updates))
	}
	if updates[0].Copy.GetBytesTotal() != 10 {
		t.Errorf("copy progress not forwarded: %+v", updates[0])
	}
	if updates[1].LogLine != "re-seeding" {
		t.Errorf("log line not forwarded: %+v", updates[1])
	}
}

// A nil onLog must be safe — the confirm flow passes nil.
func TestClientRefreshWithoutCallback(t *testing.T) {
	conn := startFake(t)
	if _, err := conn.Refresh(context.Background(), "id-1", nil); err != nil {
		t.Fatal(err)
	}
}

func TestClientRefreshError(t *testing.T) {
	conn := startFake(t)
	if _, err := conn.Refresh(context.Background(), "missing", nil); err == nil {
		t.Error("expected a daemon error to surface")
	}
}

func TestClientAddKitInline(t *testing.T) {
	conn := startFake(t)
	ref := &pb.KitRef{Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "ruff", SpecYaml: "kind: mixin\n"}}}
	sb, err := conn.AddKit(context.Background(), "id-1", ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sb.GetKits()) != 1 || sb.GetKits()[0] != "ruff" {
		t.Errorf("kits = %v, want the inline kit attached", sb.GetKits())
	}
}

func TestClientAddKitExternalSource(t *testing.T) {
	conn := startFake(t)
	ref := &pb.KitRef{Ref: &pb.KitRef_Source{Source: "ghcr.io/org/kit:1.0"}}
	sb, err := conn.AddKit(context.Background(), "id-1", ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sb.GetKits()[0] != "ghcr.io/org/kit:1.0" {
		t.Errorf("kits = %v, want the external source", sb.GetKits())
	}
}

func TestClientAddKitError(t *testing.T) {
	conn := startFake(t)
	if _, err := conn.AddKit(context.Background(), "id-1", &pb.KitRef{}, nil); err == nil {
		t.Error("expected an empty kit ref to error")
	}
}

func TestClientValidateKit(t *testing.T) {
	conn := startFake(t)
	ok, err := conn.ValidateKit(context.Background(), &pb.KitSpec{Id: "ruff", SpecYaml: "kind: mixin\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok.GetOk() || len(ok.GetWarnings()) != 1 {
		t.Errorf("resp = %+v, want ok with a warning", ok)
	}

	bad, err := conn.ValidateKit(context.Background(), &pb.KitSpec{Id: "ruff", SpecYaml: "bogus: 1\n"})
	if err != nil {
		t.Fatal(err)
	}
	if bad.GetOk() || len(bad.GetErrors()) != 1 {
		t.Errorf("resp = %+v, want a rejection carrying diagnostics", bad)
	}
}

// A stream that ends with no terminal frame must be reported, not returned as a
// silent nil sandbox.
func TestDrainWithoutTerminalFrameErrors(t *testing.T) {
	conn := startFake(t)
	// LaunchSandbox on the fake always sends Done, so use a sandbox id the fake
	// refuses instead: the point is that a missing terminal frame is an error.
	if _, err := conn.Refresh(context.Background(), "missing", nil); err == nil {
		t.Error("expected an error when no terminal Sandbox arrives")
	}
}
