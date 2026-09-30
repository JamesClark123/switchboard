package grpc

import (
	"errors"
	"testing"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sourcesStatus maps every manager sentinel onto its documented code, passes an
// existing status through untouched, and leaves unknown errors alone.
func TestSourcesStatusMapping(t *testing.T) {
	cases := []struct {
		err  error
		want codes.Code
	}{
		{registry.ErrNotFound, codes.NotFound},
		{sandbox.ErrSourceNotRecorded, codes.NotFound},
		{sandbox.ErrInvalidSource, codes.InvalidArgument},
		{sandbox.ErrSourceExists, codes.AlreadyExists},
		{sandbox.ErrBusy, codes.FailedPrecondition},
		{sandbox.ErrIneligibleState, codes.FailedPrecondition},
		{sandbox.ErrNotRepo, codes.FailedPrecondition},
		{sandbox.ErrLastSource, codes.FailedPrecondition},
	}
	for _, tc := range cases {
		wrapped := errors.Join(errors.New("context"), tc.err)
		if got := status.Code(sourcesStatus(wrapped)); got != tc.want {
			t.Errorf("sourcesStatus(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	// An error that already carries a status is returned as-is.
	pre := status.Error(codes.PermissionDenied, "no")
	if got := sourcesStatus(pre); got != pre {
		t.Errorf("a status error must pass through unchanged, got %v", got)
	}
	// An unmapped error is left alone (gRPC reports it as Unknown).
	plain := errors.New("disk on fire")
	if got := sourcesStatus(plain); got != plain {
		t.Errorf("an unmapped error must pass through unchanged, got %v", got)
	}
}
