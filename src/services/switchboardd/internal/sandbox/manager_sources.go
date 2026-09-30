package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Feature 007 — Edit Sandbox Sources. AddSources and RemoveSources edit an
// existing sandbox's seeded folder set IN PLACE: no container stop/restart, no
// PTY teardown, and — the invariant every path below preserves — no change to
// Sandbox.state (FR-054, FR-058, data-model I7). Only `sources` and `updated_at`
// move, and the on-change emit carries them to every subscriber (FR-067).
//
// Mechanics (research R2/R3): an add copies each new source into the workspace's
// own `.switchboard/staging/` and renames it into place only once EVERY source
// has staged, so a live agent never observes a half-copied seeded folder; any
// failure rolls the staging area and this operation's renamed folders back and
// leaves the record untouched. A removal is a guarded RemoveAll on the workspace
// CHILD — the bind mount root is never disturbed, which is why (unlike Refresh)
// nothing has to stop — and on a mid-batch failure the record follows the disk.

// stagingDirName is the directory, under the workspace's reserved `.switchboard`
// folder, where an add copies each new source before renaming it into place.
const stagingDirName = "staging"

// Operation labels reported by the per-sandbox latch (FR-066).
const (
	opAddSources    = "add sources"
	opRemoveSources = "remove sources"
	opRefresh       = "refresh"
)

// Sentinel errors the gRPC layer maps onto status codes (research R6).
var (
	// ErrInvalidSource: a path is empty/relative/missing/not a directory, the
	// selection is empty, a folder name is reserved or repeated in-batch.
	ErrInvalidSource = errors.New("invalid source")
	// ErrSourceExists: the folder name collides with a recorded source or an
	// on-disk top-level workspace entry.
	ErrSourceExists = errors.New("folder name collision")
	// ErrSourceNotRecorded: a removal target is not an exact recorded path.
	ErrSourceNotRecorded = errors.New("not a recorded source")
	// ErrIneligibleState: the sandbox is CREATING, DESTROYING, or ERROR.
	ErrIneligibleState = errors.New("sandbox folders cannot be edited")
	// ErrNotRepo: a clone-mode add that is not a git repository.
	ErrNotRepo = errors.New("clone mode requires a git repository")
	// ErrLastSource: the removal would leave the sandbox with no seeded folder.
	ErrLastSource = errors.New("a sandbox must keep at least one seeded folder")
)

// sourceFolder is the workspace child name a source seeds into — the same
// derivation duplicate.CopyAll and the clone path use.
func sourceFolder(path string) string {
	return filepath.Base(filepath.Clean(path))
}

// editableState reports whether a sandbox's folders may be edited in its current
// state (data-model eligibility matrix): RUNNING and STOPPED only. CREATING and
// DESTROYING own the workspace; ERROR is recovered by refresh/destroy, not edited.
func editableState(st pb.SandboxState) error {
	switch st {
	case pb.SandboxState_SANDBOX_STATE_RUNNING, pb.SandboxState_SANDBOX_STATE_STOPPED:
		return nil
	}
	label := strings.ToLower(strings.TrimPrefix(st.String(), "SANDBOX_STATE_"))
	return fmt.Errorf("%w: the sandbox is %s (folders can be edited only while running or stopped)", ErrIneligibleState, label)
}

// editableWorkspace re-checks that the record's workspace is inside the
// controlled folder before anything is created or deleted under it (I6).
func (m *Manager) editableWorkspace(sb *pb.Sandbox) (string, error) {
	wp := sb.GetWorkspacePath()
	if wp == "" || !within(m.workspaceRoot, wp) {
		return "", fmt.Errorf("refuse to edit %s: workspace %q is outside the controlled folder %q", sb.GetId(), wp, m.workspaceRoot)
	}
	return wp, nil
}

// ValidateAddSources runs every add-side refusal for a sandbox WITHOUT copying
// anything (FR-056) and returns the sandbox plus the normalized SourceRefs an add
// would record. The gRPC layer calls it before the resource gate so a refusal
// never waits behind a multi-GB size walk; AddSources re-runs it under the latch.
func (m *Manager) ValidateAddSources(id string, sources []*pb.SourceRef) (*pb.Sandbox, []*pb.SourceRef, error) {
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, nil, err
	}
	refs, err := m.validateAdd(sb, sources)
	if err != nil {
		return nil, nil, err
	}
	return sb, refs, nil
}

