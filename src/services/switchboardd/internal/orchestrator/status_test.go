package orchestrator

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func statusUnimplemented() error { return status.Error(codes.Unimplemented, "unknown method") }
