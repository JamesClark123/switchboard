package grpc

import (
	"context"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/resources"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxkit"
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

	sb, err := s.mgr.Launch(ctx, sandbox.LaunchRequest{
		Config:        req.GetConfig(),
		Sources:       req.GetSources(),
		AgentOverride: req.GetAgentOverride(),
		DisplayName:   req.GetDisplayName(),
	}, onProgress, onLog)
	if err != nil {
		return err
	}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
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
