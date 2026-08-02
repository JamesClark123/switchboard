package sandbox

import (
	"context"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Escape-hatch commands passed at launch are recorded on the sandbox and survive a
// round-trip through the bbolt registry (feature 005, FR-036).
func TestLaunchRecordsEscapeHatchCommands(t *testing.T) {
	m, reg, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	cmds := []*pb.EscapeHatchCommand{
		{Name: "install-deps", Command: "pnpm install", WhenToUse: "deps", ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN},
	}
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:              &pb.ConfigSnapshot{Name: "proj"},
		Sources:             []*pb.SourceRef{src},
		EscapeHatchCommands: cmds,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sb.GetEscapeHatchCommands()) != 1 || sb.GetEscapeHatchCommands()[0].GetName() != "install-deps" {
		t.Fatalf("launch did not record escape-hatch commands: %v", sb.GetEscapeHatchCommands())
	}

	// Reload from the registry: the field must round-trip through proto marshaling.
	reloaded, err := reg.Get(sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.GetEscapeHatchCommands()) != 1 || reloaded.GetEscapeHatchCommands()[0].GetCommand() != "pnpm install" {
		t.Errorf("escape-hatch commands did not survive registry round-trip: %v", reloaded.GetEscapeHatchCommands())
	}
}

// A sandbox launched with no escape-hatch commands decodes with an empty set (no
// migration; proto unknown-field tolerance).
func TestLaunchWithoutEscapeHatchIsEmpty(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "proj"},
		Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sb.GetEscapeHatchCommands()) != 0 {
		t.Errorf("want empty escape-hatch set, got %v", sb.GetEscapeHatchCommands())
	}
}

// AddKit merges a newly attached kit's commands with later-kit-wins on collision.
func TestAddKitMergesEscapeHatchLaterWins(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	sb, err := m.Launch(ctx, LaunchRequest{
		Config:              &pb.ConfigSnapshot{Name: "proj"},
		Sources:             []*pb.SourceRef{makeSource(t, dir, "proj")},
		EscapeHatchCommands: []*pb.EscapeHatchCommand{{Name: "install-deps", Command: "pnpm install", ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN}},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	newCmds := []*pb.EscapeHatchCommand{
		{Name: "install-deps", Command: "npm ci", ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN}, // collision -> later wins
		{Name: "e2e", Command: "pnpm test:e2e", ConsentMode: pb.ConsentMode_CONSENT_MODE_REQUIRES_APPROVAL},
	}
	out, err := m.AddKit(ctx, sb.GetId(), "/kits/extra", newCmds, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := out.GetEscapeHatchCommands()
	if len(got) != 2 {
		t.Fatalf("want 2 merged commands, got %d: %v", len(got), got)
	}
	if got[0].GetName() != "install-deps" || got[0].GetCommand() != "npm ci" {
		t.Errorf("later kit should override install-deps: %v", got[0])
	}
	if got[1].GetName() != "e2e" {
		t.Errorf("new command should be appended: %v", got[1])
	}
}
