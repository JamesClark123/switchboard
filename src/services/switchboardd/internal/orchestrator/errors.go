package orchestrator

import (
	"errors"
	"fmt"
)

// Sentinel errors the gRPC layer maps to status codes (tasks T013):
//
//	ErrNotFound          → NOT_FOUND
//	ErrNameTaken         → ALREADY_EXISTS
//	ErrAlreadyControlled → ALREADY_EXISTS (names the current controller)
//	ErrInvalid           → INVALID_ARGUMENT
//	ErrPrecondition      → FAILED_PRECONDITION
//	ErrUnreachable       → UNAVAILABLE (carries ssh's stderr tail)
//	ErrForbidden         → PERMISSION_DENIED
//	ErrAborted           → ABORTED (SWI-18 FR-307a: the editor's view is stale)
//	ErrExhausted         → RESOURCE_EXHAUSTED (SWI-18: too many attach tickets)
var (
	ErrNotFound          = errors.New("not found")
	ErrNameTaken         = errors.New("name already taken")
	ErrAlreadyControlled = errors.New("sandbox is already controlled")
	ErrInvalid           = errors.New("invalid")
	ErrPrecondition      = errors.New("precondition failed")
	ErrUnreachable       = errors.New("unreachable")
	ErrForbidden         = errors.New("forbidden")
	ErrAborted           = errors.New("aborted")
	ErrExhausted         = errors.New("resource exhausted")
)

// AlreadyControlledError names the sandbox's current controller (FR-108). It
// wraps ErrAlreadyControlled so errors.Is keeps working.
type AlreadyControlledError struct{ Name string }

func (e *AlreadyControlledError) Error() string {
	return fmt.Sprintf("sandbox is controlled by %s", e.Name)
}

func (e *AlreadyControlledError) Is(target error) bool { return target == ErrAlreadyControlled }

// UnreachableError carries the host and the dial's last words (FR-106 edge
// case: the TUI shows ssh's error verbatim). It wraps ErrUnreachable.
type UnreachableError struct {
	Host string
	Err  string
}

func (e *UnreachableError) Error() string {
	if e.Err == "" {
		return fmt.Sprintf("host %s is unreachable", e.Host)
	}
	return fmt.Sprintf("host %s is unreachable: %s", e.Host, e.Err)
}

func (e *UnreachableError) Is(target error) bool { return target == ErrUnreachable }
