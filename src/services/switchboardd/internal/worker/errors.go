package worker

import (
	"errors"
	"fmt"
)

// Sentinel errors the gRPC layer maps to status codes (grpc.orchStatus maps
// these alongside the orchestrator package's):
//
//	ErrNotFound          → NOT_FOUND
//	ErrAlreadyControlled → ALREADY_EXISTS (AlreadyControlledError names the controller)
//	ErrInvalid           → INVALID_ARGUMENT
//	ErrPrecondition      → FAILED_PRECONDITION
//	ErrForbidden         → PERMISSION_DENIED
var (
	ErrNotFound          = errors.New("not found")
	ErrAlreadyControlled = errors.New("sandbox is already controlled")
	ErrInvalid           = errors.New("invalid")
	ErrPrecondition      = errors.New("precondition failed")
	ErrForbidden         = errors.New("forbidden")
)

// AlreadyControlledError names the sandbox's current controller (FR-108).
type AlreadyControlledError struct{ Name string }

func (e *AlreadyControlledError) Error() string {
	return fmt.Sprintf("sandbox is controlled by %s", e.Name)
}

func (e *AlreadyControlledError) Is(target error) bool { return target == ErrAlreadyControlled }
