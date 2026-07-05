package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func timeUnixSec(s int64) time.Time { return time.Unix(s, 0) }

func TestLaunchCloneMode(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "c", SeedingMode: pb.SeedingMode_SEEDING_MODE_CLONE},
		Sources: []*pb.SourceRef{{Path: repo, IsRepo: true}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("clone launch: %v", err)
	}
	// fakeRunner.CloneRepo created the dest dir under the workspace.
	if _, err := os.Stat(filepath.Join(sb.GetWorkspacePath(), "repo")); err != nil {
		t.Errorf("clone dest missing: %v", err)
	}
}

func TestCloneRequiresRepo(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	plain := filepath.Join(dir, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "c", SeedingMode: pb.SeedingMode_SEEDING_MODE_CLONE},
		Sources: []*pb.SourceRef{{Path: plain, IsRepo: false}},
	}, nil, nil)
	if err == nil {
		t.Fatal("expected clone-of-non-repo error")
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_ERROR {
		t.Errorf("state = %v, want error", sb.GetState())
	}
}

func TestLaunchUsesAgentOverrideAndReadoptNoOp(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:        &pb.ConfigSnapshot{Name: "x"}, // no agent in config
		Sources:       []*pb.SourceRef{src},
		AgentOverride: &pb.AgentSpec{Kind: "claude-code"}, // FR-016b
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sb.GetAgent().GetSpec().GetKind() != "claude-code" {
		t.Errorf("agent override not applied: %+v", sb.GetAgent().GetSpec())
	}
	// Re-adopting when state already matches is a no-op (covers the continue path).
	if err := m.Readopt(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHookInjectorRunsOnLaunch(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	var injectedID, injectedWS string
	m.SetHookInjector(func(id, ws string) error {
		injectedID, injectedWS = id, ws
		return nil
	})
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "x"}, Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if injectedID != sb.GetId() || injectedWS != sb.GetWorkspacePath() {
		t.Errorf("hook injector got (%q,%q), want (%q,%q)", injectedID, injectedWS, sb.GetId(), sb.GetWorkspacePath())
	}
}

func TestSetAgentStatus(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "x"}, Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.SetAgentStatus(sb.GetId(), pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, timeUnixSec(5))
	if err != nil {
		t.Fatal(err)
	}
	if out.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT {
		t.Errorf("status = %v", out.GetAgent().GetStatus())
	}
	if _, err := m.SetAgentStatus("missing", pb.AgentStatus_AGENT_STATUS_IDLE, timeUnixSec(1)); err == nil {
		t.Error("expected error setting status on unknown sandbox")
	}
}

func TestSetOnChangeEmits(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	var events int
	m.SetOnChange(func(*pb.Sandbox) { events++ })
	src := makeSource(t, dir, "proj")
	if _, err := m.Launch(context.Background(), LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "x"}, Sources: []*pb.SourceRef{src},
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if events == 0 {
		t.Error("expected onChange to fire during launch")
	}
}

// erroringRunner fails Stop/Destroy to exercise the manager error branches.
type erroringRunner struct{ fakeRunner }

func (e *erroringRunner) Stop(context.Context, string) error    { return errors.New("stop boom") }
func (e *erroringRunner) Destroy(context.Context, string) error { return errors.New("destroy boom") }

func TestStopErrorAndDestroyIsBestEffort(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	m.runner = &erroringRunner{fakeRunner: *newFakeRunner()}
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "x"}, Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Stop surfaces a real runner error.
	if _, err := m.Stop(ctx, sb.GetId()); err == nil {
		t.Error("expected Stop to surface runner error")
	}
	// Destroy is best-effort: even when `sbx rm` fails, the retained copy and the
	// record are removed so the entry can always be cleared.
	deleted, err := m.Destroy(ctx, sb.GetId())
	if err != nil {
		t.Errorf("Destroy should be best-effort, got error %v", err)
	}
	if !deleted {
		t.Error("Destroy should remove the retained workspace")
	}
	if list, _ := m.List(); len(list) != 0 {
		t.Errorf("Destroy should remove the record, %d remain", len(list))
	}
}

func TestRestartRelaunchesWhenNoContainerRef(t *testing.T) {
	m, reg, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "x"}, Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a sandbox whose container handle is gone (e.g. host reboot).
	if _, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error {
		s.ContainerRef = ""
		s.State = pb.SandboxState_SANDBOX_STATE_STOPPED
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := m.Restart(context.Background(), sb.GetId(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || out.GetContainerRef() == "" {
		t.Errorf("relaunch should set a new container ref and run; got ref=%q state=%v", out.GetContainerRef(), out.GetState())
	}
}

func TestStopAndRestartMissingID(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	if _, err := m.Stop(context.Background(), "nope"); err == nil {
		t.Error("expected error stopping unknown id")
	}
	if _, err := m.Restart(context.Background(), "nope", nil); err == nil {
		t.Error("expected error restarting unknown id")
	}
	if _, err := m.Destroy(context.Background(), "nope"); err == nil {
		t.Error("expected error destroying unknown id")
	}
}

func TestDestroyDoesNotDeleteOutsideWorkspace(t *testing.T) {
	m, reg, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "x"}, Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Point workspace_path outside the controlled root; destroy must not delete it.
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error {
		s.WorkspacePath = outside
		s.ContainerRef = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deleted, err := m.Destroy(context.Background(), sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Error("must not report deletion of a path outside the workspace root")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside path must be preserved: %v", err)
	}
}
