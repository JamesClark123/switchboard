package grpc

import (
	"fmt"
	"testing"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SWI-2: a tool-name collision on attach is FAILED_PRECONDITION and keeps the
// hint + runtime text in the message.
func TestAttachMcpStatusMapsToolCollisionToFailedPrecondition(t *testing.T) {
	err := fmt.Errorf("%w: tool name collision with github (list_issues); filter tools (runtime: boom)", sandbox.ErrMcpToolCollision)
	st := status.Convert(attachMcpStatus(err))
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", st.Code())
	}
	if want := err.Error(); st.Message() != want {
		t.Errorf("message = %q, want %q", st.Message(), want)
	}
}
