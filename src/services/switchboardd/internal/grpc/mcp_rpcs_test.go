package grpc_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/secret"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// mcpStubSbx writes a fake `sbx` answering the documented `mcp` verbs and logging
// every argv to <dir>/sbx.log.
func mcpStubSbx(t *testing.T) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "sbx")
	logPath = filepath.Join(dir, "sbx.log")
	script := `#!/usr/bin/env bash
echo "$*" >> "` + logPath + `"
case "$1 $2 $3" in
  "version  ") echo "sbx version: v0.46.0 0411f50ee4700fe7bd37e6e7e3aced563e850ca9" ;;
  "mcp ls ") printf 'NAME     TRANSPORT     STATUS       GATEWAY                 DETAIL\nnotion   remote http   needs auth   LOCAL, managed by you   sbx mcp auth notion\n' ;;
  "mcp auth status") echo '[{"server_name":"notion","status":"authorized"}]' ;;
  "mcp auth "*) echo 'OAuth scopes for MCP server "notion":'; echo 'Open this URL to authorize MCP server "notion":'; echo 'https://api.notion.com/v1/oauth/authorize?x=1'; echo 'MCP server "notion" authorized' ;;
  "mcp rm "*) printf 'No local OAuth token found for MCP server "notion"\nMCP server "notion" removed\n' ;;
  "mcp add "*) exit 0 ;;
  "mcp load "*) echo "MCP server \"$3\" loaded into sandbox \"$5\" (live)" ;;
  "secret ls ") printf 'SCOPE      TYPE      NAME        SECRET\n(global)   service   github      gho_GCaw4o****...****43qy\nmy-sb      service   anthropic   sk-ant-****\n(global)   registry  ghcr.io     ****\n' ;;
  "secret set "*) cat > "$(dirname "$0")/secret.stdin"; echo "Secret stored." ;;
  "secret rm "*) case "$*" in *ghost*) echo 'error: secret "ghost" not found' >&2; exit 1 ;; esac; echo removed ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

type mcpEnv struct {
	client pb.SwitchboardClient
	runner *testRunner
	dir    string
	sbxLog string
}

// startMcpServer brings up a server whose gateway manager drives the stub sbx.
func startMcpServer(t *testing.T, avail mcp.Availability) *mcpEnv {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	ws := filepath.Join(dir, "ws")
	_ = os.MkdirAll(ws, 0o755)
	runner := &testRunner{running: map[string]bool{}}
	mgr := sandbox.NewManager(reg, runner, ws, "host-1")
	bin, sbxLog := mcpStubSbx(t)
	hub := agent.NewHub("host-1")
	manager := mcp.NewManager(&mcp.CLI{Bin: bin}, reg, "host-1", hub.Publish)
	srv := sbgrpc.NewServer(sbgrpc.Config{
		Manager: mgr, HostID: "host-1", DaemonVersion: "test", WorkspaceRoot: ws,
		KitRoot: filepath.Join(dir, "kits"), Hub: hub, McpAvailability: avail, Mcp: manager, Secrets: secret.NewManager(&secret.CLI{Bin: bin}, "host-1", hub.Publish),
	})
	sock := filepath.Join(dir, "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("unix:"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &mcpEnv{client: pb.NewSwitchboardClient(conn), runner: runner, dir: dir, sbxLog: sbxLog}
}

var available = mcp.Availability{Available: true, SbxVersion: "0.46.0", BaselineMet: true}

func TestMcpRPCsRefusedWhenGatewayUnavailable(t *testing.T) {
	env := startMcpServer(t, mcp.Availability{Available: false, Reason: "this host is not signed in", SbxVersion: "0.46.0", BaselineMet: true})
	_, err := env.client.ListMcpServers(context.Background(), &pb.ListMcpServersRequest{})
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || !strings.Contains(st.Message(), "not signed in") {
		t.Errorf("err = %v, want FAILED_PRECONDITION with the probed reason", err)
	}
	_, err = env.client.AddMcpServer(context.Background(), &pb.AddMcpServerRequest{Name: "x", Definition: &pb.AddMcpServerRequest_Url{Url: "https://x/mcp"}})
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition {
		t.Errorf("add err = %v", err)
	}
}

func TestMcpRPCsRoundTrip(t *testing.T) {
	env := startMcpServer(t, available)
	ctx := context.Background()

	// Subscribe first so the mark change's event is observable.
	sub, err := env.client.Subscribe(ctx, &pb.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := env.client.ListMcpServers(ctx, &pb.ListMcpServersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetServers()) != 1 || resp.GetServers()[0].GetName() != "notion" || resp.GetServers()[0].GetKind() != pb.McpServerKind_MCP_SERVER_KIND_REMOTE ||
		resp.GetServers()[0].GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED || resp.GetServers()[0].GetAttachByDefault() {
		t.Fatalf("list = %+v", resp.GetServers())
	}
	if resp.GetSettings().GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Errorf("default mode should read as additive, got %v", resp.GetSettings().GetDefaultAttachMode())
	}

	marked, err := env.client.SetMcpServerDefault(ctx, &pb.SetMcpServerDefaultRequest{Name: "notion", AttachByDefault: true})
	if err != nil || !marked.GetAttachByDefault() {
		t.Fatalf("SetDefault = %+v, %v", marked, err)
	}
	ev, err := sub.Recv()
	if err != nil || ev.GetMcpGatewayChanged().GetHostId() != "host-1" {
		t.Errorf("expected an mcp_gateway_changed event for host-1, got %+v / %v", ev, err)
	}
	if _, err := env.client.SetMcpServerDefault(ctx, &pb.SetMcpServerDefaultRequest{Name: "ghost", AttachByDefault: true}); status.Code(err) != codes.NotFound {
		t.Errorf("marking an unregistered name = %v, want NOT_FOUND", err)
	}

	settings, err := env.client.SetMcpAttachMode(ctx, &pb.SetMcpAttachModeRequest{Mode: pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE})
	if err != nil || settings.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
		t.Errorf("SetMode = %+v, %v", settings, err)
	}

	// Local servers need the isolation acknowledgement; remote adds pass --skip-auth.
	_, err = env.client.AddMcpServer(ctx, &pb.AddMcpServerRequest{Name: "pw", Definition: &pb.AddMcpServerRequest_Command{Command: &pb.AddMcpServerRequest_LocalCommand{Command: "npx", Args: []string{"@playwright/mcp@latest"}}}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("unacknowledged local add = %v, want FAILED_PRECONDITION", err)
	}
	if _, err := env.client.AddMcpServer(ctx, &pb.AddMcpServerRequest{Name: "linear", Definition: &pb.AddMcpServerRequest_Url{Url: "https://mcp.linear.app/mcp"}}); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if _, err := env.client.AddMcpServer(ctx, &pb.AddMcpServerRequest{Name: "bad name", Definition: &pb.AddMcpServerRequest_Url{Url: "https://x/mcp"}}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("bad name = %v, want INVALID_ARGUMENT", err)
	}

	// Authorize streams the URL first, then the terminal frame.
	auth, err := env.client.AuthorizeMcpServer(ctx, &pb.AuthorizeMcpServerRequest{Name: "notion"})
	if err != nil {
		t.Fatal(err)
	}
	var url string
	var done pb.McpAuthState
	for {
		f, err := auth.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("auth recv: %v", err)
		}
		if u := f.GetUrl(); u != "" {
			url = u
			if f.GetDeadlineUnix() == 0 {
				t.Error("every frame should carry the deadline")
			}
		}
		if f.GetDone() != pb.McpAuthState_MCP_AUTH_STATE_UNSPECIFIED {
			done = f.GetDone()
		}
	}
	if !strings.HasPrefix(url, "https://api.notion.com/") || done != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED {
		t.Errorf("authorize: url %q done %v", url, done)
	}

	rm, err := env.client.RemoveMcpServer(ctx, &pb.RemoveMcpServerRequest{Name: "notion"})
	if err != nil || len(rm.GetNotes()) != 1 || !strings.Contains(rm.GetNotes()[0], "No local OAuth token") {
		t.Errorf("remove = %+v, %v", rm, err)
	}

	b, _ := os.ReadFile(env.sbxLog)
	log := string(b)
	for _, want := range []string{"mcp add linear --url https://mcp.linear.app/mcp --skip-auth", "mcp auth notion", "mcp rm notion --force"} {
		if !strings.Contains(log, want) {
			t.Errorf("sbx log should contain %q:\n%s", want, log)
		}
	}
}

// Launch resolves the daemon's marks and applies them in the daemon's mode,
// recording the result on the sandbox and in the launch log (FR-081/FR-086).
func TestLaunchAppliesAttachByDefaultMarks(t *testing.T) {
	env := startMcpServer(t, available)
	ctx := context.Background()
	if _, err := env.client.SetMcpServerDefault(ctx, &pb.SetMcpServerDefaultRequest{Name: "notion", AttachByDefault: true}); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(env.dir, "src")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o644)

	stream, err := env.client.LaunchSandbox(ctx, &pb.LaunchSandboxRequest{
		Config: &pb.ConfigSnapshot{Name: "with-mcp"}, Sources: []*pb.SourceRef{{Path: src}}, OverrideResourceWarning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	var sb *pb.Sandbox
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if l := msg.GetLogLine(); l != "" {
			logs = append(logs, l)
		}
		if d := msg.GetDone(); d != nil {
			sb = d
		}
	}
	if sb == nil || strings.Join(sb.GetMcpServers(), ",") != "notion" || sb.GetMcpAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Fatalf("sandbox = %+v", sb)
	}
	if len(env.runner.mcpLoads) != 1 || !strings.HasSuffix(env.runner.mcpLoads[0], " notion") {
		t.Errorf("runner loads = %v", env.runner.mcpLoads)
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "mcp: attached notion") {
		t.Errorf("launch log should name the attached server; got:\n%s", joined)
	}
}

// Kit RPCs are refused below the runtime baseline, naming both versions; kit-less
// launches are untouched (FR-096).
func TestKitRPCsGateOnRuntimeBaseline(t *testing.T) {
	env := startMcpServer(t, mcp.Availability{Available: false, Reason: "below baseline", SbxVersion: "0.35.0", BaselineMet: false})
	ctx := context.Background()
	_, err := env.client.ValidateKit(ctx, &pb.ValidateKitRequest{Kit: &pb.KitSpec{Id: "k", SpecYaml: "schemaVersion: \"2\"\nkind: mixin\nname: k\n"}})
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || !strings.Contains(st.Message(), "0.35.0") || !strings.Contains(st.Message(), "0.36.0") {
		t.Errorf("ValidateKit = %v, want FAILED_PRECONDITION naming both versions", err)
	}
	src := filepath.Join(env.dir, "src2")
	_ = os.MkdirAll(src, 0o755)
	sb := launch(t, env.client, src) // kit-less launch still works
	addStream, err := env.client.AddSandboxKit(ctx, &pb.AddSandboxKitRequest{SandboxId: sb.GetId(), Kit: &pb.KitRef{Ref: &pb.KitRef_Source{Source: "/k"}}})
	if err == nil {
		_, err = addStream.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("AddSandboxKit = %v, want FAILED_PRECONDITION", err)
	}
	kitLaunch, err := env.client.LaunchSandbox(ctx, &pb.LaunchSandboxRequest{
		Config: &pb.ConfigSnapshot{Name: "kit"}, Sources: []*pb.SourceRef{{Path: src}}, OverrideResourceWarning: true,
		Kits: []*pb.KitRef{{Ref: &pb.KitRef_Source{Source: "/k"}}},
	})
	if err == nil {
		_, err = kitLaunch.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("launch with kits = %v, want FAILED_PRECONDITION", err)
	}
	_ = time.Second
}

// launchViaRPC launches one sandbox through the LaunchSandbox stream and returns
// its terminal record.
func launchViaRPC(t *testing.T, env *mcpEnv, name string) *pb.Sandbox {
	t.Helper()
	src := filepath.Join(env.dir, "src-"+name)
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o644)
	stream, err := env.client.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config: &pb.ConfigSnapshot{Name: name}, Sources: []*pb.SourceRef{{Path: src}}, OverrideResourceWarning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sb *pb.Sandbox
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if d := msg.GetDone(); d != nil {
			sb = d
		}
	}
	if sb == nil {
		t.Fatal("launch produced no terminal sandbox")
	}
	return sb
}

// AttachMcpServer (FR-102): loads a registered server into an existing sandbox,
// appends it to the record, and maps the failure modes to codes.
func TestAttachMcpServerRPC(t *testing.T) {
	env := startMcpServer(t, available)
	ctx := context.Background()
	sb := launchViaRPC(t, env, "plain")
	if len(sb.GetMcpServers()) != 0 {
		t.Fatalf("precondition: launched with servers %v", sb.GetMcpServers())
	}

	out, err := env.client.AttachMcpServer(ctx, &pb.AttachMcpServerRequest{SandboxId: sb.GetId(), Name: "notion"})
	if err != nil {
		t.Fatalf("AttachMcpServer: %v", err)
	}
	if strings.Join(out.GetMcpServers(), ",") != "notion" || out.GetMcpAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE {
		t.Errorf("sandbox = %+v", out)
	}
	if len(env.runner.mcpLoads) != 1 || env.runner.mcpLoads[0] != sb.GetContainerRef()+" notion" {
		t.Errorf("runner loads = %v", env.runner.mcpLoads)
	}
	listed, _ := env.client.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	var persisted []string
	for _, x := range listed.GetSandboxes() {
		if x.GetId() == sb.GetId() {
			persisted = x.GetMcpServers()
		}
	}
	if strings.Join(persisted, ",") != "notion" {
		t.Errorf("persisted = %v", persisted)
	}

	cases := []struct {
		name string
		req  *pb.AttachMcpServerRequest
		code codes.Code
	}{
		{"duplicate", &pb.AttachMcpServerRequest{SandboxId: sb.GetId(), Name: "notion"}, codes.AlreadyExists},
		{"empty name", &pb.AttachMcpServerRequest{SandboxId: sb.GetId(), Name: " "}, codes.InvalidArgument},
		{"unknown sandbox", &pb.AttachMcpServerRequest{SandboxId: "nope", Name: "notion"}, codes.NotFound},
	}
	for _, c := range cases {
		_, err := env.client.AttachMcpServer(ctx, c.req)
		if status.Code(err) != c.code {
			t.Errorf("%s: code = %v (%v), want %v", c.name, status.Code(err), err, c.code)
		}
	}
	if len(env.runner.mcpLoads) != 1 {
		t.Errorf("refused attaches must not load; loads = %v", env.runner.mcpLoads)
	}

	// The runtime's "is not registered" diagnostic maps to NOT_FOUND.
	env.runner.loadErr = map[string]error{"ghost": errors.New(`error: add "ghost" to sandbox "x": add MCP gateway server: request failed: 404 Not Found: MCP server "ghost" is not registered (run 'sbx mcp add ghost ...')`)}
	_, err = env.client.AttachMcpServer(ctx, &pb.AttachMcpServerRequest{SandboxId: sb.GetId(), Name: "ghost"})
	if status.Code(err) != codes.NotFound || !strings.Contains(err.Error(), "is not registered") {
		t.Errorf("unregistered: %v", err)
	}

	// Gateway unavailable on this host ⇒ FAILED_PRECONDITION with the reason.
	down := startMcpServer(t, mcp.Availability{Available: false, Reason: "sbx mcp is not usable here"})
	_, err = down.client.AttachMcpServer(ctx, &pb.AttachMcpServerRequest{SandboxId: "x", Name: "notion"})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "not usable") {
		t.Errorf("unavailable: %v", err)
	}
}

// DetachMcpServer (SWI-3): unloads a server from an existing sandbox, drops it
// from the record, and maps the failure modes to codes.
func TestDetachMcpServerRPC(t *testing.T) {
	env := startMcpServer(t, available)
	ctx := context.Background()
	sb := launchViaRPC(t, env, "plain")
	for _, n := range []string{"notion", "linear"} {
		if _, err := env.client.AttachMcpServer(ctx, &pb.AttachMcpServerRequest{SandboxId: sb.GetId(), Name: n}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := env.client.DetachMcpServer(ctx, &pb.DetachMcpServerRequest{SandboxId: sb.GetId(), Name: "notion"})
	if err != nil {
		t.Fatalf("DetachMcpServer: %v", err)
	}
	if strings.Join(out.GetMcpServers(), ",") != "linear" {
		t.Errorf("record = %v, want [linear]", out.GetMcpServers())
	}
	if len(env.runner.mcpUnloads) != 1 || env.runner.mcpUnloads[0] != sb.GetContainerRef()+" notion" {
		t.Errorf("runner unloads = %v", env.runner.mcpUnloads)
	}

	cases := []struct {
		name string
		req  *pb.DetachMcpServerRequest
		code codes.Code
	}{
		{"not attached", &pb.DetachMcpServerRequest{SandboxId: sb.GetId(), Name: "notion"}, codes.NotFound},
		{"empty name", &pb.DetachMcpServerRequest{SandboxId: sb.GetId(), Name: " "}, codes.InvalidArgument},
		{"orchestrator server", &pb.DetachMcpServerRequest{SandboxId: sb.GetId(), Name: mcp.WorkerServerPrefix + "abcd1234"}, codes.InvalidArgument},
		{"unknown sandbox", &pb.DetachMcpServerRequest{SandboxId: "nope", Name: "linear"}, codes.NotFound},
	}
	for _, c := range cases {
		if _, err := env.client.DetachMcpServer(ctx, c.req); status.Code(err) != c.code {
			t.Errorf("%s: code = %v (%v), want %v", c.name, status.Code(err), err, c.code)
		}
	}
	if len(env.runner.mcpUnloads) != 1 {
		t.Errorf("refused detaches must not unload; unloads = %v", env.runner.mcpUnloads)
	}

	down := startMcpServer(t, mcp.Availability{Available: false, Reason: "sbx mcp is not usable here"})
	if _, err := down.client.DetachMcpServer(ctx, &pb.DetachMcpServerRequest{SandboxId: "x", Name: "notion"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("unavailable: %v", err)
	}
}
