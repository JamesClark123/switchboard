package sandbox

import (
	"context"
	"slices"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Kits passed at launch must reach the runner as `--kit` sources and be recorded on
// the sandbox.
func TestLaunchPassesAndRecordsKits(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:     &pb.ConfigSnapshot{Name: "proj"},
		Sources:    []*pb.SourceRef{src},
		KitSources: []string{"/kits/ruff", "ghcr.io/org/kit:1.0"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/kits/ruff", "ghcr.io/org/kit:1.0"}
	if !slices.Equal(runner.lastKits, want) {
		t.Errorf("runner got kits %v, want %v", runner.lastKits, want)
	}
	if !slices.Equal(sb.GetKits(), want) {
		t.Errorf("sandbox recorded kits %v, want %v", sb.GetKits(), want)
	}
}

// AddKit shells `sbx kit add`, records the source, and leaves the sandbox RUNNING.
func TestAddKitRecordsSourceAndRestores(t *testing.T) {
	ctx := context.Background()
	m, _, runner, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "proj")

	out, err := m.AddKit(ctx, sb.GetId(), "/kits/ruff", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.kitAdds) != 1 {
		t.Fatalf("expected one `sbx kit add` call, got %v", runner.kitAdds)
	}
	if !slices.Contains(out.GetKits(), "/kits/ruff") {
		t.Errorf("kit not recorded on the sandbox: %v", out.GetKits())
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING", out.GetState())
	}
}

// sbx restarts the sandbox inside `kit add`, killing the PTY the daemon holds.
// Nothing else would report that, so AddKit must emit a non-RUNNING state to drive
// the server's session-teardown hook — otherwise the next attach gets a dead PTY.
func TestAddKitEmitsNonRunningSoTerminalIsTornDown(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "proj")

	var states []pb.SandboxState
	m.SetOnChange(func(s *pb.Sandbox) { states = append(states, s.GetState()) })

	if _, err := m.AddKit(ctx, sb.GetId(), "/kits/ruff", nil); err != nil {
		t.Fatal(err)
	}
	if len(states) < 2 || states[0] == pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("first emit must be non-RUNNING to tear down the stale PTY, got %v", states)
	}
	if got := states[len(states)-1]; got != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("final emit = %v, want RUNNING", got)
	}
}

// Adding the same kit twice must not duplicate it — the sources are replayed as
// `--kit` flags on a container recreate.
func TestAddKitIsIdempotentPerSource(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "proj")

	for range 2 {
		if _, err := m.AddKit(ctx, sb.GetId(), "/kits/ruff", nil); err != nil {
			t.Fatal(err)
		}
	}
	out, err := m.Get(sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(out.GetKits()); got != 1 {
		t.Errorf("kits recorded %d times, want 1: %v", got, out.GetKits())
	}
}

func TestAddKitRequiresSource(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "proj")
	if _, err := m.AddKit(context.Background(), sb.GetId(), "  ", nil); err == nil {
		t.Error("expected AddKit to reject an empty kit source")
	}
}

// `--kit` is honoured only at creation, so a bring-up that has to recreate the
// container must replay the recorded kits or it silently returns a
// differently-provisioned sandbox.
func TestBringUpReplaysKitsOnRelaunch(t *testing.T) {
	ctx := context.Background()
	m, _, runner, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{
		Config:     &pb.ConfigSnapshot{Name: "proj"},
		Sources:    []*pb.SourceRef{src},
		KitSources: []string{"/kits/ruff"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.lastKits = nil
	// Force the relaunch branch: `sbx start` fails, so bringUp recreates.
	runner.mu.Lock()
	runner.failStart = true
	runner.mu.Unlock()

	if _, err := m.Restart(ctx, sb.GetId(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runner.lastKits, []string{"/kits/ruff"}) {
		t.Errorf("relaunch dropped the sandbox's kits: got %v, want [/kits/ruff]", runner.lastKits)
	}
}
