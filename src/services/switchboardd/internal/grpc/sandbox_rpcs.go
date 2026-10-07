package grpc

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/escapehatch"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/portforward"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/resources"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxkit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LaunchSandbox seeds + starts a sandbox, server-streaming copy progress and sbx
// logs, then a terminal Sandbox (FR-028). A low-resource block (FR-012f) without
// override is returned as LaunchProgress.blocked instead of done.
func (s *Server) LaunchSandbox(req *pb.LaunchSandboxRequest, stream pb.Switchboard_LaunchSandboxServer) error {
	ctx := stream.Context()

	// Validate the config's kit options against the host manifest; a config
	// referencing an unsupported option fails loudly naming the key (FR-014,
	// spec edge case) rather than silently dropping it.
	if err := sbxkit.Validate(s.manifest, req.GetConfig().GetKitOptions()); err != nil {
		return err
	}

	srcPaths := make([]string, 0, len(req.GetSources()))
	for _, src := range req.GetSources() {
		srcPaths = append(srcPaths, src.GetPath())
	}

	// Pre-launch resource gate (FR-012f).
	if !req.GetOverrideResourceWarning() && req.GetConfig().GetSeedingMode() != pb.SeedingMode_SEEDING_MODE_CLONE {
		rep, err := resources.Check(srcPaths, s.workspaceRoot)
		if err != nil {
			return err
		}
		if !rep.OK {
			return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Blocked{Blocked: toResourceReport(rep)}})
		}
	}

	onProgress := func(p duplicate.Progress) {
		_ = stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Copy{Copy: &pb.LaunchProgress_CopyProgress{
			BytesCopied: p.BytesCopied,
			BytesTotal:  p.BytesTotal,
			CurrentPath: p.CurrentPath,
		}}})
	}
	onLog := func(line string) {
		_ = stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_LogLine{LogLine: line}})
	}

	// Materialize any client-authored kits onto this host and resolve external kit
	// sources, before anything is copied — an unusable kit should fail the launch
	// up front rather than after a multi-GB duplicate (FR-032).
	if len(req.GetKits()) > 0 {
		if err := s.requireKitBaseline(); err != nil {
			return err
		}
	}
	kitSources, err := s.kits.ResolveAll(req.GetKits())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	// Resolve the escape-hatch command set from the attached client-authored kits
	// with later-kit-wins on name collision (feature 005, FR-036). A bad entry fails
	// the launch up front, before any copy.
	ehCommands, err := escapehatch.Resolve(escapehatch.CommandsFromRefs(req.GetKits())...)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	// Same for the declared service set (feature 006, FR-044).
	services, err := portforward.Resolve(portforward.ServicesFromRefs(req.GetKits())...)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	// Feature 008: apply the daemon's attach-by-default MCP servers (FR-081). A
	// resolution failure is logged and the launch proceeds without MCP — never a
	// failed launch (FR-083). No gateway on this host ⇒ launch exactly as before.
	var mcpAttach *sandbox.McpAttach
	if s.mcp != nil && s.mcpAvail.Available {
		if att, err := s.mcp.ResolveAttach(ctx); err != nil {
			onLog("mcp: could not resolve attach-by-default servers (launching without): " + err.Error())
		} else {
			mcpAttach = &sandbox.McpAttach{Mode: att.Mode, Servers: att.Servers, Skipped: att.Skipped}
		}
	}

	sb, err := s.mgr.Launch(ctx, sandbox.LaunchRequest{
		Config:              req.GetConfig(),
		Sources:             req.GetSources(),
		AgentOverride:       req.GetAgentOverride(),
		DisplayName:         req.GetDisplayName(),
		KitSources:          kitSources,
		EscapeHatchCommands: ehCommands,
		Services:            services,
		Mcp:                 mcpAttach,
	}, onProgress, onLog)
	if err != nil {
		return err
	}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
}

// launchProgressStream is the common shape of every RPC that server-streams
// LaunchProgress (launch, restart, refresh, kit-add). Each generated stream type is
// distinct, so the shared sender is written against the one method they agree on.
type launchProgressStream interface {
	Send(*pb.LaunchProgress) error
}

