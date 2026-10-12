package grpc_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Secret RPCs (FR-104) drive the stub `sbx secret` verbs: list, set (value on
// stdin, never in argv), remove, and the code mapping.
func TestSecretRPCsRoundTrip(t *testing.T) {
	env := startMcpServer(t, available)
	ctx := context.Background()

	list, err := env.client.ListSecrets(ctx, &pb.ListSecretsRequest{})
	if err != nil || len(list.GetSecrets()) != 3 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if s := list.GetSecrets()[1]; s.GetScope() != "my-sb" || s.GetName() != "anthropic" || s.GetType() != "service" {
		t.Errorf("sandbox row = %+v", s)
	}

	set, err := env.client.SetSecret(ctx, &pb.SetSecretRequest{Name: "github", Value: "gho_plain"})
	if err != nil || set.GetName() != "github" || set.GetScope() != "global" || set.GetMaskedValue() != "gho_GCaw4o****...****43qy" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	stdin, _ := os.ReadFile(filepath.Join(filepath.Dir(env.sbxLog), "secret.stdin"))
	if string(stdin) != "gho_plain\n" {
		t.Errorf("stdin = %q", stdin)
	}
	b, _ := os.ReadFile(env.sbxLog)
	if !strings.Contains(string(b), "secret set github --force") || strings.Contains(string(b), "gho_plain") {
		t.Errorf("sbx log should carry the argv but never the value:\n%s", b)
	}

	if _, err := env.client.RemoveSecret(ctx, &pb.RemoveSecretRequest{Name: "anthropic", Sandbox: "my-sb"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	b, _ = os.ReadFile(env.sbxLog)
	if !strings.Contains(string(b), "secret rm anthropic --sandbox my-sb -f") {
		t.Errorf("remove argv missing:\n%s", b)
	}

	cases := []struct {
		name string
		call func() error
		code codes.Code
	}{
		{"bad name", func() error {
			_, e := env.client.SetSecret(ctx, &pb.SetSecretRequest{Name: "bad name", Value: "x"})
			return e
		}, codes.InvalidArgument},
		{"empty value", func() error { _, e := env.client.SetSecret(ctx, &pb.SetSecretRequest{Name: "github"}); return e }, codes.InvalidArgument},
		{"custom remove", func() error {
			_, e := env.client.RemoveSecret(ctx, &pb.RemoveSecretRequest{Name: "x", Type: "custom"})
			return e
		}, codes.InvalidArgument},
		{"unknown", func() error { _, e := env.client.RemoveSecret(ctx, &pb.RemoveSecretRequest{Name: "ghost"}); return e }, codes.NotFound},
	}
	for _, c := range cases {
		if err := c.call(); status.Code(err) != c.code {
			t.Errorf("%s: code = %v (%v), want %v", c.name, status.Code(err), err, c.code)
		}
	}
}

// Below the runtime baseline every secret RPC is refused with the reason.
func TestSecretRPCsGateOnRuntimeBaseline(t *testing.T) {
	env := startMcpServer(t, mcp.Availability{Available: false, Reason: "sbx 0.30.0 is below the required 0.36.0", BaselineMet: false})
	ctx := context.Background()
	calls := []func() error{
		func() error { _, e := env.client.ListSecrets(ctx, &pb.ListSecretsRequest{}); return e },
		func() error {
			_, e := env.client.SetSecret(ctx, &pb.SetSecretRequest{Name: "github", Value: "x"})
			return e
		},
		func() error { _, e := env.client.RemoveSecret(ctx, &pb.RemoveSecretRequest{Name: "github"}); return e },
	}
	for i, call := range calls {
		if err := call(); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "below the required") {
			t.Errorf("call %d: %v", i, err)
		}
	}
}
