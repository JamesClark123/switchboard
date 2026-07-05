package grpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
)

// logf is the sink debug interceptors write to (a *log.Logger.Printf in
// production; a capturing func in tests).
type logf func(format string, args ...any)

// newDebugInterceptors returns unary + stream server interceptors that log every
// RPC action and its outcome (success or error, with duration) to lg. They are
// installed only when `sxbd serve --debug` is set.
func newDebugInterceptors(lg logf) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		lg("→ %s", info.FullMethod)
		resp, err := handler(ctx, req)
		lg("%s", rpcResult(info.FullMethod, time.Since(start), err))
		return resp, err
	}
	stream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		lg("→ %s (stream)", info.FullMethod)
		err := handler(srv, ss)
		lg("%s", rpcResult(info.FullMethod+" (stream)", time.Since(start), err))
		return err
	}
	return unary, stream
}

// rpcResult formats the completion line for an RPC: a check for success, a cross
// plus the error for a failure.
func rpcResult(method string, dur time.Duration, err error) string {
	if err != nil {
		return fmt.Sprintf("✗ %s (%s): %v", method, dur.Round(time.Millisecond), err)
	}
	return fmt.Sprintf("✓ %s (%s)", method, dur.Round(time.Millisecond))
}
