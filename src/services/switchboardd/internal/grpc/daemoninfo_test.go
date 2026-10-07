package grpc

import (
	"context"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxkit"
)

// DaemonInfo advertises the runtime baseline and gateway availability probed at
// startup (feature 008, FR-096/FR-079).
func TestGetDaemonInfoCarriesBaselineAndGateway(t *testing.T) {
	s := NewServer(Config{HostID: "h1", DaemonVersion: "v1", SbxVersion: "sbx version 0.46.0",
		McpAvailability: mcp.Availability{Available: false, Reason: "not signed in", SbxVersion: "0.46.0", BaselineMet: true}})
	info, err := s.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetSbxMinVersion() != sbxkit.MinSbxVersion || !info.GetRuntimeBaselineMet() || info.GetMcpGatewayAvailable() || info.GetMcpGatewayReason() != "not signed in" {
		t.Errorf("DaemonInfo = %+v", info)
	}
}
