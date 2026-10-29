// Package switchboardproto exposes the generated gRPC contract (sub-package gen)
// plus small domain-type helpers shared by the daemon and the TUI client.
//
// Per the Repository Structure rule, this `libs` module imports no other
// switchboard category; only the generated protobuf types and the standard
// library are referenced here.
package switchboardproto

import (
	"encoding/json"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// SandboxStateLabel returns a short human label for a SandboxState, used by the
// TUI list and by log lines. Unknown values render as "unknown".
func SandboxStateLabel(s pb.SandboxState) string {
	switch s {
	case pb.SandboxState_SANDBOX_STATE_CREATING:
		return "creating"
	case pb.SandboxState_SANDBOX_STATE_RUNNING:
		return "running"
	case pb.SandboxState_SANDBOX_STATE_STOPPED:
		return "stopped"
	case pb.SandboxState_SANDBOX_STATE_DESTROYING:
		return "destroying"
	case pb.SandboxState_SANDBOX_STATE_ERROR:
		return "error"
	default:
		return "unknown"
	}
}

// AgentStatusLabel returns a short human label for an AgentStatus.
func AgentStatusLabel(s pb.AgentStatus) string {
	switch s {
	case pb.AgentStatus_AGENT_STATUS_IDLE:
		return "idle"
	case pb.AgentStatus_AGENT_STATUS_WORKING:
		return "working"
	case pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT:
		return "needs-input"
	case pb.AgentStatus_AGENT_STATUS_STARTING:
		return "starting"
	case pb.AgentStatus_AGENT_STATUS_EXITED:
		return "exited"
	case pb.AgentStatus_AGENT_STATUS_WAITING:
		return "waiting"
	default:
		return "unknown"
	}
}

// --- SWI-17: action sequence labels ---

// ModelChoiceLabel returns the model family a ModelChoice names, as Claude Code
// reads it back ("Set model to Opus …", research R1); RunStep.expect carries
// it for a MODEL step.
func ModelChoiceLabel(m pb.ModelChoice) string {
	switch m {
	case pb.ModelChoice_MODEL_CHOICE_OPUS:
		return "Opus"
	case pb.ModelChoice_MODEL_CHOICE_FABLE:
		return "Fable"
	case pb.ModelChoice_MODEL_CHOICE_SONNET:
		return "Sonnet"
	case pb.ModelChoice_MODEL_CHOICE_HAIKU:
		return "Haiku"
	default:
		return "unknown"
	}
}

// EffortChoiceLabel returns the effort level an EffortChoice names, as typed
// after `/effort` and read back (research R1); RunStep.expect carries it for an
// EFFORT step.
func EffortChoiceLabel(e pb.EffortChoice) string {
	switch e {
	case pb.EffortChoice_EFFORT_CHOICE_LOW:
		return "low"
	case pb.EffortChoice_EFFORT_CHOICE_MEDIUM:
		return "medium"
	case pb.EffortChoice_EFFORT_CHOICE_HIGH:
		return "high"
	case pb.EffortChoice_EFFORT_CHOICE_XHIGH:
		return "xhigh"
	case pb.EffortChoice_EFFORT_CHOICE_MAX:
		return "max"
	case pb.EffortChoice_EFFORT_CHOICE_AUTO:
		return "auto"
	default:
		return "unknown"
	}
}

// StepRoleLabel returns a short human label for a StepRole.
func StepRoleLabel(r pb.StepRole) string {
	switch r {
	case pb.StepRole_STEP_ROLE_SETUP:
		return "setup"
	case pb.StepRole_STEP_ROLE_ACTION:
		return "action"
	case pb.StepRole_STEP_ROLE_CLEANUP:
		return "cleanup"
	default:
		return "unknown"
	}
}

// StepKindLabel returns a short human label for a StepKind; the built-ins keep
// the names the action form shows (Model, Effort, Clear).
func StepKindLabel(k pb.StepKind) string {
	switch k {
	case pb.StepKind_STEP_KIND_MODEL:
		return "Model"
	case pb.StepKind_STEP_KIND_EFFORT:
		return "Effort"
	case pb.StepKind_STEP_KIND_CLEAR:
		return "Clear"
	case pb.StepKind_STEP_KIND_CUSTOM:
		return "custom prompt"
	case pb.StepKind_STEP_KIND_ACTION_PROMPT:
		return "action prompt"
	default:
		return "unknown"
	}
}

// StepStatusLabel returns a short human label for a StepStatus.
func StepStatusLabel(s pb.StepStatus) string {
	switch s {
	case pb.StepStatus_STEP_STATUS_PENDING:
		return "pending"
	case pb.StepStatus_STEP_STATUS_SENDING:
		return "sending"
	case pb.StepStatus_STEP_STATUS_SENT:
		return "sent"
	case pb.StepStatus_STEP_STATUS_TURN:
		return "turn"
	case pb.StepStatus_STEP_STATUS_HALTED:
		return "halted"
	case pb.StepStatus_STEP_STATUS_AWAIT_CLOSE:
		return "awaiting close"
	case pb.StepStatus_STEP_STATUS_DONE:
		return "done"
	default:
		return "unknown"
	}
}

// StepObservedLabel returns a short human label for a StepObserved.
func StepObservedLabel(o pb.StepObserved) string {
	switch o {
	case pb.StepObserved_STEP_OBSERVED_MODEL_TURN:
		return "model turn"
	case pb.StepObserved_STEP_OBSERVED_MODEL_TURN_INPUT_ONLY:
		return "model turn (input only)"
	case pb.StepObserved_STEP_OBSERVED_AGENT_LOCAL:
		return "agent-local"
	default:
		return "unknown"
	}
}

// WorkerHoldLabel returns a short human label for a WorkerHold, as the sandbox
// rows phrase it (contracts/jobs-ui.md).
func WorkerHoldLabel(h pb.WorkerHold) string {
	switch h {
	case pb.WorkerHold_WORKER_HOLD_NONE:
		return "none"
	case pb.WorkerHold_WORKER_HOLD_CLEANING_UP:
		return "cleaning up"
	case pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING:
		return "needs input (freeing)"
	case pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER:
		return "needing the developer"
	default:
		return "unknown"
	}
}

// StepOutcomeLabel returns a short human label for a StepOutcome.
func StepOutcomeLabel(o pb.StepOutcome) string {
	switch o {
	case pb.StepOutcome_STEP_OUTCOME_MODEL_TURN:
		return "model turn"
	case pb.StepOutcome_STEP_OUTCOME_MODEL_TURN_INPUT_ONLY:
		return "model turn (input only)"
	case pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL:
		return "agent-local"
	case pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED:
		return "not accepted"
	case pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED:
		return "unconfirmed"
	default:
		return "unknown"
	}
}

// StepResolutionLabel returns a short human label for a StepResolution.
func StepResolutionLabel(r pb.StepResolution) string {
	switch r {
	case pb.StepResolution_STEP_RESOLUTION_RETRY:
		return "retry"
	case pb.StepResolution_STEP_RESOLUTION_SKIP:
		return "skip"
	case pb.StepResolution_STEP_RESOLUTION_FAIL:
		return "fail"
	default:
		return "unknown"
	}
}

// AgentCheckStateLabel returns a short human label for an AgentCheck.State.
func AgentCheckStateLabel(s pb.AgentCheck_State) string {
	switch s {
	case pb.AgentCheck_STATE_CLEAR:
		return "clear"
	case pb.AgentCheck_STATE_UNKNOWN:
		return "checking"
	case pb.AgentCheck_STATE_DETACHED:
		return "detached"
	case pb.AgentCheck_STATE_CHECK_FAILED:
		return "check failed"
	default:
		return "unknown"
	}
}

// SeedingModeFromString maps the human seeding-mode tokens used in client TOML
// ("duplicate"/"clone") to the proto enum. Defaults to DUPLICATE (FR-009).
func SeedingModeFromString(s string) pb.SeedingMode {
	if s == "clone" {
		return pb.SeedingMode_SEEDING_MODE_CLONE
	}
	return pb.SeedingMode_SEEDING_MODE_DUPLICATE
}

// SeedingModeString is the inverse of SeedingModeFromString.
func SeedingModeString(m pb.SeedingMode) string {
	if m == pb.SeedingMode_SEEDING_MODE_CLONE {
		return "clone"
	}
	return "duplicate"
}

// EncodeOptionValue JSON-encodes a kit-option value for transport in
// ConfigSnapshot.kit_options (the contract carries each value as a JSON-encoded
// scalar/list so option fidelity is preserved across the wire — FR-014).
func EncodeOptionValue(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeOptionValue parses a JSON-encoded kit-option value back into a Go value.
func DecodeOptionValue(s string) (any, error) {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}
