package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store is the subset of the registry the Manager needs (eases testing).
type Store interface {
	Put(*pb.Sandbox) error
	Get(string) (*pb.Sandbox, error)
	Delete(string) error
	List() ([]*pb.Sandbox, error)
	Update(string, func(*pb.Sandbox) error) (*pb.Sandbox, error)
}

// Manager orchestrates sandbox lifecycle on a single host.
type Manager struct {
	store         Store
	runner        Runner
	workspaceRoot string
	hostID        string

	// onChange is invoked after any sandbox state change so the gRPC layer can
	// fan it out on the event stream. MAY be nil.
	onChange func(*pb.Sandbox)

	// injectHooks, when set, is called after a workspace is seeded so the agent's
	// Claude Code hooks can be written into it (US4). MAY be nil.
	injectHooks func(sandboxID, workspacePath string) error
}

// SetHookInjector registers a callback invoked after seeding to inject agent
// hooks into the workspace (US4).
func (m *Manager) SetHookInjector(fn func(sandboxID, workspacePath string) error) {
	m.injectHooks = fn
}

// NewManager constructs a Manager.
func NewManager(store Store, runner Runner, workspaceRoot, hostID string) *Manager {
	return &Manager{store: store, runner: runner, workspaceRoot: workspaceRoot, hostID: hostID}
}

// SetOnChange registers a sandbox-change observer.
func (m *Manager) SetOnChange(fn func(*pb.Sandbox)) { m.onChange = fn }

func (m *Manager) emit(s *pb.Sandbox) {
	if m.onChange != nil && s != nil {
		m.onChange(s)
	}
}

// LaunchRequest captures everything needed to create a sandbox.
type LaunchRequest struct {
	Config        *pb.ConfigSnapshot
	Sources       []*pb.SourceRef
	AgentOverride *pb.AgentSpec
	DisplayName   string
}

// List returns all sandboxes on this host, first pruning any whose retained
// workspace directory has vanished (an unusable, out-of-sync record) so stale
// entries disappear from the UI automatically.
func (m *Manager) List() ([]*pb.Sandbox, error) {
	all, err := m.store.List()
	if err != nil {
		return nil, err
	}
	out := make([]*pb.Sandbox, 0, len(all))
	for _, sb := range all {
		if m.workspaceGone(sb) {
			if delErr := m.store.Delete(sb.GetId()); delErr == nil {
				m.emit(&pb.Sandbox{Id: sb.GetId(), State: pb.SandboxState_SANDBOX_STATE_DESTROYING})
			}
			continue
		}
		out = append(out, sb)
	}
	return out, nil
}

// workspaceGone reports whether a sandbox's retained copy is missing on disk. A
// CREATING sandbox is exempt (its directory may not exist yet mid-launch).
func (m *Manager) workspaceGone(sb *pb.Sandbox) bool {
	if sb.GetState() == pb.SandboxState_SANDBOX_STATE_CREATING {
		return false
	}
	wp := sb.GetWorkspacePath()
	if wp == "" || !within(m.workspaceRoot, wp) {
		return false
	}
	_, err := os.Stat(wp)
	return os.IsNotExist(err)
}

// Get returns a single sandbox by id.
func (m *Manager) Get(id string) (*pb.Sandbox, error) { return m.store.Get(id) }

