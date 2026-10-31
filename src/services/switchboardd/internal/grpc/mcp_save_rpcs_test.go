package grpc_test

import (
	"context"
	"os"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func refusalOf(err error) pb.McpSaveRefusal_Reason {
	st, _ := status.FromError(err)
	for _, d := range st.Details() {
		if r, ok := d.(*pb.McpSaveRefusal); ok {
			return r.GetReason()
		}
	}
	return pb.McpSaveRefusal_REASON_UNSPECIFIED
}

func TestSaveMcpServerRPCs(t *testing.T) {
	env := startMcpServer(t, available)
	ctx := context.Background()
	info, err := env.client.GetDaemonInfo(ctx, &pb.GetDaemonInfoRequest{})
	if err != nil || !hasString(info.GetCapabilities(), sbgrpc.CapabilityMcpEdit) {
		t.Fatalf("capabilities = %v, %v", info.GetCapabilities(), err)
	}

	// A new server through SaveMcpServer: the secret goes to stdin, never argv.
	out, err := env.client.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{
		Name: "gh", IsNew: true,
		Definition:   &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://x/mcp", Headers: []string{"Authorization: Bearer ${k}"}},
		SecretValues: map[string]string{"mcp:gh:k": "rpc-secret-value"},
	})
	if err != nil || out.GetState() != pb.McpSaveOutcome_REGISTERED {
		t.Fatalf("save = %v, %v", out, err)
	}
	b, _ := os.ReadFile(env.sbxLog)
	log := string(b)
	if strings.Contains(log, "rpc-secret-value") || !strings.Contains(log, "secret set mcp:gh:k --force") {
		t.Errorf("argv log = %s", log)
	}

	// Invalid arguments and a refusal carry their codes; GONE is NOT_FOUND with the detail.
	_, err = env.client.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "x", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://x", Headers: []string{"X-Api-Key: abc"}}})
	if status.Code(err) != codes.InvalidArgument || strings.Contains(err.Error(), "abc") {
		t.Errorf("literal = %v", err)
	}
	_, err = env.client.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "notion", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://x"}})
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("re-add = %v", err)
	}
	_, err = env.client.GetMcpServerDefinition(ctx, &pb.GetMcpServerDefinitionRequest{Name: "ghost"})
	if status.Code(err) != codes.NotFound || refusalOf(err) != pb.McpSaveRefusal_GONE {
		t.Errorf("gone = %v", err)
	}
	v, err := env.client.GetMcpServerDefinition(ctx, &pb.GetMcpServerDefinitionRequest{Name: "notion"})
	if err != nil || v.GetFingerprint() == "" {
		t.Fatalf("definition = %v, %v", v, err)
	}
	_, err = env.client.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "notion", Definition: v.GetDefinition(), AttachByDefault: true, Fingerprint: "bogus"})
	if status.Code(err) != codes.FailedPrecondition || refusalOf(err) == pb.McpSaveRefusal_REASON_UNSPECIFIED {
		t.Errorf("stale = %v", err)
	}
	out, err = env.client.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "notion", Definition: v.GetDefinition(), AttachByDefault: true, Fingerprint: v.GetFingerprint()})
	if err != nil || out.GetState() != pb.McpSaveOutcome_UNCHANGED_REGISTRATION {
		t.Errorf("mark-only = %v, %v", out, err)
	}
	list, err := env.client.ListMcpServers(ctx, &pb.ListMcpServersRequest{})
	if err != nil || !list.GetServers()[0].GetAttachByDefault() || len(list.GetPendingReplaces()) != 0 || list.GetSettings().GetDefinitions() != nil {
		t.Errorf("list = %v, %v", list, err)
	}
	// Cancel without a pending replace: PENDING_ENDED. Remove secrets of another server: INVALID_ARGUMENT.
	_, err = env.client.CancelMcpPendingReplace(ctx, &pb.CancelMcpPendingReplaceRequest{Name: "notion"})
	if refusalOf(err) != pb.McpSaveRefusal_PENDING_ENDED {
		t.Errorf("cancel = %v", err)
	}
	_, err = env.client.RemoveMcpServerSecrets(ctx, &pb.RemoveMcpServerSecretsRequest{Name: "notion", Secrets: []string{"github"}})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("remove secrets = %v", err)
	}
	resp, err := env.client.RemoveMcpServerSecrets(ctx, &pb.RemoveMcpServerSecretsRequest{Name: "notion", Secrets: []string{"mcp:notion:k"}})
	if err != nil || len(resp.GetRemoved()) != 1 {
		t.Errorf("remove secrets = %v, %v", resp, err)
	}
	// Old clients: AddMcpServer accepts a literal and records it redacted.
	if _, err := env.client.AddMcpServer(ctx, &pb.AddMcpServerRequest{Name: "old", Definition: &pb.AddMcpServerRequest_Url{Url: "https://o"}, Headers: []string{"X-Api-Key: abc"}}); err != nil {
		t.Errorf("old add = %v", err)
	}
}

func TestMcpSaveRPCsRefusedWhenGatewayUnavailable(t *testing.T) {
	env := startMcpServer(t, mcp.Availability{Available: false, Reason: "not signed in", SbxVersion: "0.46.0", BaselineMet: true})
	ctx := context.Background()
	for name, err := range map[string]error{
		"get":    second(env.client.GetMcpServerDefinition(ctx, &pb.GetMcpServerDefinitionRequest{Name: "x"})),
		"save":   second(env.client.SaveMcpServer(ctx, &pb.SaveMcpServerRequest{Name: "x"})),
		"cancel": second(env.client.CancelMcpPendingReplace(ctx, &pb.CancelMcpPendingReplaceRequest{Name: "x"})),
		"remove": second(env.client.RemoveMcpServerSecrets(ctx, &pb.RemoveMcpServerSecretsRequest{Name: "x"})),
	} {
		if status.Code(err) != codes.FailedPrecondition {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