// validateAdd applies research R6's table: state eligibility, absolute existing
// directories, folder-name collisions against the record ∪ the on-disk workspace
// ∪ the batch itself, the reserved bookkeeping name, and — for clone-mode
// sandboxes — a daemon-side .git re-verification (the client's is_repo is
// advisory). Every refusal names the offending folder and the reason.
func (m *Manager) validateAdd(sb *pb.Sandbox, sources []*pb.SourceRef) ([]*pb.SourceRef, error) {
	if err := editableState(sb.GetState()); err != nil {
		return nil, err
	}
	wp, err := m.editableWorkspace(sb)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("%w: no folders selected", ErrInvalidSource)
	}
	recorded := map[string]string{} // folder -> origin path
	for _, s := range sb.GetSources() {
		recorded[sourceFolder(s.GetPath())] = s.GetPath()
	}
	seen := map[string]bool{}
	out := make([]*pb.SourceRef, 0, len(sources))
	for _, src := range sources {
		path := strings.TrimSpace(src.GetPath())
		if path == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("%w: path %q must be absolute", ErrInvalidSource, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidSource, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not a directory", ErrInvalidSource, path)
		}
		folder := sourceFolder(path)
		switch {
		case folder == "" || folder == "." || folder == ".." || folder == string(os.PathSeparator):
			return nil, fmt.Errorf("%w: %q has no usable folder name", ErrInvalidSource, path)
		case folder == workspaceMarkerDir:
			return nil, fmt.Errorf("%w: %q is reserved for switchboard's own bookkeeping", ErrInvalidSource, folder)
		case seen[folder]:
			return nil, fmt.Errorf("%w: folder %q is selected more than once in this request", ErrInvalidSource, folder)
		}
		if prev, ok := recorded[folder]; ok {
			return nil, fmt.Errorf("%w: folder %q is already seeded into this sandbox (from %s)", ErrSourceExists, folder, prev)
		}
		if _, err := os.Lstat(filepath.Join(wp, folder)); err == nil {
			return nil, fmt.Errorf("%w: folder %q already exists in the sandbox workspace", ErrSourceExists, folder)
		}
		isRepo := isGitRepo(path)
		if sb.GetSeedingMode() == pb.SeedingMode_SEEDING_MODE_CLONE && !isRepo {
			return nil, fmt.Errorf("%w: folder %q (%s) has no .git", ErrNotRepo, folder, path)
		}
		seen[folder] = true
		out = append(out, &pb.SourceRef{Path: path, IsRepo: isRepo})
	}
	return out, nil
}