// Launch duplicates (or clones) the sources, starts the sandbox via the runner,
// and persists a running record. Progress and sbx output are streamed via the
// callbacks (FR-028). The originals are never modified (SC-002).
func (m *Manager) Launch(ctx context.Context, req LaunchRequest, onProgress func(duplicate.Progress), onLog func(string)) (*pb.Sandbox, error) {
	if req.Config == nil {
		return nil, errors.New("config is required")
	}
	if len(req.Sources) == 0 {
		return nil, errors.New("at least one source is required")
	}

	id := uuid.NewString()
	now := timestamppb.Now()

	// The name is unique per host and IS the workspace directory (FR-012e). The
	// uuid stays the registry key / cross-host identity and the sbx handle.
	name, err := m.resolveName(strings.TrimSpace(req.DisplayName), req.Config.GetName(), id)
	if err != nil {
		return nil, err
	}
	workspacePath := filepath.Join(m.workspaceRoot, name)

	agent := req.Config.GetAgent()
	if agent == nil || agent.GetKind() == "" {
		agent = req.AgentOverride // FR-016b: user-chosen agent when config omits one
	}

	sb := &pb.Sandbox{
		Id:             id,
		DisplayName:    name,
		State:          pb.SandboxState_SANDBOX_STATE_CREATING,
		HostId:         m.hostID,
		ConfigSnapshot: req.Config,
		ConfigLabel:    req.Config.GetName(),
		Sources:        req.Sources,
		SeedingMode:    req.Config.GetSeedingMode(),
		WorkspacePath:  workspacePath,
		Agent:          &pb.AgentSession{Spec: agent, Status: pb.AgentStatus_AGENT_STATUS_IDLE, LastEventAt: now},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := m.store.Put(sb); err != nil {
		return nil, err
	}
	m.emit(sb)

	if err := m.seed(ctx, sb, onProgress, onLog); err != nil {
		return m.fail(sb, err)
	}

	// Drop a workspace marker so `sxb` run from inside the copy can auto-resolve
	// this sandbox (feature 003, FR-017). Best-effort; ResolveWorkspace is the
	// authoritative fallback.
	if err := writeWorkspaceMarker(workspacePath, m.hostID, id); err != nil && onLog != nil {
		onLog("warning: workspace marker not written: " + err.Error())
	}

	// Inject agent hooks into the seeded workspace so the agent reports task
	// completion / needs-prompting back to the daemon (US4). Non-fatal.
	if m.injectHooks != nil {
		if err := m.injectHooks(id, workspacePath); err != nil && onLog != nil {
			onLog("warning: hook injection failed: " + err.Error())
		}
	}

	ref, err := m.runner.Launch(ctx, LaunchSpec{
		SandboxID:     id,
		Name:          name,
		WorkspacePath: workspacePath,
		KitOptions:    req.Config.GetKitOptions(),
		SeedingMode:   req.Config.GetSeedingMode(),
		Sources:       req.Sources,
	}, onLog)
	if err != nil {
		return m.fail(sb, err)
	}

	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.ContainerRef = ref
		s.State = pb.SandboxState_SANDBOX_STATE_RUNNING
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// seed performs duplicate or clone into the controlled folder.
func (m *Manager) seed(ctx context.Context, sb *pb.Sandbox, onProgress func(duplicate.Progress), onLog func(string)) error {
	if sb.GetSeedingMode() == pb.SeedingMode_SEEDING_MODE_CLONE {
		if err := os.MkdirAll(sb.GetWorkspacePath(), 0o755); err != nil {
			return err
		}
		for _, src := range sb.GetSources() {
			if !src.GetIsRepo() {
				return fmt.Errorf("clone mode requires a git repo: %s", src.GetPath())
			}
			dest := filepath.Join(sb.GetWorkspacePath(), filepath.Base(src.GetPath()))
			if err := m.runner.CloneRepo(ctx, src.GetPath(), dest, onLog); err != nil {
				return err
			}
		}
		return nil
	}

	paths := make([]string, 0, len(sb.GetSources()))
	for _, s := range sb.GetSources() {
		paths = append(paths, s.GetPath())
	}
	_, err := duplicate.CopyAll(paths, sb.GetWorkspacePath(), onProgress)
	return err
}

func (m *Manager) fail(sb *pb.Sandbox, cause error) (*pb.Sandbox, error) {
	out, err := m.store.Update(sb.GetId(), func(s *pb.Sandbox) error {
		s.State = pb.SandboxState_SANDBOX_STATE_ERROR
		s.Error = cause.Error()
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, cause
}

// sbxHandle is the name sbx knows a sandbox by. The daemon creates with
// `sbx create --name <DisplayName>` (the per-host-unique human name), so that
// name is the handle for stop/start/rm/status. It falls back to the uuid only if
// a record somehow has no name.
func sbxHandle(sb *pb.Sandbox) string {
	if n := sb.GetDisplayName(); n != "" {
		return n
	}
	return sb.GetId()
}

// sbxRefs lists the handles a sandbox might be addressed by in sbx, most-specific
// first: the human name, then the uuid id as a fallback. The id fallback recovers
// records that are out of sync with sbx — e.g. renamed before names drove the
// sbx --name, so the container is still named by the id.
func sbxRefs(sb *pb.Sandbox) []string {
	name, id := sb.GetDisplayName(), sb.GetId()
	if name == "" || name == id {
		return []string{id}
	}
	return []string{name, id}
}

// tryRefs runs op against each candidate handle until one succeeds, returning the
// last error if all fail.
func tryRefs(refs []string, op func(string) error) error {
	var err error
	for _, r := range refs {
		if err = op(r); err == nil {
			return nil
		}
	}
	if err == nil {
		return nil
	}
	// Report every handle tried so a failure isn't misread as "only tried the id".
	return fmt.Errorf("no sbx sandbox matched any of %v: %w", refs, err)
}

// Stop stops the container but RETAINS the workspace copy (FR-012a).
func (m *Manager) Stop(ctx context.Context, id string) (*pb.Sandbox, error) {
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	log.Printf("[debug] stop sandbox %s via %v (stored container_ref=%q)", id, sbxRefs(sb), sb.GetContainerRef())
	if sb.GetContainerRef() != "" {
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Stop(ctx, r) }); err != nil {
			return nil, err
		}
	}
	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.State = pb.SandboxState_SANDBOX_STATE_STOPPED
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// Restart brings a stopped sandbox back up from its retained copy (FR-012b). It
// first tries to resume an existing container (`sbx start`, by name then id), but
// many sbx setups don't keep a startable container across a stop, so on failure —
// or when there's no handle — it relaunches from the retained workspace copy,
// clearing any stale container first.
func (m *Manager) Restart(ctx context.Context, id string, onLog func(string)) (*pb.Sandbox, error) {
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}

	started := false
	if sb.GetContainerRef() != "" {
		log.Printf("[debug] restart sandbox %s: start via %v", id, sbxRefs(sb))
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Start(ctx, r) }); err == nil {
			started = true
		} else {
			// sbx couldn't resume it (no such container, no `start` subcommand,
			// etc.). Drop any stale container and relaunch from the retained copy.
			log.Printf("[debug] restart %s: start failed (%v); relaunching from the retained copy", id, err)
			_ = tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Destroy(ctx, r) })
		}
	}
	if !started {
		ref, lerr := m.runner.Launch(ctx, LaunchSpec{
			SandboxID:     id,
			Name:          sb.GetDisplayName(),
			WorkspacePath: sb.GetWorkspacePath(),
			KitOptions:    sb.GetConfigSnapshot().GetKitOptions(),
			SeedingMode:   sb.GetSeedingMode(),
			Sources:       sb.GetSources(),
		}, onLog)
		if lerr != nil {
			return nil, lerr
		}
		sb.ContainerRef = ref
	}
	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.ContainerRef = sb.GetContainerRef()
		s.State = pb.SandboxState_SANDBOX_STATE_RUNNING
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// Destroy removes the container and DELETES the retained copy (FR-012c).
func (m *Manager) Destroy(ctx context.Context, id string) (bool, error) {
	sb, err := m.store.Get(id)
	if err != nil {
		return false, err
	}
	if sb.GetContainerRef() != "" {
		// Best-effort: the container may already be gone or out of sync (renamed
		// without its folder, etc.). Try the name then the id; if both fail, log
		// and still remove the workspace + record so the entry can be cleared.
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Destroy(ctx, r) }); err != nil {
			log.Printf("[warn] destroy %s: sbx rm failed for %v (removing record anyway): %v", id, sbxRefs(sb), err)
		}
	}
	deletedWorkspace := false
	if wp := sb.GetWorkspacePath(); wp != "" && within(m.workspaceRoot, wp) {
		if err := os.RemoveAll(wp); err != nil {
			return false, err
		}
		deletedWorkspace = true
	}
	if err := m.store.Delete(id); err != nil {
		return false, err
	}
	m.emit(&pb.Sandbox{Id: id, State: pb.SandboxState_SANDBOX_STATE_DESTROYING})
	return deletedWorkspace, nil
}

