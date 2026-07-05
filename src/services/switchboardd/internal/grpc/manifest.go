package grpc

import (
	"context"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// GetOptionManifest returns the host's full sbx option surface so the client
// editor can cover 100% of kit options (FR-014). Returns an empty manifest when
// introspection was unavailable at startup.
func (s *Server) GetOptionManifest(_ context.Context, _ *pb.GetOptionManifestRequest) (*pb.OptionManifest, error) {
	if s.manifest == nil {
		return &pb.OptionManifest{SbxVersion: s.sbxVersion}, nil
	}
	return s.manifest, nil
}
