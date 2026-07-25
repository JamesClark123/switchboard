package escapehatch

import (
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestStatusLabelAndSlugCoverAllStates(t *testing.T) {
	all := []pb.EscapeHatchRunStatus{
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_TIMED_OUT,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_UNSPECIFIED,
	}
	for _, st := range all {
		if statusLabel(st) == "" {
			t.Errorf("statusLabel(%v) is empty", st)
		}
		if statusSlug(st) == "" {
			t.Errorf("statusSlug(%v) is empty", st)
		}
	}
	if statusSlug(pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL) != "pending_approval" {
		t.Error("pending_approval slug mismatch")
	}
}

func TestAppendLine(t *testing.T) {
	if got := appendLine("", "x"); got != "x" {
		t.Errorf("appendLine on empty = %q", got)
	}
	if got := appendLine("a", "b"); got != "a\nb" {
		t.Errorf("appendLine = %q", got)
	}
}

func TestServiceAccessors(t *testing.T) {
	svc, _ := serviceWith(t, t.TempDir(), shCmd("x", "true"))
	if svc.Runs() == nil {
		t.Error("Runs() should expose the store")
	}
	if svc.InFlight("sb1") != 0 {
		t.Error("InFlight should be 0 with no runs")
	}
	// Decide on an unknown run resolves to UNSPECIFIED (idempotent no-op).
	if st := svc.Decide("ehr-nope", true); st != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_UNSPECIFIED {
		t.Errorf("decide on unknown run = %v, want UNSPECIFIED", st)
	}
}

func TestRemoveBlockShapes(t *testing.T) {
	block := ruleBeginMarker + "\nrule\n" + ruleEndMarker
	// block only.
	if got := removeBlock(block); got != "" {
		t.Errorf("block-only removal = %q, want empty", got)
	}
	// content before + block.
	if got := removeBlock("intro\n\n" + block); !strings.Contains(got, "intro") || strings.Contains(got, ruleBeginMarker) {
		t.Errorf("before-only removal = %q", got)
	}
	// block + content after.
	if got := removeBlock(block + "\n\noutro"); !strings.Contains(got, "outro") || strings.Contains(got, ruleEndMarker) {
		t.Errorf("after-only removal = %q", got)
	}
	// content on both sides.
	if got := removeBlock("intro\n\n" + block + "\n\noutro"); !strings.Contains(got, "intro") || !strings.Contains(got, "outro") || strings.Contains(got, ruleBeginMarker) {
		t.Errorf("both-sides removal = %q", got)
	}
	// no block -> unchanged.
	if got := removeBlock("plain content"); got != "plain content" {
		t.Errorf("no-block content should be unchanged, got %q", got)
	}
}
