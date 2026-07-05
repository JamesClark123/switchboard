package grpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

func TestDebugInterceptorsLogActionsAndErrors(t *testing.T) {
	var rendered []string
	rec := func(format string, args ...any) {
		rendered = append(rendered, fmt.Sprintf(format, args...))
	}

	unary, stream := newDebugInterceptors(rec)

	// Successful unary call is logged with a check mark.
	if _, err := unary(context.Background(), "req",
		&grpc.UnaryServerInfo{FullMethod: "/sw.Switchboard/ListSandboxes"},
		func(context.Context, any) (any, error) { return "ok", nil }); err != nil {
		t.Fatalf("unary: %v", err)
	}

	// Failing unary call propagates the error and logs it.
	boom := errors.New("boom")
	if _, err := unary(context.Background(), "req",
		&grpc.UnaryServerInfo{FullMethod: "/sw.Switchboard/StopSandbox"},
		func(context.Context, any) (any, error) { return nil, boom }); err == nil {
		t.Fatal("expected error to propagate")
	}

	// Stream call is logged.
	if err := stream(nil, nil,
		&grpc.StreamServerInfo{FullMethod: "/sw.Switchboard/Subscribe"},
		func(any, grpc.ServerStream) error { return nil }); err != nil {
		t.Fatalf("stream: %v", err)
	}

	joined := strings.Join(rendered, "\n")
	for _, want := range []string{
		"→ /sw.Switchboard/ListSandboxes",
		"✓ /sw.Switchboard/ListSandboxes",
		"✗ /sw.Switchboard/StopSandbox",
		"boom",
		"Subscribe (stream)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("debug log missing %q\n--- log ---\n%s", want, joined)
		}
	}
}
