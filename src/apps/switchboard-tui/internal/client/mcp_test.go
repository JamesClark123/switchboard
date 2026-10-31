package client_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mcpFakeServer answers the feature-008 gateway RPCs with canned data.
type mcpFakeServer struct {
	pb.UnimplementedSwitchboardServer
	marked map[string]bool
	mode   pb.McpAttachMode
	// SWI-21: the last authorize request, save request and cancel/remove calls.
	authReq   *pb.AuthorizeMcpServerRequest
	saveReq   *pb.SaveMcpServerRequest
	cancelReq *pb.CancelMcpPendingReplaceRequest
	rmReq     *pb.RemoveMcpServerSecretsRequest
}

func (s *mcpFakeServer) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: "host-1", SbxVersion: "sbx version 0.46.0", SbxMinVersion: "0.36.0", RuntimeBaselineMet: true, McpGatewayAvailable: false, McpGatewayReason: "not signed in"}, nil
}
func (s *mcpFakeServer) ListMcpServers(context.Context, *pb.ListMcpServersRequest) (*pb.ListMcpServersResponse, error) {
	return &pb.ListMcpServersResponse{Servers: []*pb.McpServer{{Name: "notion", AttachByDefault: s.marked["notion"]}}, Settings: &pb.McpGatewaySettings{DefaultAttachMode: s.mode},
		PendingReplaces: []*pb.McpPendingReplace{{Name: "linear", Revision: 3}}}, nil
}
func (s *mcpFakeServer) AddMcpServer(_ context.Context, req *pb.AddMcpServerRequest) (*pb.McpServer, error) {
	return &pb.McpServer{Name: req.GetName(), Target: req.GetUrl()}, nil
}
func (s *mcpFakeServer) RemoveMcpServer(context.Context, *pb.RemoveMcpServerRequest) (*pb.RemoveMcpServerResponse, error) {
	return &pb.RemoveMcpServerResponse{Notes: []string{"sbx secret rm mcp:notion:api-key"}}, nil
}
func (s *mcpFakeServer) AuthorizeMcpServer(req *pb.AuthorizeMcpServerRequest, stream pb.Switchboard_AuthorizeMcpServerServer) error {
	s.authReq = req
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
func (s *mcpFakeServer) AttachMcpServer(_ context.Context, req *pb.AttachMcpServerRequest) (*pb.Sandbox, error) {
	return &pb.Sandbox{Id: req.GetSandboxId(), McpServers: []string{req.GetName()}, McpAttachMode: pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE}, nil
}

// refusal is a FAILED_PRECONDITION (or NOT_FOUND) status with an McpSaveRefusal detail.
func refusal(code codes.Code, reason pb.McpSaveRefusal_Reason, msg string) error {
	st, _ := status.New(code, "refused: "+msg).WithDetails(&pb.McpSaveRefusal{Reason: reason, Message: msg})
	return st.Err()
}

func (s *mcpFakeServer) GetMcpServerDefinition(_ context.Context, req *pb.GetMcpServerDefinitionRequest) (*pb.McpDefinitionView, error) {
	switch req.GetName() {
	case "gone":
		return nil, refusal(codes.NotFound, pb.McpSaveRefusal_GONE, "gone is no longer registered")
	case "plain":
		return nil, status.Error(codes.NotFound, "no such server")
	}
	return &pb.McpDefinitionView{Name: req.GetName(), Fingerprint: "fp", HasRecord: !req.GetForAdd()}, nil
}

func (s *mcpFakeServer) SaveMcpServer(_ context.Context, req *pb.SaveMcpServerRequest) (*pb.McpSaveOutcome, error) {
	s.saveReq = req
	switch req.GetName() {
	case "stale":
		return nil, refusal(codes.FailedPrecondition, pb.McpSaveRefusal_STALE_CHANGED, "changed on the host")
	case "nodetail":
		return nil, status.Error(codes.FailedPrecondition, "local execution not acknowledged")
	case "emptymsg":
		st, _ := status.New(codes.FailedPrecondition, "status text").WithDetails(&pb.McpSaveRefusal{Reason: pb.McpSaveRefusal_UNVERIFIED})
		return nil, st.Err()
	case "notrecorded":
		st, _ := status.New(codes.Unavailable, "the result could not be recorded yet").WithDetails(
			&errdetails.ErrorInfo{Reason: client.SaveNotRecordedReason, Domain: client.SaveNotRecordedDomain})
		return nil, st.Err()
	case "otherinfo":
		st, _ := status.New(codes.Unavailable, "other").WithDetails(&errdetails.ErrorInfo{Reason: "SOMETHING_ELSE", Domain: client.SaveNotRecordedDomain})
		return nil, st.Err()
	case "unreachable":
		return nil, status.Error(codes.Unavailable, "connection refused")
	}
	return &pb.McpSaveOutcome{Name: req.GetName(), State: pb.McpSaveOutcome_REGISTERED}, nil
}

func (s *mcpFakeServer) CancelMcpPendingReplace(_ context.Context, req *pb.CancelMcpPendingReplaceRequest) (*pb.CancelMcpPendingReplaceResponse, error) {
	s.cancelReq = req
	if req.GetName() == "busy" {
		return nil, refusal(codes.FailedPrecondition, pb.McpSaveRefusal_BEING_REPLACED, "a save of this server is still running; try again later")
	}
	return &pb.CancelMcpPendingReplaceResponse{LeftOnHost: []string{"mcp:linear:k"}}, nil
}

func (s *mcpFakeServer) RemoveMcpServerSecrets(_ context.Context, req *pb.RemoveMcpServerSecretsRequest) (*pb.RemoveMcpServerSecretsResponse, error) {
	s.rmReq = req
	if req.GetName() == "busy" {
		return nil, status.Error(codes.Unavailable, "down")
	}
	return &pb.RemoveMcpServerSecretsResponse{Removed: req.GetSecrets()}, nil
}

func startMcpFake(t *testing.T) *client.Conn {
	t.Helper()
	conn, _ := startMcpFakeServer(t)
	return conn
}

func startMcpFakeServer(t *testing.T) (*client.Conn, *mcpFakeServer) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	fake := &mcpFakeServer{marked: map[string]bool{}}
	pb.RegisterSwitchboardServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialLocal(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, fake
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
	if sb, err := conn.AttachMcpServer(ctx, "sb-1", "notion"); err != nil || sb.GetId() != "sb-1" || len(sb.GetMcpServers()) != 1 {
		t.Errorf("attach = %+v %v", sb, err)
	}
	var updates []client.McpAuthUpdate
	state, err := conn.AuthorizeMcpServer(ctx, "notion", nil, false, func(u client.McpAuthUpdate) { updates = append(updates, u) })
	if err != nil || state != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED {
		t.Fatalf("authorize = %v %v", state, err)
	}
	if len(updates) != 3 || updates[0].URL != "https://auth.example/notion" || updates[0].Deadline.IsZero() || updates[1].Message != "waiting" || updates[2].Done == nil {
		t.Errorf("updates = %+v", updates)
	}
}

func TestMcpEditWrappers(t *testing.T) {
	conn, fake := startMcpFakeServer(t)
	ctx := context.Background()

	resp, err := conn.ListMcpGateway(ctx)
	if err != nil || len(resp.GetPendingReplaces()) != 1 || resp.GetPendingReplaces()[0].GetRevision() != 3 {
		t.Fatalf("gateway = %+v %v", resp, err)
	}
	if v, err := conn.GetMcpServerDefinition(ctx, "notion", false); err != nil || v.GetFingerprint() != "fp" || !v.GetHasRecord() {
		t.Errorf("definition = %+v %v", v, err)
	}
	if v, err := conn.GetMcpServerDefinition(ctx, "new", true); err != nil || v.GetHasRecord() {
		t.Errorf("for_add = %+v %v", v, err)
	}
	_, err = conn.GetMcpServerDefinition(ctx, "gone", false)
	if r, ok := client.AsSaveRefusal(err); !ok || r.Reason != pb.McpSaveRefusal_GONE || r.Code != codes.NotFound || r.Error() != "gone is no longer registered" {
		t.Errorf("gone = %v (%+v)", err, r)
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("the refusal keeps its code; got %v", status.Code(err))
	}
	_, err = conn.GetMcpServerDefinition(ctx, "plain", false)
	if _, ok := client.AsSaveRefusal(err); ok || status.Code(err) != codes.NotFound {
		t.Errorf("a NOT_FOUND without a detail is not a refusal; got %v", err)
	}

	out, err := conn.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "notion", Fingerprint: "fp", SecretValues: map[string]string{"mcp:notion:k": "v"}})
	if err != nil || out.GetState() != pb.McpSaveOutcome_REGISTERED || fake.saveReq.GetSecretValues()["mcp:notion:k"] != "v" {
		t.Errorf("save = %+v %v", out, err)
	}
	_, err = conn.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "stale"})
	if r, ok := client.AsSaveRefusal(err); !ok || r.Reason != pb.McpSaveRefusal_STALE_CHANGED || r.Message != "changed on the host" {
		t.Errorf("stale = %v", err)
	}
	_, err = conn.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "nodetail"})
	if _, ok := client.AsSaveRefusal(err); ok || err == nil {
		t.Errorf("a precondition without a detail stays a plain error; got %v", err)
	}
	_, err = conn.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "emptymsg"})
	if r, ok := client.AsSaveRefusal(err); !ok || r.Message != "status text" {
		t.Errorf("an empty refusal message falls back to the status text; got %v", err)
	}
	if _, ok := client.AsSaveRefusal(nil); ok {
		t.Error("nil is not a refusal")
	}
	// Only the daemon's own UNAVAILABLE carries the not-recorded ErrorInfo; a
	// bare UNAVAILABLE is a transport failure (the save may never have run).
	_, err = conn.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "notrecorded"})
	if !client.IsSaveNotRecorded(err) {
		t.Errorf("not recorded = %v", err)
	}
	for _, name := range []string{"unreachable", "otherinfo", "stale"} {
		_, err = conn.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: name})
		if client.IsSaveNotRecorded(err) {
			t.Errorf("%s is not the not-recorded case: %v", name, err)
		}
	}
	if client.IsSaveNotRecorded(nil) {
		t.Error("nil is not the not-recorded case")
	}

	if res, err := conn.CancelMcpPendingReplace(ctx, "linear", 3); err != nil || len(res.GetLeftOnHost()) != 1 || fake.cancelReq.GetRevision() != 3 {
		t.Errorf("cancel = %+v %v", res, err)
	}
	if _, err := conn.CancelMcpPendingReplace(ctx, "busy", 1); err == nil {
		t.Error("cancel refusal should be returned")
	} else if r, ok := client.AsSaveRefusal(err); !ok || r.Reason != pb.McpSaveRefusal_BEING_REPLACED {
		t.Errorf("cancel refusal = %v", err)
	}
	if res, err := conn.RemoveMcpServerSecrets(ctx, "notion", []string{"mcp:notion:k"}); err != nil || len(res.GetRemoved()) != 1 || fake.rmReq.GetName() != "notion" {
		t.Errorf("remove secrets = %+v %v", res, err)
	}
	if _, err := conn.RemoveMcpServerSecrets(ctx, "busy", nil); status.Code(err) != codes.Unavailable {
		t.Errorf("remove secrets error = %v", err)
	}

	if _, err := conn.AuthorizeMcpServer(ctx, "notion", []string{"read", "write"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := fake.authReq.GetScopes(); len(got) != 2 || got[0] != "read" || fake.authReq.GetNoScope() {
		t.Errorf("authorize scopes = %+v", fake.authReq)
	}
	if _, err := conn.AuthorizeMcpServer(ctx, "notion", nil, true, nil); err != nil || !fake.authReq.GetNoScope() {
		t.Errorf("authorize no-scope = %+v %v", fake.authReq, err)
	}
}
