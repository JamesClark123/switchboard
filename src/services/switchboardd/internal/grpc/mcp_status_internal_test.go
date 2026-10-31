package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMcpStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason pb.McpSaveRefusal_Reason
	}{
		{mcp.ErrBeingReplaced, codes.FailedPrecondition, pb.McpSaveRefusal_BEING_REPLACED},
		{&mcp.Refusal{Reason: pb.McpSaveRefusal_GONE, Msg: "gone"}, codes.NotFound, pb.McpSaveRefusal_GONE},
		{fmt.Errorf("%w: busy", mcp.ErrAuthInProgress), codes.FailedPrecondition, pb.McpSaveRefusal_AUTH_IN_PROGRESS},
		{fmt.Errorf("%w: retrying", mcp.ErrUnavailable), codes.Unavailable, 0},
		{mcp.ErrAlreadyRegistered, codes.AlreadyExists, 0},
		{mcp.ErrConfirmRequired, codes.FailedPrecondition, 0},
		{mcp.ErrSecretsUnavailable, codes.FailedPrecondition, 0},
		{context.Canceled, codes.Canceled, 0},
		{context.DeadlineExceeded, codes.DeadlineExceeded, 0},
		{mcp.ErrNotRegistered, codes.NotFound, 0},
		{mcp.ErrInvalidArgs, codes.InvalidArgument, 0},
		{mcp.ErrLocalNotAcknowledged, codes.FailedPrecondition, 0},
		{status.Error(codes.Aborted, "x"), codes.Aborted, 0},
		{errors.New("host said no"), codes.Unknown, 0},
	} {
		err := mcpStatus(tc.err)
		st, _ := status.FromError(err)
		if st.Code() != tc.code {
			t.Errorf("%v: code %v, want %v", tc.err, st.Code(), tc.code)
		}
		var got pb.McpSaveRefusal_Reason
		for _, d := range st.Details() {
			if r, ok := d.(*pb.McpSaveRefusal); ok {
				got = r.GetReason()
			}
		}
		if got != tc.reason {
			t.Errorf("%v: detail %v, want %v", tc.err, got, tc.reason)
		}
	}
	if mcpStatus(nil) != nil {
		t.Error("nil")
	}
	if st, _ := status.FromError(mcpStatus(fmt.Errorf("%w: retrying", mcp.ErrUnavailable))); st.Message() != "retrying" {
		t.Errorf("unavailable message = %q", st.Message())
	}
}
