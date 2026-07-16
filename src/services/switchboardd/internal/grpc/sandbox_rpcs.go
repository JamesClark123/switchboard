package grpc

import (
	"context"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
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
	kitSources, err := s.kits.ResolveAll(req.GetKits())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	sb, err := s.mgr.Launch(ctx, sandbox.LaunchRequest{
		Config:        req.GetConfig(),
		Sources:       req.GetSources(),
		AgentOverride: req.GetAgentOverride(),
		DisplayName:   req.GetDisplayName(),
		KitSources:    kitSources,
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

// ValidateKit materializes a kit and checks it with `sbx kit validate`, so the
// editor reports the host sbx's own diagnostics rather than a second, drifting
// implementation of Docker's (experimental) kit schema (feature 004, FR-034).
func (s *Server) ValidateKit(ctx context.Context, req *pb.ValidateKitRequest) (*pb.ValidateKitResponse, error) {
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
	src, err := s.kits.Resolve(req.GetKit())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	_, onLog := progressSender(stream)
	sb, err := s.mgr.AddKit(stream.Context(), req.GetSandboxId(), src, onLog)
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