// SetAgentStatus updates a sandbox's agent status (driven by injected Claude Code
// hooks — FR-024/025) and publishes the change so subscribers see it live.
func (m *Manager) SetAgentStatus(sandboxID string, status pb.AgentStatus, now time.Time) (*pb.Sandbox, error) {
	out, err := m.store.Update(sandboxID, func(s *pb.Sandbox) error {
		if s.Agent == nil {
			s.Agent = &pb.AgentSession{}
		}
		s.Agent.Status = status
		s.Agent.LastEventAt = timestamppb.New(now)
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// SetTag sets (or clears, when tag is "") a sandbox's mutable purpose tag
// (feature 003, FR-021/022). The tag is trimmed and capped; no other field
// changes, so tagging never affects identity or lifecycle. Persisted via the
// registry's proto marshaling and emitted so the list updates.
func (m *Manager) SetTag(sandboxID, tag string) (*pb.Sandbox, error) {
	tag = strings.TrimSpace(tag)
	if len(tag) > maxTagLen {
		tag = tag[:maxTagLen]
	}
	out, err := m.store.Update(sandboxID, func(s *pb.Sandbox) error {
		s.Tag = tag
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// maxTagLen bounds a sandbox tag (research.md R6).
const maxTagLen = 64

// workspaceMarkerDir/File name the on-disk marker `sxb` walks up to find so it can
// auto-open a sandbox's session from within its workspace copy (feature 003, R6).
const (
	workspaceMarkerDir  = ".switchboard"
	workspaceMarkerFile = "session.json"
)

// writeWorkspaceMarker records {host_id, sandbox_id} under <workspace>/.switchboard/
// session.json. Overwrites any prior marker.
func writeWorkspaceMarker(workspacePath, hostID, sandboxID string) error {
	if workspacePath == "" {
		return nil
	}
	dir := filepath.Join(workspacePath, workspaceMarkerDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	blob, err := json.Marshal(map[string]string{"host_id": hostID, "sandbox_id": sandboxID})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, workspaceMarkerFile), blob, 0o644)
}

// ResolveWorkspace returns the sandbox whose retained workspace copy contains
// path (or is path), matching the deepest workspace so nested subdirectories
// resolve correctly (feature 003, FR-017/018). ok is false when no sandbox owns
// the path.
func (m *Manager) ResolveWorkspace(path string) (*pb.Sandbox, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, err
	}
	all, err := m.store.List()
	if err != nil {
		return nil, false, err
	}
	var best *pb.Sandbox
	var bestLen int
	for _, sb := range all {
		wp := sb.GetWorkspacePath()
		if wp == "" {
			continue
		}
		if within(wp, abs) && len(wp) > bestLen {
			best, bestLen = sb, len(wp)
		}
	}
	if best == nil {
		return nil, false, nil
	}
	return best, true, nil
}

// sandboxNameRe constrains sandbox names: they become a filesystem directory and
// an sbx --name, so only a safe, path-segment-friendly character set is allowed.
var sandboxNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validName reports whether name is usable as a per-host sandbox name.
func validName(name string) error {
	switch {
	case name == "":
		return errors.New("sandbox name cannot be empty")
	case len(name) > 64:
		return errors.New("sandbox name too long (max 64 characters)")
	case !sandboxNameRe.MatchString(name):
		return fmt.Errorf("sandbox name %q must start with a letter or digit and use only letters, digits, '-', '_', '.'", name)
	}
	return nil
}

// nameTaken reports whether another sandbox on this host already uses name.
func (m *Manager) nameTaken(name, exceptID string) (bool, error) {
	all, err := m.store.List()
	if err != nil {
		return false, err
	}
	for _, s := range all {
		if s.GetId() != exceptID && s.GetDisplayName() == name {
			return true, nil
		}
	}
	return false, nil
}

// resolveName picks the sandbox's unique, filesystem-safe name. An explicit name
// must be valid and unused (error otherwise). A name derived from the config
// label is used when valid+free, auto-uniquified on collision, and falls back to
// a short-id default when there is no usable label.
func (m *Manager) resolveName(explicit, label, id string) (string, error) {
	if explicit != "" {
		if err := validName(explicit); err != nil {
			return "", err
		}
		taken, err := m.nameTaken(explicit, id)
		if err != nil {
			return "", err
		}
		if taken {
			return "", fmt.Errorf("a sandbox named %q already exists on this host", explicit)
		}
		return explicit, nil
	}

	fallback := "sandbox-" + id[:8]
	label = strings.TrimSpace(label)
	if label == "" || validName(label) != nil {
		return fallback, nil
	}
	taken, err := m.nameTaken(label, id)
	if err != nil {
		return "", err
	}
	if taken {
		return label + "-" + id[:8], nil
	}
	return label, nil
}

// Rename gives a sandbox a new unique per-host name and moves its retained
// workspace copy to match (FR-012e). The sandbox MUST be stopped: the running
// container's mount is tied to the old path, so the container is released and
// relaunched from the renamed copy on the next start. The uuid id is unchanged.
func (m *Manager) Rename(ctx context.Context, id, newName string) (*pb.Sandbox, error) {
	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	newName = strings.TrimSpace(newName)
	if err := validName(newName); err != nil {
		return nil, err
	}
	if newName == sb.GetDisplayName() {
		return sb, nil // no-op
	}
	if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
		return nil, errors.New("stop the sandbox before renaming it")
	}
	taken, err := m.nameTaken(newName, id)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, fmt.Errorf("a sandbox named %q already exists on this host", newName)
	}

	// Release the old container (its --name/mount reference the old path); the
	// retained copy is preserved and relaunched under the new name on next start.
	if sb.GetContainerRef() != "" {
		if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.Destroy(ctx, r) }); err != nil {
			return nil, err
		}
	}
	newPath := filepath.Join(m.workspaceRoot, newName)
	if old := sb.GetWorkspacePath(); old != "" && within(m.workspaceRoot, old) {
		if _, statErr := os.Stat(old); statErr == nil {
			if err := os.Rename(old, newPath); err != nil {
				return nil, err
			}
		}
	}

	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.DisplayName = newName
		s.WorkspacePath = newPath
		s.ContainerRef = "" // recreated from the renamed copy on next start
		s.State = pb.SandboxState_SANDBOX_STATE_STOPPED
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// Readopt reconciles the registry with live containers on daemon startup
// (FR-002a, SC-012): still-running containers are marked running; the rest are
// marked stopped. Records are never deleted here.
func (m *Manager) Readopt(ctx context.Context) error {
	all, err := m.store.List()
	if err != nil {
		return err
	}
	for _, sb := range all {
		running := false
		if sb.GetContainerRef() != "" {
			for _, r := range sbxRefs(sb) {
				if ok, _ := m.runner.IsRunning(ctx, r); ok {
					running = true
					break
				}
			}
		}
		want := pb.SandboxState_SANDBOX_STATE_STOPPED
		if running {
			want = pb.SandboxState_SANDBOX_STATE_RUNNING
		}
		if sb.GetState() == want {
			continue
		}
		if _, err := m.store.Update(sb.GetId(), func(s *pb.Sandbox) error {
			s.State = want
			s.UpdatedAt = timestamppb.Now()
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// within reports whether path is inside root (defense against deleting outside
// the controlled folder).
func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) && !startsWithDotDot(rel)
}

func startsWithDotDot(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.'
}
