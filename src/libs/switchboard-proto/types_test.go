package switchboardproto

import (
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestSandboxStateLabel(t *testing.T) {
	cases := map[pb.SandboxState]string{
		pb.SandboxState_SANDBOX_STATE_CREATING:    "creating",
		pb.SandboxState_SANDBOX_STATE_RUNNING:     "running",
		pb.SandboxState_SANDBOX_STATE_STOPPED:     "stopped",
		pb.SandboxState_SANDBOX_STATE_DESTROYING:  "destroying",
		pb.SandboxState_SANDBOX_STATE_ERROR:       "error",
		pb.SandboxState_SANDBOX_STATE_UNSPECIFIED: "unknown",
	}
	for st, want := range cases {
		if got := SandboxStateLabel(st); got != want {
			t.Errorf("SandboxStateLabel(%v) = %q, want %q", st, got, want)
		}
	}
}

func TestAgentStatusLabel(t *testing.T) {
	cases := map[pb.AgentStatus]string{
		pb.AgentStatus_AGENT_STATUS_IDLE:        "idle",
		pb.AgentStatus_AGENT_STATUS_WORKING:     "working",
		pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT: "needs-input",
		pb.AgentStatus_AGENT_STATUS_EXITED:      "exited",
		pb.AgentStatus_AGENT_STATUS_UNSPECIFIED: "unknown",
	}
	for st, want := range cases {
		if got := AgentStatusLabel(st); got != want {
			t.Errorf("AgentStatusLabel(%v) = %q, want %q", st, got, want)
		}
	}
}

func TestSeedingModeRoundTrip(t *testing.T) {
	if SeedingModeFromString("clone") != pb.SeedingMode_SEEDING_MODE_CLONE {
		t.Error("clone should map to CLONE")
	}
	if SeedingModeFromString("duplicate") != pb.SeedingMode_SEEDING_MODE_DUPLICATE {
		t.Error("duplicate should map to DUPLICATE")
	}
	if SeedingModeFromString("") != pb.SeedingMode_SEEDING_MODE_DUPLICATE {
		t.Error("empty should default to DUPLICATE")
	}
	if SeedingModeString(pb.SeedingMode_SEEDING_MODE_CLONE) != "clone" {
		t.Error("CLONE should render as clone")
	}
	if SeedingModeString(pb.SeedingMode_SEEDING_MODE_DUPLICATE) != "duplicate" {
		t.Error("DUPLICATE should render as duplicate")
	}
}

func TestEncodeDecodeOptionValue(t *testing.T) {
	enc, err := EncodeOptionValue(map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecodeOptionValue(enc)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := dec.(map[string]any)
	if !ok || m["k"] != "v" {
		t.Errorf("round-trip failed: %#v", dec)
	}
	if _, err := DecodeOptionValue("{not json"); err == nil {
		t.Error("expected decode error on bad JSON")
	}
}
