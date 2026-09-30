package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// makeRepo is makeSource plus a .git entry, so the directory passes the daemon's
// clone-mode re-verification.
func makeRepo(t *testing.T, dir, name string) *pb.SourceRef {
	t.Helper()
	src := makeSource(t, dir, name)
	if err := os.MkdirAll(filepath.Join(src.GetPath(), ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	src.IsRepo = true
	return src
}

// launchSeeded launches a sandbox seeded from the given sources in mode.
func launchSeeded(t *testing.T, m *Manager, name string, mode pb.SeedingMode, srcs ...*pb.SourceRef) *pb.Sandbox {
	t.Helper()
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: name, SeedingMode: mode},
		Sources: srcs,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

// treeDigest hashes every regular file (path + content) under root, so a
// before/after comparison proves an origin was never modified (SC-003).
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		_, _ = fmt.Fprintf(h, "%s|%v|", rel, info.Mode())
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if _, err := io.Copy(h, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func sourcePaths(sb *pb.Sandbox) []string {
	out := make([]string, 0, len(sb.GetSources()))
	for _, s := range sb.GetSources() {
		out = append(out, s.GetPath())
	}
	return out
}

func stagingOf(sb *pb.Sandbox) string {
	return filepath.Join(sb.GetWorkspacePath(), workspaceMarkerDir, stagingDirName)
}

// --- US1: add ---

// The folder appears at its seeded path only after the whole copy completes: while
// bytes are still flowing it lives under .switchboard/staging (research R2), and
// the final rename makes it appear complete (FR-058).
func TestAddSourcesStagesThenRenamesIntoPlace(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	srcA := makeSource(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA)
	ws := sb.GetWorkspacePath()
	srcB := makeSource(t, dir, "repo-b")
	before := treeDigest(t, srcB.GetPath())

	var emitted []*pb.Sandbox
	m.SetOnChange(func(s *pb.Sandbox) { emitted = append(emitted, s) })

	probes := 0
	onProgress := func(duplicate.Progress) {
		probes++
		if exists(filepath.Join(ws, "repo-b")) {
			t.Error("the seeded path must not exist while the copy is still in flight")
		}
		if !exists(stagingOf(sb)) {
			t.Error("mid-copy content must live under .switchboard/staging")
		}
	}
	out, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{srcB}, onProgress, nil)
	if err != nil {
		t.Fatalf("AddSources: %v", err)
	}
	if probes == 0 {
		t.Error("expected copy progress during the add")
	}
	if _, err := os.Stat(filepath.Join(ws, "repo-b", "file.txt")); err != nil {
		t.Errorf("added folder missing after the add: %v", err)
	}
	if exists(stagingOf(sb)) {
		t.Error("staging area must be removed once the add completes")
	}
	if got := sourcePaths(out); len(got) != 2 || got[1] != srcB.GetPath() {
		t.Errorf("sources = %v, want the original plus repo-b appended", got)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING — an add never drives sandbox state", out.GetState())
	}
	if len(emitted) != 1 || len(emitted[0].GetSources()) != 2 {
		t.Errorf("emits = %d, want exactly one changed Sandbox carrying the new set", len(emitted))
	}
	rec, _ := reg.Get(sb.GetId())
	if len(rec.GetSources()) != 2 {
		t.Errorf("record sources = %d, want 2", len(rec.GetSources()))
	}
	if treeDigest(t, srcB.GetPath()) != before {
		t.Error("the origin directory was modified by the add")
	}
}

// A mid-batch clone failure rolls everything back: nothing at any seeded path,
// no staging debris, record and state untouched (FR-058, SC-005).
func TestAddSourcesRollsBackOnCloneFailure(t *testing.T) {
	ctx := context.Background()
	m, reg, runner, dir := newTestManager(t)
	repoA := makeRepo(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_CLONE, repoA)
	ws := sb.GetWorkspacePath()
	repoB, repoC := makeRepo(t, dir, "repo-b"), makeRepo(t, dir, "repo-c")

	runner.failCloneOn = runner.cloneCalls + 2 // repo-b clones, repo-c fails
	_, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{repoB, repoC}, nil, nil)
	if err == nil {
		t.Fatal("expected the add to fail")
	}
	for _, f := range []string{"repo-b", "repo-c"} {
		if exists(filepath.Join(ws, f)) {
			t.Errorf("%s must not exist after a failed add", f)
		}
	}
	if exists(stagingOf(sb)) {
		t.Error("staging debris left behind after rollback")
	}
	rec, _ := reg.Get(sb.GetId())
	if got := sourcePaths(rec); len(got) != 1 || got[0] != repoA.GetPath() {
		t.Errorf("record sources = %v, want unchanged {repo-a}", got)
	}
	if rec.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || rec.GetError() != "" {
		t.Errorf("state = %v error = %q; a failed add must never push the sandbox into ERROR", rec.GetState(), rec.GetError())
	}
	if _, held := m.ops.holder(sb.GetId()); held {
		t.Error("latch still held after a failed add")
	}
}

// A rename losing a race with a directory entry that appeared mid-copy fails the
// add; folders this operation already renamed are removed again, the stray entry
// (not ours) survives, and the record is untouched.
func TestAddSourcesRollsBackAlreadyRenamedOnRenameFailure(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	srcA := makeSource(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA)
	ws := sb.GetWorkspacePath()
	srcB, srcC := makeSource(t, dir, "repo-b"), makeSource(t, dir, "repo-c")

	stray := filepath.Join(ws, "repo-c")
	planted := false
	onProgress := func(duplicate.Progress) {
		if !planted { // the agent creates a FILE named repo-c while the copy runs
			planted = true
			if err := os.WriteFile(stray, []byte("agent"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{srcB, srcC}, onProgress, nil)
	if err == nil {
		t.Fatal("expected the add to fail when repo-c cannot be moved into place")
	}
	if !strings.Contains(err.Error(), "repo-c") {
		t.Errorf("err = %v, want it to name the folder that failed", err)
	}
	if exists(filepath.Join(ws, "repo-b")) {
		t.Error("repo-b was renamed into place before the failure and must be rolled back")
	}
	if b, rerr := os.ReadFile(stray); rerr != nil || string(b) != "agent" {
		t.Error("the agent's own entry must survive the rollback untouched")
	}
	if exists(stagingOf(sb)) {
		t.Error("staging debris left behind after rollback")
	}
	rec, _ := reg.Get(sb.GetId())
	if got := sourcePaths(rec); len(got) != 1 {
		t.Errorf("record sources = %v, want unchanged", got)
	}
}

// Every research-R6 refusal fires before any byte lands (FR-056), naming the
// offending folder, and each is recognisable by its sentinel.
func TestAddSourcesRefusalsCopyNothing(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	srcA := makeSource(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA)
	ws := sb.GetWorkspacePath()

	// An agent-created top-level directory the record knows nothing about.
	if err := os.MkdirAll(filepath.Join(ws, "mylib"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "elsewhere")
	sameName := makeSource(t, other, "repo-a") // same basename as the recorded source
	agentName := makeSource(t, other, "mylib") // same basename as the on-disk dir
	reserved := makeSource(t, other, workspaceMarkerDir)
	dup1, dup2 := makeSource(t, filepath.Join(dir, "p1"), "twin"), makeSource(t, filepath.Join(dir, "p2"), "twin")
	file := filepath.Join(dir, "afile.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		srcs []*pb.SourceRef
		want error
		text string
	}{
		{"empty selection", nil, ErrInvalidSource, "no folders"},
		{"relative path", []*pb.SourceRef{{Path: "relative/dir"}}, ErrInvalidSource, "absolute"},
		{"missing path", []*pb.SourceRef{{Path: filepath.Join(dir, "nope")}}, ErrInvalidSource, "nope"},
		{"not a directory", []*pb.SourceRef{{Path: file}}, ErrInvalidSource, "not a directory"},
		{"collides with recorded", []*pb.SourceRef{sameName}, ErrSourceExists, `"repo-a"`},
		{"collides with on-disk entry", []*pb.SourceRef{agentName}, ErrSourceExists, `"mylib"`},
		{"reserved name", []*pb.SourceRef{reserved}, ErrInvalidSource, workspaceMarkerDir},
		{"selected twice in one batch", []*pb.SourceRef{dup1, dup2}, ErrInvalidSource, `"twin"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.AddSources(ctx, sb.GetId(), tc.srcs, nil, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.text) {
				t.Errorf("err = %q, want it to mention %q", err, tc.text)
			}
			entries, _ := os.ReadDir(ws)
			for _, e := range entries {
				if e.Name() != "repo-a" && e.Name() != "mylib" && e.Name() != workspaceMarkerDir {
					t.Errorf("refusal copied something: %s", e.Name())
				}
			}
			if exists(stagingOf(sb)) {
				t.Error("refusal must not create a staging area")
			}
			rec, _ := reg.Get(sb.GetId())
			if len(rec.GetSources()) != 1 {
				t.Error("refusal must leave the record untouched")
			}
		})
	}
}

// Clone-mode adds re-verify .git daemon-side (the client's flag is advisory),
// refuse non-repos, and drive the runner's clone into the staging area.
func TestAddSourcesCloneModeVerifiesRepoAndStages(t *testing.T) {
	ctx := context.Background()
	m, reg, runner, dir := newTestManager(t)
	repoA := makeRepo(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_CLONE, repoA)
	ws := sb.GetWorkspacePath()

	notRepo := makeSource(t, dir, "plain")
	notRepo.IsRepo = true // a lying client flag must not be trusted
	_, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{notRepo}, nil, nil)
	if !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), `"plain"`) {
		t.Fatalf("err = %v, want ErrNotRepo naming plain", err)
	}
	if exists(filepath.Join(ws, "plain")) {
		t.Error("a refused clone-mode add copied something")
	}

	repoB := makeRepo(t, dir, "repo-b")
	repoB.IsRepo = false // and a false negative is corrected by the daemon
	out, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{repoB}, nil, nil)
	if err != nil {
		t.Fatalf("AddSources: %v", err)
	}
	wantDest := filepath.Join(stagingOf(sb), "repo-b")
	found := false
	for _, d := range runner.cloneDests {
		if d == wantDest {
			found = true
		}
	}
	if !found {
		t.Errorf("clone destinations = %v, want %s (clones go into staging)", runner.cloneDests, wantDest)
	}
	if !exists(filepath.Join(ws, "repo-b")) {
		t.Error("cloned folder missing after the add")
	}
	if got := out.GetSources()[1]; !got.GetIsRepo() {
		t.Error("the recorded SourceRef must carry the daemon-verified is_repo")
	}
	rec, _ := reg.Get(sb.GetId())
	if len(rec.GetSources()) != 2 {
		t.Errorf("record sources = %d, want 2", len(rec.GetSources()))
	}
}

// A second seed-mutating operation on the same sandbox is refused via the latch
// (FR-066) and copies nothing.
func TestAddSourcesRefusedWhileLatched(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	srcA := makeSource(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA)
	srcB := makeSource(t, dir, "repo-b")

	if err := m.ops.acquire(sb.GetId(), opRefresh); err != nil {
		t.Fatal(err)
	}
	defer m.ops.release(sb.GetId())
	_, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{srcB}, nil, nil)
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "refresh in progress") {
		t.Fatalf("err = %v, want ErrBusy naming the refresh", err)
	}
	if exists(filepath.Join(sb.GetWorkspacePath(), "repo-b")) || exists(stagingOf(sb)) {
		t.Error("a refused add must copy nothing")
	}
	_, err = m.RemoveSources(ctx, sb.GetId(), []string{srcA.GetPath()})
	if !errors.Is(err, ErrBusy) {
		t.Errorf("remove err = %v, want ErrBusy", err)
	}
}

func TestAddSourcesUnknownSandbox(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "repo-a")
	if _, err := m.AddSources(context.Background(), "nope", []*pb.SourceRef{src}, nil, nil); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("err = %v, want registry.ErrNotFound", err)
	}
	if _, _, err := m.ValidateAddSources("nope", []*pb.SourceRef{src}); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("validate err = %v, want registry.ErrNotFound", err)
	}
	if _, err := m.RemoveSources(context.Background(), "nope", []string{src.GetPath()}); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("remove err = %v, want registry.ErrNotFound", err)
	}
}

// ValidateAddSources is the gRPC layer's pre-gate check: it returns the sandbox
// and the normalized refs and never copies.
func TestValidateAddSourcesReturnsRefsWithoutCopying(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	srcA := makeSource(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA)
	repoB := makeRepo(t, dir, "repo-b")
	repoB.IsRepo = false

	got, refs, err := m.ValidateAddSources(sb.GetId(), []*pb.SourceRef{{Path: "  " + repoB.GetPath() + " "}})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetId() != sb.GetId() {
		t.Error("expected the sandbox record back")
	}
	if len(refs) != 1 || refs[0].GetPath() != repoB.GetPath() || !refs[0].GetIsRepo() {
		t.Errorf("refs = %+v, want the trimmed path with daemon-verified is_repo", refs)
	}
	if exists(filepath.Join(sb.GetWorkspacePath(), "repo-b")) || exists(stagingOf(sb)) {
		t.Error("validation must not copy")
	}
}

// --- US2: remove ---

func TestRemoveSourcesDeletesCopyAndDropsRef(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	srcA, srcB := makeSource(t, dir, "repo-a"), makeSource(t, dir, "repo-b")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA, srcB)
	ws := sb.GetWorkspacePath()
	before := treeDigest(t, srcB.GetPath())

	var emits int
	m.SetOnChange(func(*pb.Sandbox) { emits++ })

	out, err := m.RemoveSources(ctx, sb.GetId(), []string{srcB.GetPath()})
	if err != nil {
		t.Fatalf("RemoveSources: %v", err)
	}
	if exists(filepath.Join(ws, "repo-b")) {
		t.Error("the copy must be deleted")
	}
	if !exists(filepath.Join(ws, "repo-a", "file.txt")) {
		t.Error("the surviving folder must be intact")
	}
	if got := sourcePaths(out); len(got) != 1 || got[0] != srcA.GetPath() {
		t.Errorf("sources = %v, want {repo-a}", got)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING — a removal never drives sandbox state", out.GetState())
	}
	if emits != 1 {
		t.Errorf("emits = %d, want 1", emits)
	}
	rec, _ := reg.Get(sb.GetId())
	if len(rec.GetSources()) != 1 {
		t.Errorf("record sources = %d, want 1", len(rec.GetSources()))
	}
	if treeDigest(t, srcB.GetPath()) != before {
		t.Error("the origin directory was touched by the removal")
	}
}

// A copy the agent already deleted is treated as gone: the record is cleaned and
// the operation succeeds (FR-063).
func TestRemoveSourcesAlreadyMissingCopySucceeds(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	srcA, srcB := makeSource(t, dir, "repo-a"), makeSource(t, dir, "repo-b")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA, srcB)
	if err := os.RemoveAll(filepath.Join(sb.GetWorkspacePath(), "repo-b")); err != nil {
		t.Fatal(err)
	}
	out, err := m.RemoveSources(ctx, sb.GetId(), []string{srcB.GetPath()})
	if err != nil {
		t.Fatalf("RemoveSources: %v", err)
	}
	if got := sourcePaths(out); len(got) != 1 || got[0] != srcA.GetPath() {
		t.Errorf("sources = %v, want {repo-a}", got)
	}
}

// An unknown target refuses the whole batch before anything is deleted; requests
// that name the same folder twice are deduplicated rather than refused.
func TestRemoveSourcesUnknownPathDeletesNothing(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	srcA, srcB := makeSource(t, dir, "repo-a"), makeSource(t, dir, "repo-b")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA, srcB)
	ws := sb.GetWorkspacePath()

	_, err := m.RemoveSources(ctx, sb.GetId(), []string{srcB.GetPath(), filepath.Join(dir, "nope")})
	if !errors.Is(err, ErrSourceNotRecorded) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want ErrSourceNotRecorded naming the path", err)
	}
	if !exists(filepath.Join(ws, "repo-b")) {
		t.Error("nothing may be deleted when any target is unknown")
	}
	rec, _ := reg.Get(sb.GetId())
	if len(rec.GetSources()) != 2 {
		t.Error("record must be untouched")
	}
	if _, err := m.RemoveSources(ctx, sb.GetId(), nil); !errors.Is(err, ErrInvalidSource) {
		t.Errorf("empty request err = %v, want ErrInvalidSource", err)
	}
	out, err := m.RemoveSources(ctx, sb.GetId(), []string{srcB.GetPath(), srcB.GetPath()})
	if err != nil {
		t.Fatalf("duplicate targets should be deduplicated: %v", err)
	}
	if len(out.GetSources()) != 1 {
		t.Errorf("sources = %v, want {repo-a}", sourcePaths(out))
	}
}

// A sandbox always retains at least one seeded folder (FR-061); the refusal fires
// before any deletion.
func TestRemoveSourcesLastSourceRefused(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	srcA, srcB := makeSource(t, dir, "repo-a"), makeSource(t, dir, "repo-b")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA, srcB)
	ws := sb.GetWorkspacePath()

	_, err := m.RemoveSources(ctx, sb.GetId(), []string{srcA.GetPath(), srcB.GetPath()})
	if !errors.Is(err, ErrLastSource) {
		t.Fatalf("err = %v, want ErrLastSource", err)
	}
	if !exists(filepath.Join(ws, "repo-a")) || !exists(filepath.Join(ws, "repo-b")) {
		t.Error("the refusal must delete nothing")
	}
	if _, err := m.RemoveSources(ctx, sb.GetId(), []string{srcB.GetPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RemoveSources(ctx, sb.GetId(), []string{srcA.GetPath()}); !errors.Is(err, ErrLastSource) {
		t.Errorf("removing the sole remaining folder: err = %v, want ErrLastSource", err)
	}
	if !exists(filepath.Join(ws, "repo-a", "file.txt")) {
		t.Error("the last folder must survive")
	}
}

// On a mid-batch deletion failure the record follows the disk (research R3): the
// folders already deleted stay dropped, the failed folder stays recorded (and the
// error names it), and folders after it are untouched.
func TestRemoveSourcesMidBatchFailureRecordFollowsDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based deletion failure cannot be simulated as root")
	}
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	srcA, srcB, srcC, srcD := makeSource(t, dir, "a"), makeSource(t, dir, "b"), makeSource(t, dir, "c"), makeSource(t, dir, "d")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA, srcB, srcC, srcD)
	ws := sb.GetWorkspacePath()

	// c's copy holds a child inside a directory we cannot write to, so RemoveAll
	// fails partway (the same shape as a root-owned file a container process made).
	locked := filepath.Join(ws, "c")
	if err := os.MkdirAll(filepath.Join(locked, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	out, err := m.RemoveSources(ctx, sb.GetId(), []string{srcB.GetPath(), srcC.GetPath(), srcD.GetPath()})
	if err == nil {
		t.Fatal("expected the batch to fail on c")
	}
	if !strings.Contains(err.Error(), `"c"`) {
		t.Errorf("err = %v, want it to name the failed folder", err)
	}
	if exists(filepath.Join(ws, "b")) {
		t.Error("b was deleted before the failure and must stay gone")
	}
	if !exists(filepath.Join(ws, "d", "file.txt")) {
		t.Error("d comes after the failure and must be untouched")
	}
	want := []string{srcA.GetPath(), srcC.GetPath(), srcD.GetPath()}
	rec, _ := reg.Get(sb.GetId())
	if got := sourcePaths(rec); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("record sources = %v, want %v (record follows disk)", got, want)
	}
	if out == nil || len(out.GetSources()) != 3 {
		t.Error("the updated record must be returned alongside the error")
	}
	if rec.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING", rec.GetState())
	}
}

// --- US3: edited records reach every downstream reader ---

// After edits, a container relaunch receives the edited set and a stop→start
// cycle preserves it (FR-064).
func TestEditedSourcesReachRelaunchAndSurviveStopStart(t *testing.T) {
	ctx := context.Background()
	m, reg, runner, dir := newTestManager(t)
	srcA := makeSource(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA)
	srcB := makeSource(t, dir, "repo-b")

	if _, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{srcB}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RemoveSources(ctx, sb.GetId(), []string{srcA.GetPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}
	stopped, _ := reg.Get(sb.GetId())
	if got := sourcePaths(stopped); len(got) != 1 || got[0] != srcB.GetPath() {
		t.Errorf("sources after stop = %v, want {repo-b}", got)
	}
	runner.failStart = true // force bringUp down its relaunch branch
	out, err := m.Restart(ctx, sb.GetId(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.lastSources; len(got) != 1 || got[0].GetPath() != srcB.GetPath() {
		t.Errorf("relaunch LaunchSpec.Sources = %+v, want the edited set {repo-b}", got)
	}
	if got := sourcePaths(out); len(got) != 1 || got[0] != srcB.GetPath() {
		t.Errorf("sources after start = %v, want {repo-b}", got)
	}
}

// --- US4: state eligibility ---

// Edits work on STOPPED sandboxes (the retained copy is edited in place) and are
// refused on CREATING, DESTROYING, and ERROR (data-model eligibility matrix).
func TestSourceEditsStateEligibility(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	srcA, srcB := makeSource(t, dir, "repo-a"), makeSource(t, dir, "repo-b")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA, srcB)
	ws := sb.GetWorkspacePath()
	srcC := makeSource(t, dir, "repo-c")
	setState := func(st pb.SandboxState) {
		if _, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error { s.State = st; return nil }); err != nil {
			t.Fatal(err)
		}
	}

	setState(pb.SandboxState_SANDBOX_STATE_STOPPED)
	if _, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{srcC}, nil, nil); err != nil {
		t.Fatalf("add on a stopped sandbox: %v", err)
	}
	if !exists(filepath.Join(ws, "repo-c", "file.txt")) {
		t.Error("a stopped sandbox's retained copy must be edited on disk immediately")
	}
	if _, err := m.RemoveSources(ctx, sb.GetId(), []string{srcB.GetPath()}); err != nil {
		t.Fatalf("remove on a stopped sandbox: %v", err)
	}
	if exists(filepath.Join(ws, "repo-b")) {
		t.Error("removal on a stopped sandbox must delete the retained copy")
	}
	rec, _ := reg.Get(sb.GetId())
	if rec.GetState() != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Errorf("state = %v, want STOPPED (edits never change state)", rec.GetState())
	}

	srcD := makeSource(t, dir, "repo-d")
	for _, st := range []pb.SandboxState{
		pb.SandboxState_SANDBOX_STATE_CREATING,
		pb.SandboxState_SANDBOX_STATE_DESTROYING,
		pb.SandboxState_SANDBOX_STATE_ERROR,
	} {
		setState(st)
		_, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{srcD}, nil, nil)
		if !errors.Is(err, ErrIneligibleState) {
			t.Errorf("%v: add err = %v, want ErrIneligibleState", st, err)
		}
		if exists(filepath.Join(ws, "repo-d")) {
			t.Errorf("%v: a refused add copied something", st)
		}
		_, err = m.RemoveSources(ctx, sb.GetId(), []string{srcA.GetPath()})
		if !errors.Is(err, ErrIneligibleState) {
			t.Errorf("%v: remove err = %v, want ErrIneligibleState", st, err)
		}
		if !exists(filepath.Join(ws, "repo-a")) {
			t.Errorf("%v: a refused removal deleted something", st)
		}
	}
}

// A record whose workspace points outside the controlled folder is never edited.
func TestSourceEditsRefuseWorkspaceOutsideRoot(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	srcA, srcB := makeSource(t, dir, "repo-a"), makeSource(t, dir, "repo-b")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA, srcB)
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "repo-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error { s.WorkspacePath = outside; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSources(ctx, sb.GetId(), []*pb.SourceRef{makeSource(t, dir, "repo-c")}, nil, nil); err == nil {
		t.Error("expected the add to refuse a workspace outside the controlled folder")
	}
	if _, err := m.RemoveSources(ctx, sb.GetId(), []string{srcA.GetPath()}); err == nil {
		t.Error("expected the removal to refuse a workspace outside the controlled folder")
	}
	if !exists(filepath.Join(outside, "repo-a")) {
		t.Error("a directory outside the controlled folder was deleted")
	}
}

// --- startup purge ---

func TestPurgeStagingRemovesDebrisOnly(t *testing.T) {
	m, reg, _, dir := newTestManager(t)
	srcA := makeSource(t, dir, "repo-a")
	sb := launchSeeded(t, m, "sb", pb.SeedingMode_SEEDING_MODE_DUPLICATE, srcA)
	debris := filepath.Join(stagingOf(sb), "half-copied")
	if err := os.MkdirAll(debris, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(debris, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second record whose workspace lies outside the root must be skipped, and a
	// sandbox with no staging area is simply not touched.
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(filepath.Join(outside, workspaceMarkerDir, stagingDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reg.Put(&pb.Sandbox{Id: "elsewhere", WorkspacePath: outside, State: pb.SandboxState_SANDBOX_STATE_STOPPED}); err != nil {
		t.Fatal(err)
	}

	if err := m.PurgeStaging(); err != nil {
		t.Fatalf("PurgeStaging: %v", err)
	}
	if exists(stagingOf(sb)) {
		t.Error("staging debris was not purged")
	}
	if !exists(filepath.Join(sb.GetWorkspacePath(), "repo-a", "file.txt")) {
		t.Error("purge must touch nothing but the staging area")
	}
	if !exists(filepath.Join(sb.GetWorkspacePath(), workspaceMarkerDir, workspaceMarkerFile)) {
		t.Error("the workspace marker must survive the purge")
	}
	if !exists(filepath.Join(outside, workspaceMarkerDir, stagingDirName)) {
		t.Error("a workspace outside the controlled folder must be skipped")
	}
}