// progressSender adapts a LaunchProgress stream to the Manager's onProgress/onLog
// callbacks. Sends are fire-and-forget, matching LaunchSandbox: a slow or vanished
// client must never break an in-flight lifecycle operation.
func progressSender(stream launchProgressStream) (func(duplicate.Progress), func(string)) {
	onProgress := func(p duplicate.Progress) {
		_ = stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Copy{Copy: &pb.LaunchProgress_CopyProgress{
			BytesCopied: p.BytesCopied,
			BytesTotal:  p.BytesTotal,
			CurrentPath: p.CurrentPath,
		}}})
	}
	onLog := func(line string) {
		_ = stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_LogLine{LogLine: line}})
	}
	return onProgress, onLog
}

// RefreshSandbox re-seeds a sandbox's workspace from its recorded sources and
// brings it back up on the same container (feature 004, FR-030). DESTRUCTIVE — the
// retained copy is deleted; the client is responsible for confirming with the user.
func (s *Server) RefreshSandbox(req *pb.SandboxIdRequest, stream pb.Switchboard_RefreshSandboxServer) error {
	onProgress, onLog := progressSender(stream)
	sb, err := s.mgr.Refresh(stream.Context(), req.GetSandboxId(), onProgress, onLog)
	if err != nil {
		return err
	}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: s.withTerminalCounts(sb)}})
}

// --- Edit sandbox sources (feature 007, FR-053..FR-067) ---

// AddSandboxSources seeds additional folders into an existing sandbox's workspace
// IN PLACE — no stop, restart, or state change (FR-054) — streaming the same
// LaunchProgress shape as LaunchSandbox: copy progress / clone log lines, then a
// terminal `done` carrying the updated Sandbox, or `blocked` when the duplicate-
// mode resource gate trips without override (FR-057). Every research-R6 refusal
// is checked before the gate so nothing waits behind a size walk, and the manager
// re-checks under its latch before a byte is copied (FR-056).
func (s *Server) AddSandboxSources(req *pb.AddSandboxSourcesRequest, stream pb.Switchboard_AddSandboxSourcesServer) error {
	sb, refs, err := s.mgr.ValidateAddSources(req.GetSandboxId(), req.GetSources())
	if err != nil {
		return sourcesStatus(err)
	}
	// Resource gate, exactly like LaunchSandbox: duplicate mode only (clone sizes
	// are unknowable up front), overridable by re-sending with the flag set.
	if !req.GetOverrideResourceWarning() && sb.GetSeedingMode() != pb.SeedingMode_SEEDING_MODE_CLONE {
		paths := make([]string, 0, len(refs))
		for _, ref := range refs {
			paths = append(paths, ref.GetPath())
		}
		rep, err := resources.Check(paths, s.workspaceRoot)
		if err != nil {
			return err
		}
		if !rep.OK {
			return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Blocked{Blocked: toResourceReport(rep)}})
		}
	}
	onProgress, onLog := progressSender(stream)
	out, err := s.mgr.AddSources(stream.Context(), req.GetSandboxId(), req.GetSources(), onProgress, onLog)
	if err != nil {
		return sourcesStatus(err)
	}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: s.withTerminalCounts(out)}})
}

// RemoveSandboxSources deletes seeded folders' copies and drops them from the
// record (FR-059). DESTRUCTIVE for the copy — the client confirms first (FR-060);
// the daemon does not re-confirm, as with RefreshSandbox. Targets are exact
// recorded SourceRef.path values. The FR-062 service guard runs here, where the
// port-forwarding supervisor is wired: a removal is refused while a declared
// service instance is starting or running with its working directory at or
// beneath a selected folder — refuse, never auto-stop.
func (s *Server) RemoveSandboxSources(ctx context.Context, req *pb.RemoveSandboxSourcesRequest) (*pb.Sandbox, error) {
	sb, err := s.mgr.Get(req.GetSandboxId())
	if err != nil {
		return nil, sourcesStatus(err)
	}
	if err := s.serviceBlocksRemoval(sb, req.GetSourcePaths()); err != nil {
		return nil, err
	}
	out, err := s.mgr.RemoveSources(ctx, req.GetSandboxId(), req.GetSourcePaths())
	if err != nil {
		// On a mid-batch failure the manager already persisted and emitted the
		// folders that did go; the error names the one that did not.
		return nil, sourcesStatus(err)
	}
	return s.withTerminalCounts(out), nil
}