// AddSources seeds additional folders into an existing sandbox's workspace
// (FR-053) with the sandbox's recorded seeding mode, streaming copy progress and
// clone output via the callbacks (FR-055), and appends them to the record on
// success. The sandbox is never stopped and its state never changes (FR-054).
//
// Lifecycle (data-model.md): acquire latch → validate (nothing copied on any
// refusal, FR-056) → stage every source under <workspace>/.switchboard/staging/
// → rename each staged folder into place → record → emit. Any failure after
// staging began removes the staging area and every folder THIS operation renamed
// (they are fresh copies, safe to delete) and returns with the record untouched
// (FR-058, SC-005). Originals are opened read-only throughout (I5).
func (m *Manager) AddSources(ctx context.Context, id string, sources []*pb.SourceRef, onProgress func(duplicate.Progress), onLog func(string)) (*pb.Sandbox, error) {
	if err := m.ops.acquire(id, opAddSources); err != nil {
		return nil, err
	}
	defer m.ops.release(id)

	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	refs, err := m.validateAdd(sb, sources)
	if err != nil {
		return nil, err
	}
	wp := sb.GetWorkspacePath()
	staging := filepath.Join(wp, workspaceMarkerDir, stagingDirName)

	// Anything already under staging is debris from an interrupted add — the
	// latch guarantees no concurrent one — so start from a clean area.
	if err := os.RemoveAll(staging); err != nil {
		return nil, fmt.Errorf("clear staging area: %w", err)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, fmt.Errorf("create staging area: %w", err)
	}

	var renamed []string // final paths committed by THIS operation
	rollback := func() {
		for _, p := range renamed {
			_ = os.RemoveAll(p)
		}
		_ = os.RemoveAll(staging)
	}

	if err := m.stage(ctx, sb, refs, staging, onProgress, onLog); err != nil {
		rollback()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		rollback()
		return nil, err
	}
	// Commit: every source staged, so each rename is the cheap same-filesystem
	// directory move — the folder appears complete or not at all.
	for _, ref := range refs {
		folder := sourceFolder(ref.GetPath())
		dest := filepath.Join(wp, folder)
		if err := os.Rename(filepath.Join(staging, folder), dest); err != nil {
			rollback()
			return nil, fmt.Errorf("move folder %q into place: %w", folder, err)
		}
		renamed = append(renamed, dest)
	}
	if err := os.RemoveAll(staging); err != nil {
		log.Printf("[warn] add sources %s: staging area not removed: %v", id, err)
	}

	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.Sources = append(s.Sources, refs...)
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		// The disk moved but the record did not: undo the disk so record == disk
		// (I1) still holds, then report.
		rollback()
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// stage copies (or clones) each source into staging/<folder>, honoring the
// sandbox's seeding mode exactly as launch does.
func (m *Manager) stage(ctx context.Context, sb *pb.Sandbox, refs []*pb.SourceRef, staging string, onProgress func(duplicate.Progress), onLog func(string)) error {
	if sb.GetSeedingMode() == pb.SeedingMode_SEEDING_MODE_CLONE {
		for _, ref := range refs {
			dest := filepath.Join(staging, sourceFolder(ref.GetPath()))
			if err := m.runner.CloneRepo(ctx, ref.GetPath(), dest, onLog); err != nil {
				return err
			}
		}
		return nil
	}
	paths := make([]string, 0, len(refs))
	for _, ref := range refs {
		paths = append(paths, ref.GetPath())
	}
	_, err := duplicate.CopyAll(paths, staging, onProgress)
	return err
}

// RemoveSources deletes seeded folders' copies from a sandbox's workspace and
// drops them from the record (FR-059). paths are EXACT recorded SourceRef.path
// values; an unknown one is refused before anything is deleted, as is a batch
// that would empty the record (FR-061) or an ineligible state. A copy already
// missing on disk counts as removed (FR-063).
//
// Folders are deleted in request order; on a mid-batch failure the loop stops,
// the folders already deleted stay dropped from the record (record follows disk,
// research R3), the failed folder stays recorded, and the returned error names
// it — alongside the updated record. The sandbox's state never changes, and only
// direct children of the controlled workspace are ever deleted (I6).
func (m *Manager) RemoveSources(_ context.Context, id string, paths []string) (*pb.Sandbox, error) {
	if err := m.ops.acquire(id, opRemoveSources); err != nil {
		return nil, err
	}
	defer m.ops.release(id)

	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	if err := editableState(sb.GetState()); err != nil {
		return nil, err
	}
	wp, err := m.editableWorkspace(sb)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: no folders selected", ErrInvalidSource)
	}
	recorded := map[string]bool{}
	for _, s := range sb.GetSources() {
		recorded[s.GetPath()] = true
	}
	targets := make([]string, 0, len(paths)) // deduplicated, request order
	seen := map[string]bool{}
	for _, p := range paths {
		if !recorded[p] {
			return nil, fmt.Errorf("%w: %s", ErrSourceNotRecorded, p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		targets = append(targets, p)
	}
	if len(targets) >= len(recorded) {
		return nil, ErrLastSource
	}

	removed := map[string]bool{}
	var failure error
	for _, p := range targets {
		folder := sourceFolder(p)
		child := filepath.Join(wp, folder)
		if child == filepath.Clean(wp) || !within(wp, child) {
			failure = fmt.Errorf("refuse to delete %q: it does not resolve to a workspace child", folder)
			break
		}
		// RemoveAll treats an already-missing path as success (FR-063).
		if err := os.RemoveAll(child); err != nil {
			failure = fmt.Errorf("delete folder %q: %w", folder, err)
			break
		}
		removed[p] = true
	}
	if len(removed) == 0 {
		return nil, failure
	}
	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		kept := make([]*pb.SourceRef, 0, len(s.Sources))
		for _, src := range s.Sources {
			if !removed[src.GetPath()] {
				kept = append(kept, src)
			}
		}
		s.Sources = kept
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	if failure != nil {
		return out, failure
	}
	return out, nil
}

// PurgeStaging removes leftover `.switchboard/staging` areas from every
// registered sandbox's workspace (research R2 / risk 3). An add interrupted by a
// daemon crash can strand a partial copy there; it is never a corrupt workspace
// (staged trees sit outside the seeded set) but it may be gigabytes. Best-effort:
// every workspace is visited and the errors, if any, are joined.
func (m *Manager) PurgeStaging() error {
	all, err := m.store.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, sb := range all {
		wp := sb.GetWorkspacePath()
		if wp == "" || !within(m.workspaceRoot, wp) {
			continue
		}
		staging := filepath.Join(wp, workspaceMarkerDir, stagingDirName)
		if _, err := os.Lstat(staging); err != nil {
			continue
		}
		if err := os.RemoveAll(staging); err != nil {
			errs = append(errs, fmt.Errorf("purge staging for %s: %w", sb.GetId(), err))
		}
	}
	return errors.Join(errs...)
}
