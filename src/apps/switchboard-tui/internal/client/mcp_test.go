package client_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// mcpFakeServer answers the feature-008 gateway RPCs with canned data.
type mcpFakeServer struct {
	pb.UnimplementedSwitchboardServer
	marked map[string]bool
	mode   pb.McpAttachMode
}

func (s *mcpFakeServer) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: "host-1", SbxVersion: "sbx version 0.46.0", SbxMinVersion: "0.36.0", RuntimeBaselineMet: true, McpGatewayAvailable: false, McpGatewayReason: "not signed in"}, nil
}
func (s *mcpFakeServer) ListMcpServers(context.Context, *pb.ListMcpServersRequest) (*pb.ListMcpServersResponse, error) {
	return &pb.ListMcpServersResponse{Servers: []*pb.McpServer{{Name: "notion", AttachByDefault: s.marked["notion"]}}, Settings: &pb.McpGatewaySettings{DefaultAttachMode: s.mode}}, nil
}
func (s *mcpFakeServer) AddMcpServer(_ context.Context, req *pb.AddMcpServerRequest) (*pb.McpServer, error) {
	return &pb.McpServer{Name: req.GetName(), Target: req.GetUrl()}, nil
}
func (s *mcpFakeServer) RemoveMcpServer(context.Context, *pb.RemoveMcpServerRequest) (*pb.RemoveMcpServerResponse, error) {
	return &pb.RemoveMcpServerResponse{Notes: []string{"sbx secret rm mcp:notion:api-key"}}, nil
}
func (s *mcpFakeServer) AuthorizeMcpServer(req *pb.AuthorizeMcpServerRequest, stream pb.Switchboard_AuthorizeMcpServerServer) error {
	deadline := time.Now().Add(10 * time.Minute).Unix()
	if err := stream.Send(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Url{Url: "https://auth.example/" + req.GetName()}, DeadlineUnix: deadline}); err != nil {
		return err
	}
	_ = stream.Send(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Message{Message: "waiting"}, DeadlineUnix: deadline})
	return stream.Send(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Done{Done: pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED}, DeadlineUnix: deadline})
}
func (s *mcpFakeServer) SetMcpServerDefault(_ context.Context, req *pb.SetMcpServerDefaultRequest) (*pb.McpServer, error) {
	s.marked[req.GetName()] = req.GetAttachByDefault()
	return &pb.McpServer{Name: req.GetName(), AttachByDefault: req.GetAttachByDefault()}, nil
}
func (s *mcpFakeServer) SetMcpAttachMode(_ context.Context, req *pb.SetMcpAttachModeRequest) (*pb.McpGatewaySettings, error) {
	s.mode = req.GetMode()
	return &pb.McpGatewaySettings{DefaultAttachMode: s.mode}, nil
}

func startMcpFake(t *testing.T) *client.Conn {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterSwitchboardServer(srv, &mcpFakeServer{marked: map[string]bool{}})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialLocal(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestMcpWrappersAndDaemonInfoAccessors(t *testing.T) {
	conn := startMcpFake(t)
	ctx := context.Background()
	if conn.SbxVersion() != "sbx version 0.46.0" || conn.SbxMinVersion() != "0.36.0" || !conn.RuntimeBaselineMet() {
		t.Errorf("accessors = %q %q %v", conn.SbxVersion(), conn.SbxMinVersion(), conn.RuntimeBaselineMet())
	}
	if ok, reason := conn.McpGateway(); ok || reason != "not signed in" {
		t.Errorf("McpGateway = %v %q", ok, reason)
	}
	if _, err := conn.SetMcpServerDefault(ctx, "notion", true); err != nil {
		t.Fatal(err)
	}
	servers, settings, err := conn.ListMcpServers(ctx)
	if err != nil || len(servers) != 1 || !servers[0].GetAttachByDefault() || settings == nil {
		t.Fatalf("list = %+v %+v %v", servers, settings, err)
	}
	if s, err := conn.SetMcpAttachMode(ctx, pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE); err != nil || s.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
		t.Errorf("mode = %+v %v", s, err)
	}
	if srv, err := conn.AddMcpServer(ctx, &pb.AddMcpServerRequest{Name: "linear", Definition: &pb.AddMcpServerRequest_Url{Url: "https://x/mcp"}}); err != nil || srv.GetTarget() != "https://x/mcp" {
		t.Errorf("add = %+v %v", srv, err)
	}
	if notes, err := conn.RemoveMcpServer(ctx, "notion"); err != nil || len(notes) != 1 {
		t.Errorf("remove = %v %v", notes, err)
	}
	var updates []client.McpAuthUpdate
	state, err := conn.AuthorizeMcpServer(ctx, "notion", func(u client.McpAuthUpdate) { updates = append(updates, u) })
	if err != nil || state != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED {
		t.Fatalf("authorize = %v %v", state, err)
	}
	if len(updates) != 3 || updates[0].URL != "https://auth.example/notion" || updates[0].Deadline.IsZero() || updates[1].Message != "waiting" || updates[2].Done == nil {
		t.Errorf("updates = %+v", updates)
	}
}