// serviceBlocksRemoval implements the FR-062 guard: an ACTIVE (starting or
// running) instance of a declared service whose workspace-relative working_dir is
// a selected folder, or sits beneath one, blocks the removal, naming the service
// and the remedy. A root/empty working_dir never blocks — the root is not being
// deleted (research R4).
func (s *Server) serviceBlocksRemoval(sb *pb.Sandbox, paths []string) error {
	if s.services == nil || len(paths) == 0 {
		return nil
	}
	folders := map[string]bool{}
	for _, p := range paths {
		folders[filepath.Base(filepath.Clean(p))] = true
	}
	declared := map[string]*pb.KitService{}
	for _, d := range sb.GetServices() {
		declared[d.GetName()] = d
	}
	for _, inst := range s.services.Instances().ActiveBySandbox(sb.GetId()) {
		d, ok := declared[inst.GetServiceName()]
		if !ok {
			continue
		}
		wd := filepath.ToSlash(filepath.Clean(strings.TrimSpace(d.GetWorkingDir())))
		if wd == "" || wd == "." {
			continue
		}
		top := strings.SplitN(wd, "/", 2)[0]
		if folders[top] {
			verb := "running"
			if inst.GetState() == pb.ServiceState_SERVICE_STATE_STARTING {
				verb = "starting"
			}
			return status.Errorf(codes.FailedPrecondition,
				"service %q is %s with its working directory in %q — stop it first", d.GetName(), verb, top)
		}
	}
	return nil
}

// sourcesStatus maps the manager's source-edit sentinels onto gRPC codes
// (research R6). An error that already carries a status passes through.
func sourcesStatus(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, registry.ErrNotFound), errors.Is(err, sandbox.ErrSourceNotRecorded):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, sandbox.ErrInvalidSource):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, sandbox.ErrSourceExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, sandbox.ErrBusy),
		errors.Is(err, sandbox.ErrIneligibleState),
		errors.Is(err, sandbox.ErrNotRepo),
		errors.Is(err, sandbox.ErrLastSource):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return err
	}
}

// ValidateKit materializes a kit and checks it with `sbx kit validate`, so the
// editor reports the host sbx's own diagnostics rather than a second, drifting
// implementation of Docker's (experimental) kit schema (feature 004, FR-034).
// requireKitBaseline refuses kit operations on a host whose sandbox CLI predates
// kit schema 2 (feature 008, FR-096), naming both versions. Kit-less sandbox
// operations are untouched.
//
// Only a KNOWN version below the baseline refuses; when the probe could not read
// a version at all (no CLI, unparseable output) the operation proceeds and fails
// with the CLI's own, more specific error.
func (s *Server) requireKitBaseline() error {
	if s.mcpAvail.BaselineMet || s.mcpAvail.SbxVersion == "" {
		return nil
	}
	return status.Errorf(codes.FailedPrecondition, "host sbx %s is below the minimum %s required for kit schema 2 (host %s)", s.mcpAvail.SbxVersion, sbxkit.MinSbxVersion, s.hostID)
}

func (s *Server) ValidateKit(ctx context.Context, req *pb.ValidateKitRequest) (*pb.ValidateKitResponse, error) {
	if err := s.requireKitBaseline(); err != nil {
		return nil, err
	}
	dir, err := s.kits.Write(req.GetKit())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out, err := s.mgr.ValidateKit(ctx, dir)
	if err != nil {
		// A non-zero exit means sbx rejected the kit — that is a validation result,
		// not an RPC failure, so it comes back in the response body.
		return &pb.ValidateKitResponse{Ok: false, Errors: splitLines(out, err)}, nil
	}
	return &pb.ValidateKitResponse{Ok: true, Warnings: splitLines(out, nil)}, nil
}

// AddSandboxKit attaches a kit to an already-created sandbox (`sbx kit add`,
// FR-033). sbx restarts the sandbox to apply it; VM state is preserved.
func (s *Server) AddSandboxKit(req *pb.AddSandboxKitRequest, stream pb.Switchboard_AddSandboxKitServer) error {
	if err := s.requireKitBaseline(); err != nil {
		return err
	}
	src, err := s.kits.Resolve(req.GetKit())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	// The kit's escape-hatch commands (client-authored kits only) are merged into the
	// sandbox's set inside AddKit, later-kit-wins (feature 005).
	newCommands := req.GetKit().GetSpec().GetEscapeHatch()
	// Likewise the kit's declared services (feature 006, FR-044).
	newServices := req.GetKit().GetSpec().GetServices()
	_, onLog := progressSender(stream)
	sb, err := s.mgr.AddKit(stream.Context(), req.GetSandboxId(), src, newCommands, newServices, onLog)
	if err != nil {
		return err
	}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: s.withTerminalCounts(sb)}})
}

