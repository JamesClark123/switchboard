package grpc

import (
	"context"
	"errors"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/secret"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Secret RPCs (feature 008, FR-104): the host's `sbx secret` store, managed per
// daemon like the gateway. Values are handled only inside the request and the
// manager's stdin pipe; nothing here logs them.

// requireSecrets answers FAILED_PRECONDITION when no manager is wired or the
// host's sandbox CLI is missing or below the runtime baseline (FR-096).
func (s *Server) requireSecrets() error {
	if s.secrets == nil {
		return status.Error(codes.FailedPrecondition, "secret manager is not configured on this daemon")
	}
	if !s.mcpAvail.BaselineMet {
		reason := s.mcpAvail.Reason
		if reason == "" {
			reason = "the host's sandbox CLI is missing or below the runtime baseline"
		}
		return status.Error(codes.FailedPrecondition, reason)
	}
	return nil
}

// secretStatus maps manager errors to codes; runtime diagnostics pass verbatim.
func secretStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, secret.ErrInvalidArgs):
		return status.Error(codes.InvalidArgument, err.Error())
	case secret.LooksNotFound(err.Error()):
		return status.Error(codes.NotFound, err.Error())
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Unknown, err.Error())
}

func (s *Server) ListSecrets(ctx context.Context, _ *pb.ListSecretsRequest) (*pb.ListSecretsResponse, error) {
	if err := s.requireSecrets(); err != nil {
		return nil, err
	}
	secrets, err := s.secrets.List(ctx)
	if err != nil {
		return nil, secretStatus(err)
	}
	return &pb.ListSecretsResponse{Secrets: secrets}, nil
}

func (s *Server) SetSecret(ctx context.Context, req *pb.SetSecretRequest) (*pb.Secret, error) {
	if err := s.requireSecrets(); err != nil {
		return nil, err
	}
	out, err := s.secrets.Set(ctx, req)
	return out, secretStatus(err)
}

func (s *Server) RemoveSecret(ctx context.Context, req *pb.RemoveSecretRequest) (*pb.RemoveSecretResponse, error) {
	if err := s.requireSecrets(); err != nil {
		return nil, err
	}
	if err := s.secrets.Remove(ctx, req); err != nil {
		return nil, secretStatus(err)
	}
	return &pb.RemoveSecretResponse{}, nil
}
