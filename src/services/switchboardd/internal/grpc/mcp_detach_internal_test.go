package grpc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PR #3 review: every sentinel the detach/attach paths can return has a code.
func TestDetachAndAttachMcpStatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		detach codes.Code
		attach codes.Code
	}{
		{"busy", fmt.Errorf("wrap: %w", sandbox.ErrBusy), codes.FailedPrecondition, codes.FailedPrecondition},
		{"ineligible", fmt.Errorf("%w: stopping", sandbox.ErrIneligibleState), codes.FailedPrecondition, codes.FailedPrecondition},
		{"not attached", fmt.Errorf("%w: x", sandbox.ErrMcpNotAttached), codes.NotFound, codes.Unknown},
		{"reserved", fmt.Errorf("%w: sxb-orch-x", sandbox.ErrMcpReserved), codes.InvalidArgument, codes.Unknown},
		{"invalid name", fmt.Errorf("%w: \"-x\"", sandbox.ErrMcpInvalidName), codes.InvalidArgument, codes.InvalidArgument},
		{"runtime text", errors.New("gateway unreachable"), codes.Unknown, codes.Unknown},
		// The sentinel's message quotes the name; text matching must not reinterpret it.
		{"invalid name quoting 'unknown'", fmt.Errorf("%w: \"-unknown server\"", sandbox.ErrMcpInvalidName), codes.InvalidArgument, codes.InvalidArgument},
		{"invalid name quoting 'not found'", fmt.Errorf("%w: \"-not found\"", sandbox.ErrMcpInvalidName), codes.InvalidArgument, codes.InvalidArgument},
		{"runtime 'is not registered'", errors.New(`MCP server "x" is not registered`), codes.Unknown, codes.NotFound},
	}
	for _, c := range cases {
		if got := status.Code(detachMcpStatus(c.err)); got != c.detach {
			t.Errorf("detach %s: code = %v, want %v", c.name, got, c.detach)
		}
		if got := status.Code(attachMcpStatus(c.err)); got != c.attach {
			t.Errorf("attach %s: code = %v, want %v", c.name, got, c.attach)
		}
	}
}
