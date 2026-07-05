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
	case pb.AgentStatus_AGENT_STATUS_EXITED:
		return "exited"
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
