package client

import (
	"context"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Secret-store wrappers (feature 008, FR-104). Each daemon manages its own
// host's `sbx secret` store; these calls act on the daemon behind this Conn.

// SecretsAvailable reports whether the daemon's host can manage secrets: the
// sandbox CLI must be present and meet the runtime baseline (the probe's reason
// is reused when it does not).
func (c *Conn) SecretsAvailable() (available bool, reason string) {
	if c.Info.GetRuntimeBaselineMet() {
		return true, ""
	}
	reason = c.Info.GetMcpGatewayReason()
	if reason == "" {
		reason = "the host's sandbox CLI is missing or below the runtime baseline"
	}
	return false, reason
}

func (c *Conn) ListSecrets(ctx context.Context) ([]*pb.Secret, error) {
	resp, err := c.api.ListSecrets(ctx, &pb.ListSecretsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetSecrets(), nil
}

func (c *Conn) SetSecret(ctx context.Context, req *pb.SetSecretRequest) (*pb.Secret, error) {
	return c.api.SetSecret(ctx, req)
}

func (c *Conn) RemoveSecret(ctx context.Context, req *pb.RemoveSecretRequest) error {
	_, err := c.api.RemoveSecret(ctx, req)
	return err
}
