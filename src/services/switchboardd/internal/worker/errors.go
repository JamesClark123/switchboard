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
//	ErrStepSent          → ALREADY_EXISTS (DeliverWorkerStep: the step was typed before, SWI-17)
//	ErrUnsupported       → UNIMPLEMENTED (an older daemon has no DeliverWorkerStep, SWI-17)
var (
	ErrNotFound          = errors.New("not found")
	ErrAlreadyControlled = errors.New("sandbox is already controlled")
	ErrInvalid           = errors.New("invalid")
	ErrPrecondition      = errors.New("precondition failed")
	ErrForbidden         = errors.New("forbidden")
	// ErrStepSent: a step with this send number was already typed for the run
	// (guard 3a). The caller reads controller.run.step_sent_at from the
	// sandbox record: a gRPC status carries no timestamp.
	ErrStepSent = errors.New("step already sent")
	// ErrUnsupported: the worker's daemon predates DeliverWorkerStep.
	ErrUnsupported = errors.New("step delivery not supported by this daemon")
)

// AlreadyControlledError names the sandbox's current controller (FR-108).
type AlreadyControlledError struct{ Name string }

func (e *AlreadyControlledError) Error() string {
	return fmt.Sprintf("sandbox is controlled by %s", e.Name)
}

func (e *AlreadyControlledError) Is(target error) bool { return target == ErrAlreadyControlled }
