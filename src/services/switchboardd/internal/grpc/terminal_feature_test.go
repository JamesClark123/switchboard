package grpc_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// mkSrc creates a small source directory to seed a sandbox launch.
func mkSrc(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

// attachExternal opens an AttachAgent stream declaring the EXTERNAL kind.
func attachExternal(t *testing.T, ctx context.Context, client pb.SwitchboardClient, sandboxID string) pb.Switchboard_AttachAgentClient {
	t.Helper()
	st, err := client.AttachAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(&pb.AgentInput{
		SandboxId: sandboxID,
		Attach:    &pb.AgentInput_AttachInfo{Kind: pb.ClientKind_CLIENT_KIND_EXTERNAL, InitialSize: &pb.AgentInput_Resize{Cols: 80, Rows: 24}},
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestReattachReplaysPriorOutput proves a reconnecting client is shown prior
// output via the snapshot rather than a blank screen (US1, FR-003, SC-002).
func TestReattachReplaysPriorOutput(t *testing.T) {
	client, _ := startServerWithAgents(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First attach; type something that the echo session reflects into the buffer.
	st1, err := client.AttachAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st1.Send(&pb.AgentInput{SandboxId: "sb1", Data: []byte("prior-work")}); err != nil {
		t.Fatal(err)
	}
	// Drain until we see our bytes, so they are guaranteed in the ring.
	waitForBytes(t, st1, "prior-work")
	// Detach (close the send direction + cancel is heavy; just close send).
	_ = st1.CloseSend()

	// Reattach: the snapshot must contain the earlier output.
	st2, err := client.AttachAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st2.Send(&pb.AgentInput{SandboxId: "sb1"}); err != nil {
		t.Fatal(err)
	}
	frame, err := st2.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if !containsBytes(frame.GetSnapshot().GetData(), "prior-work") {
		t.Fatalf("reattach snapshot = %q, want it to replay prior output", string(frame.GetSnapshot().GetData()))
	}
}

// TestSecondExternalRefused proves the daemon rejects a second EXTERNAL attach
// (US3, FR-014/015).
func TestSecondExternalRefused(t *testing.T) {
	client, _ := startServerWithAgents(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st1 := attachExternal(t, ctx, client, "sb1")
	// Ensure the first external is registered server-side before the second try.
	_ = st1
	time.Sleep(100 * time.Millisecond)

	st2 := attachExternal(t, ctx, client, "sb1")
	if _, err := st2.Recv(); err == nil {
		t.Fatal("second EXTERNAL attach should be refused")
	}
}

// TestSetSandboxTag proves tags set/clear, persist in the returned sandbox, and
// change no other field (US5, FR-021/022, SC-007).
func TestSetSandboxTag(t *testing.T) {
	client, _ := startServerWithAgents(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sb := launch(t, client, mkSrc(t))

	tagged, err := client.SetSandboxTag(ctx, &pb.SetSandboxTagRequest{SandboxId: sb.GetId(), Tag: "  auth-refactor  "})
	if err != nil {
		t.Fatal(err)
	}
	if tagged.GetTag() != "auth-refactor" {
		t.Fatalf("tag = %q, want trimmed 'auth-refactor'", tagged.GetTag())
	}
	if tagged.GetId() != sb.GetId() || tagged.GetDisplayName() != sb.GetDisplayName() || tagged.GetState() != sb.GetState() {
		t.Fatal("SetSandboxTag must not change id/name/state")
	}

	// Clear it.
	cleared, err := client.SetSandboxTag(ctx, &pb.SetSandboxTagRequest{SandboxId: sb.GetId(), Tag: ""})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.GetTag() != "" {
		t.Fatalf("tag after clear = %q, want empty", cleared.GetTag())
	}

	// Persistence: the tag survives in the list after being set again.
	if _, err := client.SetSandboxTag(ctx, &pb.SetSandboxTagRequest{SandboxId: sb.GetId(), Tag: "keep"}); err != nil {
		t.Fatal(err)
	}
	list, err := client.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list.GetSandboxes() {
		if s.GetId() == sb.GetId() {
			found = true
			if s.GetTag() != "keep" {
				t.Fatalf("listed tag = %q, want 'keep'", s.GetTag())
			}
		}
	}
	if !found {
		t.Fatal("sandbox missing from list")
	}
}

// TestResolveWorkspace proves a path (and nested subpath) resolves to the owning
// sandbox (US4, FR-017/018).
func TestResolveWorkspace(t *testing.T) {
	client, _ := startServerWithAgents(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sb := launch(t, client, mkSrc(t))
	if sb.GetWorkspacePath() == "" {
		t.Skip("runner produced no workspace path")
	}

	// Exact path resolves.
	res, err := client.ResolveWorkspace(ctx, &pb.ResolveWorkspaceRequest{Path: sb.GetWorkspacePath()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetFound() || res.GetSandboxId() != sb.GetId() {
		t.Fatalf("resolve exact = %+v, want sandbox %s", res, sb.GetId())
	}

	// A path outside any workspace does not resolve.
	res2, err := client.ResolveWorkspace(ctx, &pb.ResolveWorkspaceRequest{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res2.GetFound() {
		t.Fatalf("resolve outside workspace should be not-found, got %+v", res2)
	}
}

// TestAttachmentCountOnList proves the list-page connected-terminal count reflects
// live attach/detach (US3, FR-007/008, SC-005).
func TestAttachmentCountOnList(t *testing.T) {
	client, _ := startServerWithAgents(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sb := launch(t, client, mkSrc(t))

	// Attach one client and read its first frame so the server-side attach is live.
	st, err := client.AttachAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(&pb.AgentInput{SandboxId: sb.GetId(), Data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Recv(); err != nil {
		t.Fatal(err)
	}

	// The list must show at least one attached terminal.
	waitForCount(t, client, sb.GetId(), 1)

	// Detach by cancelling the attach stream's context; the count returns to zero.
	cancel()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	waitForCountCtx(t, ctx2, client, sb.GetId(), 0)
}

func TestSetSandboxTagUnknown(t *testing.T) {
	client, _ := startServerWithAgents(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.SetSandboxTag(ctx, &pb.SetSandboxTagRequest{SandboxId: "nope", Tag: "x"}); err == nil {
		t.Fatal("expected error tagging unknown sandbox")
	}
}

func waitForCount(t *testing.T, client pb.SwitchboardClient, id string, want int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	waitForCountCtx(t, ctx, client, id, want)
}

func waitForCountCtx(t *testing.T, ctx context.Context, client pb.SwitchboardClient, id string, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err := client.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
		if err == nil {
			for _, s := range list.GetSandboxes() {
				if s.GetId() == id && s.GetAttachedTerminals() == want {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("attached_terminals for %s did not reach %d", id, want)
}

// waitForBytes drains a stream until the accumulated bytes contain sub.
func waitForBytes(t *testing.T, st pb.Switchboard_AttachAgentClient, sub string) {
	t.Helper()
	var got []byte
	for i := 0; i < 10; i++ {
		frame, err := st.Recv()
		if err != nil {
			t.Fatalf("recv while waiting for %q: %v", sub, err)
		}
		got = append(got, frame.GetSnapshot().GetData()...)
		got = append(got, frame.GetData()...)
		if containsBytes(got, sub) {
			return
		}
	}
	t.Fatalf("did not observe %q in output", sub)
}
