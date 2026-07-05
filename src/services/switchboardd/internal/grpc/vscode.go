package grpc

import (
	"context"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// GetVSCodeTarget returns the sandbox's controlled workspace folder on this host
// — the retained verbatim copy of the seeded files — so the CLIENT opens that
// folder in VS Code (locally, or over Remote-SSH for a remote host), NOT the
// running container (FR-027).
//
// ssh_target is left empty: the daemon serves one host and does not know how the
// client reached it; the client fills ssh_target from its known-host entry.
func (s *Server) GetVSCodeTarget(_ context.Context, req *pb.SandboxIdRequest) (*pb.VSCodeTarget, error) {
	sb, err := s.mgr.Get(req.GetSandboxId())
	if err != nil {
		return nil, err
	}
	return &pb.VSCodeTarget{
		WorkspacePath: sb.GetWorkspacePath(),
	}, nil
}