// splitLines turns sbx's combined output into diagnostic lines, falling back to
// the process error when it exits non-zero without saying anything useful.
func splitLines(out string, err error) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 && err != nil {
		lines = []string{err.Error()}
	}
	return lines
}

// StopSandbox stops a sandbox, retaining its copy (FR-012a).
func (s *Server) StopSandbox(ctx context.Context, req *pb.SandboxIdRequest) (*pb.Sandbox, error) {
	return s.mgr.Stop(ctx, req.GetSandboxId())
}

// RestartSandbox restarts from the retained copy, streaming progress (FR-012b).
func (s *Server) RestartSandbox(req *pb.SandboxIdRequest, stream pb.Switchboard_RestartSandboxServer) error {
	onLog := func(line string) {
		_ = stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_LogLine{LogLine: line}})
	}
	sb, err := s.mgr.Restart(stream.Context(), req.GetSandboxId(), onLog)
	if err != nil {
		return err
	}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
}

// DestroySandbox removes the sandbox and deletes its copy (FR-012c).
func (s *Server) DestroySandbox(ctx context.Context, req *pb.SandboxIdRequest) (*pb.DestroyResponse, error) {
	deleted, err := s.mgr.Destroy(ctx, req.GetSandboxId())
	if err != nil {
		return nil, err
	}
	return &pb.DestroyResponse{DeletedWorkspace: deleted}, nil
}

// RenameSandbox gives a stopped sandbox a new unique per-host name and moves its
// workspace copy to match (FR-012e).
func (s *Server) RenameSandbox(ctx context.Context, req *pb.RenameSandboxRequest) (*pb.Sandbox, error) {
	return s.mgr.Rename(ctx, req.GetSandboxId(), req.GetDisplayName())
}

// SetSandboxTag sets or clears a sandbox's mutable purpose tag (feature 003,
// FR-021..024). It changes no other attribute and never affects lifecycle.
func (s *Server) SetSandboxTag(_ context.Context, req *pb.SetSandboxTagRequest) (*pb.Sandbox, error) {
	sb, err := s.mgr.SetTag(req.GetSandboxId(), req.GetTag())
	if err != nil {
		return nil, err
	}
	return s.withTerminalCounts(sb), nil
}

// ResolveWorkspace maps a filesystem path to the sandbox that owns it, so `sxb`
// run inside a workspace can open that sandbox's session (feature 003, FR-017/018).
func (s *Server) ResolveWorkspace(_ context.Context, req *pb.ResolveWorkspaceRequest) (*pb.ResolveWorkspaceResponse, error) {
	sb, ok, err := s.mgr.ResolveWorkspace(req.GetPath())
	if err != nil {
		return nil, err
	}
	if !ok {
		return &pb.ResolveWorkspaceResponse{Found: false}, nil
	}
	return &pb.ResolveWorkspaceResponse{
		Found:     true,
		SandboxId: sb.GetId(),
		State:     sb.GetState(),
	}, nil
}

// ListSourceCandidates enumerates launch candidates under a root (FR-007).
func (s *Server) ListSourceCandidates(_ context.Context, req *pb.ListSourceCandidatesRequest) (*pb.ListSourceCandidatesResponse, error) {
	root := req.GetRoot()
	if root == "" {
		root = "."
	}
	cands, err := sandbox.ListSourceCandidates(root, req.GetReposOnly())
	if err != nil {
		return nil, err
	}
	return &pb.ListSourceCandidatesResponse{Candidates: cands}, nil
}

// CheckResources reports the pre-launch disk estimate + warnings (FR-012f).
func (s *Server) CheckResources(_ context.Context, req *pb.CheckResourcesRequest) (*pb.ResourceReport, error) {
	paths := make([]string, 0, len(req.GetSources()))
	for _, src := range req.GetSources() {
		paths = append(paths, src.GetPath())
	}
	rep, err := resources.Check(paths, s.workspaceRoot)
	if err != nil {
		return nil, err
	}
	return toResourceReport(rep), nil
}

func toResourceReport(r *resources.Report) *pb.ResourceReport {
	return &pb.ResourceReport{
		Ok:             r.OK,
		RequiredBytes:  r.RequiredBytes,
		AvailableBytes: r.AvailableBytes,
		Warnings:       r.Warnings,
	}
}
