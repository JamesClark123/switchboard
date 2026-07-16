package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
)

// launchOne is the common arrange step: a sandbox seeded from one source dir.
func launchOne(t *testing.T, m *Manager, dir, name string) (*pb.Sandbox, *pb.SourceRef) {
	t.Helper()
	src := makeSource(t, dir, name)
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: name},
		Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sb, src
}

// Refresh must yield a *fresh* tree, not a union of old and new: files the agent
// created in the workspace are gone, and files deleted from the source stay gone.
func TestRefreshRebuildsWorkspaceFromSources(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	sb, src := launchOne(t, m, dir, "proj")
	ws := sb.GetWorkspacePath()

	// The agent scribbles in the workspace, and the source gains a new file.
	agentFile := filepath.Join(ws, "proj", "agent-scratch.txt")
	if err := os.WriteFile(agentFile, []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.GetPath(), "added.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := m.Refresh(ctx, sb.GetId(), nil, nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING", out.GetState())
	}
	if _, err := os.Stat(agentFile); !os.IsNotExist(err) {
		t.Error("agent's workspace file survived the refresh; the copy was not rebuilt")
	}
	if _, err := os.Stat(filepath.Join(ws, "proj", "added.txt")); err != nil {
		t.Errorf("source file added since launch missing after refresh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "proj", "file.txt")); err != nil {
		t.Errorf("original source file missing after refresh: %v", err)
	}
}

// A source containing a symlink is the regression that makes copy-over impossible:
// duplicate.copyTree calls os.Symlink, which fails EEXIST against a populated
// destination. Refresh must wipe first, so a second pass succeeds.
func TestRefreshHandlesSymlinkedSources(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	sb, src := launchOne(t, m, dir, "proj")

	if err := os.Symlink("file.txt", filepath.Join(src.GetPath(), "link.txt")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	// First refresh copies the symlink in; the second would hit EEXIST if the
	// workspace were not deleted between runs.
	for i := range 2 {
		if _, err := m.Refresh(ctx, sb.GetId(), nil, nil); err != nil {
			t.Fatalf("Refresh #%d with a symlinked source: %v", i+1, err)
		}
	}
	link := filepath.Join(sb.GetWorkspacePath(), "proj", "link.txt")
	if fi, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink missing after refresh: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was copied as a regular file")
	}
}

// The terminal session lives in the workspace being deleted, so refresh must emit a
// non-RUNNING state (which drives the server's PTY teardown hook) before it wipes,
// and land on RUNNING at the end.
func TestRefreshEmitsNonRunningBeforeRunning(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "proj")

	var states []pb.SandboxState
	m.SetOnChange(func(s *pb.Sandbox) { states = append(states, s.GetState()) })

	if _, err := m.Refresh(ctx, sb.GetId(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(states) < 2 {
		t.Fatalf("expected at least 2 emits, got %v", states)
	}
	if states[0] == pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Error("first refresh emit must be non-RUNNING so the terminal session is torn down before the wipe")
	}
	if got := states[len(states)-1]; got != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("final emit = %v, want RUNNING", got)
	}
}

// Refresh must never delete outside the controlled folder, however the record got
// into that state.
func TestRefreshRefusesWorkspaceOutsideRoot(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "proj")

	outside := filepath.Join(dir, "not-the-workspace-root")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error {
		s.WorkspacePath = outside
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Refresh(ctx, sb.GetId(), nil, nil); err == nil {
		t.Fatal("expected Refresh to refuse a workspace outside the controlled folder")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("Refresh deleted a directory outside the controlled folder: %v", err)
	}
}

// A sandbox with no recorded sources has nothing to re-seed from; refusing beats
// wiping the workspace and leaving it empty.
func TestRefreshRequiresSources(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "proj")
	if _, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error {
		s.Sources = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Refresh(ctx, sb.GetId(), nil, nil); err == nil {
		t.Fatal("expected Refresh to refuse a sandbox with no recorded sources")
	}
	if _, err := os.Stat(filepath.Join(sb.GetWorkspacePath(), "proj", "file.txt")); err != nil {
		t.Errorf("workspace was wiped despite the refusal: %v", err)
	}
}

// A seed failure must leave the record in ERROR rather than silently RUNNING over a
// half-built workspace.
func TestRefreshSeedFailureMarksError(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	sb, src := launchOne(t, m, dir, "proj")

	// Remove the source so the re-seed cannot succeed.
	if err := os.RemoveAll(src.GetPath()); err != nil {
		t.Fatal(err)
	}
	out, err := m.Refresh(ctx, sb.GetId(), nil, nil)
	if err == nil {
		t.Fatal("expected Refresh to fail when a source has vanished")
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_ERROR {
		t.Errorf("state = %v, want ERROR", out.GetState())
	}
	if out.GetError() == "" {
		t.Error("expected the failure cause to be recorded on the sandbox")
	}
}

// Refresh reports copy progress, which is what the client renders during the
// potentially multi-GB re-seed.
func TestRefreshReportsProgress(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "proj")

	progressed := false
	if _, err := m.Refresh(ctx, sb.GetId(), func(duplicate.Progress) { progressed = true }, nil); err != nil {
		t.Fatal(err)
	}
	if !progressed {
		t.Error("expected Refresh to report copy progress")
	}
}
