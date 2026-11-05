package switchboardproto

import (
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
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
		pb.AgentStatus_AGENT_STATUS_WAITING:     "waiting",
		pb.AgentStatus_AGENT_STATUS_STARTING:    "starting",
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

// TestSequenceLabels covers the SWI-17 enum labels, including each enum's
// out-of-range value.
func TestSequenceLabels(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"model opus", ModelChoiceLabel(pb.ModelChoice_MODEL_CHOICE_OPUS), "Opus"},
		{"model fable", ModelChoiceLabel(pb.ModelChoice_MODEL_CHOICE_FABLE), "Fable"},
		{"model sonnet", ModelChoiceLabel(pb.ModelChoice_MODEL_CHOICE_SONNET), "Sonnet"},
		{"model haiku", ModelChoiceLabel(pb.ModelChoice_MODEL_CHOICE_HAIKU), "Haiku"},
		{"model unspecified", ModelChoiceLabel(pb.ModelChoice_MODEL_CHOICE_UNSPECIFIED), "unknown"},
		{"effort low", EffortChoiceLabel(pb.EffortChoice_EFFORT_CHOICE_LOW), "low"},
		{"effort medium", EffortChoiceLabel(pb.EffortChoice_EFFORT_CHOICE_MEDIUM), "medium"},
		{"effort high", EffortChoiceLabel(pb.EffortChoice_EFFORT_CHOICE_HIGH), "high"},
		{"effort xhigh", EffortChoiceLabel(pb.EffortChoice_EFFORT_CHOICE_XHIGH), "xhigh"},
		{"effort max", EffortChoiceLabel(pb.EffortChoice_EFFORT_CHOICE_MAX), "max"},
		{"effort auto", EffortChoiceLabel(pb.EffortChoice_EFFORT_CHOICE_AUTO), "auto"},
		{"effort unspecified", EffortChoiceLabel(pb.EffortChoice_EFFORT_CHOICE_UNSPECIFIED), "unknown"},
		{"role setup", StepRoleLabel(pb.StepRole_STEP_ROLE_SETUP), "setup"},
		{"role action", StepRoleLabel(pb.StepRole_STEP_ROLE_ACTION), "action"},
		{"role cleanup", StepRoleLabel(pb.StepRole_STEP_ROLE_CLEANUP), "cleanup"},
		{"role unspecified", StepRoleLabel(pb.StepRole_STEP_ROLE_UNSPECIFIED), "unknown"},
		{"kind model", StepKindLabel(pb.StepKind_STEP_KIND_MODEL), "Model"},
		{"kind effort", StepKindLabel(pb.StepKind_STEP_KIND_EFFORT), "Effort"},
		{"kind clear", StepKindLabel(pb.StepKind_STEP_KIND_CLEAR), "Clear"},
		{"kind custom", StepKindLabel(pb.StepKind_STEP_KIND_CUSTOM), "custom prompt"},
		{"kind action", StepKindLabel(pb.StepKind_STEP_KIND_ACTION_PROMPT), "action prompt"},
		{"kind unspecified", StepKindLabel(pb.StepKind_STEP_KIND_UNSPECIFIED), "unknown"},
		{"status pending", StepStatusLabel(pb.StepStatus_STEP_STATUS_PENDING), "pending"},
		{"status sending", StepStatusLabel(pb.StepStatus_STEP_STATUS_SENDING), "sending"},
		{"status sent", StepStatusLabel(pb.StepStatus_STEP_STATUS_SENT), "sent"},
		{"status turn", StepStatusLabel(pb.StepStatus_STEP_STATUS_TURN), "turn"},
		{"status halted", StepStatusLabel(pb.StepStatus_STEP_STATUS_HALTED), "halted"},
		{"status await close", StepStatusLabel(pb.StepStatus_STEP_STATUS_AWAIT_CLOSE), "awaiting close"},
		{"status done", StepStatusLabel(pb.StepStatus_STEP_STATUS_DONE), "done"},
		{"status unspecified", StepStatusLabel(pb.StepStatus_STEP_STATUS_UNSPECIFIED), "unknown"},
		{"observed turn", StepObservedLabel(pb.StepObserved_STEP_OBSERVED_MODEL_TURN), "model turn"},
		{"observed input only", StepObservedLabel(pb.StepObserved_STEP_OBSERVED_MODEL_TURN_INPUT_ONLY), "model turn (input only)"},
		{"observed local", StepObservedLabel(pb.StepObserved_STEP_OBSERVED_AGENT_LOCAL), "agent-local"},
		{"observed unknown", StepObservedLabel(pb.StepObserved_STEP_OBSERVED_UNKNOWN), "unknown"},
		{"hold none", WorkerHoldLabel(pb.WorkerHold_WORKER_HOLD_NONE), "none"},
		{"hold cleaning up", WorkerHoldLabel(pb.WorkerHold_WORKER_HOLD_CLEANING_UP), "cleaning up"},
		{"hold freeing", WorkerHoldLabel(pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING), "needs input (freeing)"},
		{"hold developer", WorkerHoldLabel(pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER), "needing the developer"},
		{"hold out of range", WorkerHoldLabel(pb.WorkerHold(99)), "unknown"},
		{"outcome turn", StepOutcomeLabel(pb.StepOutcome_STEP_OUTCOME_MODEL_TURN), "model turn"},
		{"outcome input only", StepOutcomeLabel(pb.StepOutcome_STEP_OUTCOME_MODEL_TURN_INPUT_ONLY), "model turn (input only)"},
		{"outcome local", StepOutcomeLabel(pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL), "agent-local"},
		{"outcome not accepted", StepOutcomeLabel(pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED), "not accepted"},
		{"outcome unconfirmed", StepOutcomeLabel(pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED), "unconfirmed"},
		{"outcome unspecified", StepOutcomeLabel(pb.StepOutcome_STEP_OUTCOME_UNSPECIFIED), "unknown"},
		{"resolution retry", StepResolutionLabel(pb.StepResolution_STEP_RESOLUTION_RETRY), "retry"},
		{"resolution skip", StepResolutionLabel(pb.StepResolution_STEP_RESOLUTION_SKIP), "skip"},
		{"resolution fail", StepResolutionLabel(pb.StepResolution_STEP_RESOLUTION_FAIL), "fail"},
		{"resolution unspecified", StepResolutionLabel(pb.StepResolution_STEP_RESOLUTION_UNSPECIFIED), "unknown"},
		{"check clear", AgentCheckStateLabel(pb.AgentCheck_STATE_CLEAR), "clear"},
		{"check unknown", AgentCheckStateLabel(pb.AgentCheck_STATE_UNKNOWN), "checking"},
		{"check detached", AgentCheckStateLabel(pb.AgentCheck_STATE_DETACHED), "detached"},
		{"check failed", AgentCheckStateLabel(pb.AgentCheck_STATE_CHECK_FAILED), "check failed"},
		{"check out of range", AgentCheckStateLabel(pb.AgentCheck_State(99)), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestDetachedDetail(t *testing.T) {
	seen := time.Date(2026, 10, 28, 10, 42, 0, 0, time.Local)
	check := &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED, LastStatus: pb.AgentStatus_AGENT_STATUS_WORKING, LastSeenAt: timestamppb.New(seen), Processes: 4}
	if got := DetachedDetail(check, time.Time{}); got != " (last: working, Oct 28 10:42, 4 procs)" {
		t.Errorf("persisted, another day = %q", got)
	}
	today := time.Now().Truncate(time.Minute)
	check.LastSeenAt = timestamppb.New(today)
	if got := DetachedDetail(check, time.Time{}); got != " (last: working, "+today.Local().Format("15:04")+", 4 procs)" {
		t.Errorf("persisted, today = %q", got)
	}
	check.LastSeenAt = timestamppb.New(seen)
	for d, want := range map[time.Duration]string{30 * time.Second: "just now", 3 * time.Minute: "3m ago", 2 * time.Hour: "2h ago", 30 * time.Hour: "Oct 28 10:42"} {
		if got := DetachedDetail(check, seen.Add(d)); got != " (last: working, "+want+", 4 procs)" {
			t.Errorf("%s: %q", d, got)
		}
	}
	one := &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED, LastStatus: pb.AgentStatus_AGENT_STATUS_IDLE, Processes: 1}
	if got := DetachedDetail(one, time.Time{}); got != " (last: idle, 1 proc)" {
		t.Errorf("one proc, no time = %q", got)
	}
	if got := DetachedDetail(&pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED}, time.Now()); got != " (last: unknown state)" {
		t.Errorf("bare = %q", got)
	}
	if DetachedDetail(&pb.AgentCheck{State: pb.AgentCheck_STATE_CHECK_FAILED, Processes: 2}, time.Time{}) != "" || DetachedDetail(nil, time.Time{}) != "" {
		t.Error("only a DETACHED check has a detail")
	}
	for s, want := range map[pb.AgentStatus]string{pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT: "waiting for input", pb.AgentStatus_AGENT_STATUS_WAITING: "waiting", pb.AgentStatus_AGENT_STATUS_STARTING: "starting", pb.AgentStatus_AGENT_STATUS_EXITED: "unknown state"} {
		if got := LastStateLabel(s); got != want {
			t.Errorf("%v = %q", s, got)
		}
	}
}

func TestCheckFailedNote(t *testing.T) {
	note := CheckFailedNote("probe: exit 1")
	if note != "can't check for a detached agent: probe: exit 1" || !IsCheckFailedNote(note) {
		t.Errorf("note = %q", note)
	}
	if IsCheckFailedNote("waiting: can't check for a detached agent") || IsCheckFailedNote("") {
		t.Error("only the prefix matches")
	}
}

func TestBulkReplaceTimeout(t *testing.T) {
	if BulkReplaceTimeout(0) != 60*time.Second || BulkReplaceTimeout(4) != 75*time.Second || BulkReplaceTimeout(5) != 90*time.Second {
		t.Errorf("timeouts: %v %v %v", BulkReplaceTimeout(0), BulkReplaceTimeout(4), BulkReplaceTimeout(5))
	}
}
