package client_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The feature-007 RPCs on fakeServer.

func (s *fakeServer) AddSandboxSources(req *pb.AddSandboxSourcesRequest, stream pb.Switchboard_AddSandboxSourcesServer) error {
	sb, ok := s.sandboxes[req.GetSandboxId()]
	if !ok {
		return errNotFound
	}
	if !req.GetOverrideResourceWarning() {
		for _, src := range req.GetSources() {
			if strings.Contains(src.GetPath(), "huge") {
				return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Blocked{Blocked: &pb.ResourceReport{Warnings: []string{"low disk"}}}})
			}
		}
	}
	if err := stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Copy{Copy: &pb.LaunchProgress_CopyProgress{BytesCopied: 5, BytesTotal: 10, CurrentPath: "lib/x"}}}); err != nil {
		return err
	}
	if err := stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_LogLine{LogLine: "staging lib"}}); err != nil {
		return err
	}
	sb.Sources = append(sb.Sources, req.GetSources()...)
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
}

func (s *fakeServer) RemoveSandboxSources(_ context.Context, req *pb.RemoveSandboxSourcesRequest) (*pb.Sandbox, error) {
	sb, ok := s.sandboxes[req.GetSandboxId()]
	if !ok {
		return nil, errNotFound
	}
	drop := map[string]bool{}
	for _, p := range req.GetSourcePaths() {
		drop[p] = true
	}
	kept := sb.Sources[:0]
	for _, src := range sb.Sources {
		if !drop[src.GetPath()] {
			kept = append(kept, src)
		}
	}
	sb.Sources = kept
	return sb, nil
}

// launchOne creates "id-1" on the fake so the edit RPCs have a target.
func launchOne(t *testing.T, conn *client.Conn) *pb.Sandbox {
	t.Helper()
	sb, _, err := conn.Launch(context.Background(), &pb.LaunchSandboxRequest{
		Config:  &pb.ConfigSnapshot{Name: "edit-me"},
		Sources: []*pb.SourceRef{{Path: "/work/proj"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func TestClientAddSourcesStreamsUpdatesThenDone(t *testing.T) {
	conn := startFake(t)
	sb := launchOne(t, conn)

	var copies, logs int
	out, blocked, err := conn.AddSources(context.Background(), sb.GetId(), []*pb.SourceRef{{Path: "/work/lib"}}, false, func(u client.LaunchUpdate) {
		if u.Copy != nil {
			copies++
		}
		if u.LogLine != "" {
			logs++
		}
	})
	if err != nil || blocked != nil {
		t.Fatalf("AddSources: err=%v blocked=%v", err, blocked)
	}
	if copies != 1 || logs != 1 {
		t.Errorf("updates: copies=%d logs=%d, want 1/1", copies, logs)
	}
	if len(out.GetSources()) != 2 || out.GetSources()[1].GetPath() != "/work/lib" {
		t.Errorf("sources = %+v, want proj + lib", out.GetSources())
	}
	// A nil callback is fine.
	if _, _, err := conn.AddSources(context.Background(), sb.GetId(), []*pb.SourceRef{{Path: "/work/docs"}}, false, nil); err != nil {
		t.Fatal(err)
	}
}

// The low-resource gate round-trips like Launch: blocked without override, done
// with it (FR-057).
func TestClientAddSourcesBlockedThenOverride(t *testing.T) {
	conn := startFake(t)
	sb := launchOne(t, conn)

	out, blocked, err := conn.AddSources(context.Background(), sb.GetId(), []*pb.SourceRef{{Path: "/work/huge"}}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil || blocked == nil || len(blocked.GetWarnings()) == 0 {
		t.Fatalf("expected a blocked report and no sandbox; got out=%v blocked=%v", out, blocked)
	}
	out, blocked, err = conn.AddSources(context.Background(), sb.GetId(), []*pb.SourceRef{{Path: "/work/huge"}}, true, nil)
	if err != nil || blocked != nil || out == nil {
		t.Fatalf("override should proceed: err=%v blocked=%v out=%v", err, blocked, out)
	}
}

func TestClientRemoveSources(t *testing.T) {
	conn := startFake(t)
	sb := launchOne(t, conn)
	if _, _, err := conn.AddSources(context.Background(), sb.GetId(), []*pb.SourceRef{{Path: "/work/lib"}}, false, nil); err != nil {
		t.Fatal(err)
	}
	out, err := conn.RemoveSources(context.Background(), sb.GetId(), []string{"/work/lib"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.GetSources()) != 1 || out.GetSources()[0].GetPath() != "/work/proj" {
		t.Errorf("sources = %+v, want {proj}", out.GetSources())
	}
}

func TestClientSourceEditsSurfaceErrors(t *testing.T) {
	conn := startFake(t)
	if _, _, err := conn.AddSources(context.Background(), "missing", []*pb.SourceRef{{Path: "/work/lib"}}, false, nil); err == nil {
		t.Error("expected an error adding to an unknown sandbox")
	}
	if _, err := conn.RemoveSources(context.Background(), "missing", []string{"/work/lib"}); err == nil {
		t.Error("expected an error removing from an unknown sandbox")
	}
}

// A transport that cannot open the stream reports the dial error rather than a
// missing terminal result.
func TestClientAddSourcesStreamOpenError(t *testing.T) {
	conn := startFake(t)
	_ = conn.Close()
	if _, _, err := conn.AddSources(context.Background(), "id-1", []*pb.SourceRef{{Path: "/work/lib"}}, false, nil); err == nil {
		t.Error("expected an error when the stream cannot be opened")
	}
}
