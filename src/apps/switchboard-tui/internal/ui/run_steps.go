package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	swb "github.com/jamesclark123/switchboard/libs/switchboard-proto"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A run's sequence on the TUI (SWI-17, contracts/jobs-ui.md): the step a run is
// at on its row (FR-213, SC-006), retry/skip/fail of a halted step (FR-222) and
// the steps in the prompt overlay (FR-220). The orchestrator owns the state;
// everything here reads Run.steps/step/step_status/step_note/hold as loaded.

// currentStep is the run's current step, nil when it has none (no sequence,
// or an index out of range).
func currentStep(r *pb.Run) *pb.RunStep {
	steps, i := r.GetSteps(), int(r.GetStep())
	if i < 0 || i >= len(steps) {
		return nil
	}
	return steps[i]
}

// runHalted reports a run halted at a step that was not accepted (FR-206c).
func runHalted(r *pb.Run) bool {
	return len(r.GetSteps()) > 0 && r.GetStepStatus() == pb.StepStatus_STEP_STATUS_HALTED
}

// runHoldsSandbox reports a closed run still holding its worker (cleanup, a
// hold) or halted: listed under In progress, and its terminal is the way out
// (FR-213).
func runHoldsSandbox(r *pb.Run) bool {
	return runFinished(r) && (r.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE || runHalted(r))
}

// runHasTerminal reports whether a run's worker has a terminal worth opening:
// in flight, or closed but holding it.
func runHasTerminal(r *pb.Run) bool { return runInFlight(r) || runHoldsSandbox(r) }

// stepProgress is "setting up 2/3" or "cleaning up 1/2" for a setup or cleanup
// step; "" for the action step and for a run without steps.
func stepProgress(r *pb.Run) string {
	st := currentStep(r)
	if st == nil {
		return ""
	}
	var verb string
	switch st.GetRole() {
	case pb.StepRole_STEP_ROLE_SETUP:
		verb = "setting up"
	case pb.StepRole_STEP_ROLE_CLEANUP:
		verb = "cleaning up"
	default:
		return ""
	}
	n, of := 0, 0
	for i, s := range r.GetSteps() {
		if s.GetRole() == st.GetRole() {
			of++
			if i <= int(r.GetStep()) {
				n++
			}
		}
	}
	return fmt.Sprintf("%s %d/%d", verb, n, of)
}

// stepName names the current step on a halted row: Clear by name, a setup or
// cleanup step by its progress, the action step as such.
func stepName(r *pb.Run) string {
	st := currentStep(r)
	switch {
	case st == nil:
		return "step"
	case st.GetKind() == pb.StepKind_STEP_KIND_CLEAR:
		return "Clear"
	case st.GetKind() == pb.StepKind_STEP_KIND_ACTION_PROMPT:
		return "action prompt"
	}
	return stepProgress(r)
}

// haltedMarker qualifies a halt the developer must judge before retrying.
func haltedMarker(r *pb.Run) string {
	switch {
	case r.GetStepSentBeforeRestart():
		return " (sent before a restart)"
	case r.GetStepUnconfirmed():
		return " (may have run)"
	}
	return ""
}

// runStepDetail is the step part of a run row (contracts/jobs-ui.md):
// progress, a `waiting:` note, a not-accepted halt, or a hold of a closed run.
// The note is the orchestrator's text, sanitised (FR-029). "" when the run has
// nothing to add (no steps, or the action step running normally).
func runStepDetail(r *pb.Run) string {
	if len(r.GetSteps()) == 0 {
		return ""
	}
	note := sanitizeLine(r.GetStepNote(), 60)
	if runHalted(r) {
		d := stepName(r) + " not accepted" + haltedMarker(r)
		if note != "" {
			d += ": " + note
		}
		return d
	}
	switch r.GetHold() {
	case pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING, pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER:
		return swb.WorkerHoldLabel(r.GetHold())
	}
	// A `waiting: <reason>` note follows the progress (or stands alone on the
	// action step).
	return strings.TrimSpace(phaseProgress(r) + " " + note)
}

// phaseProgress is stepProgress where the run's state agrees with it (data-model
// run state table): setup while the run is running or awaiting input, cleanup
// once it is closed. A run awaiting review is at its action step whatever its
// step index says, so it shows none.
func phaseProgress(r *pb.Run) string {
	switch currentStep(r).GetRole() {
	case pb.StepRole_STEP_ROLE_SETUP:
		if runFinished(r) || r.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW {
			return ""
		}
	case pb.StepRole_STEP_ROLE_CLEANUP:
		if !runFinished(r) {
			return ""
		}
	}
	return stepProgress(r)
}

// isRestartHalt reports a step halted by an owning-daemon restart (its agent
// was detached): R restarts setup with a fresh agent, and S is not offered
// (research R14).
func isRestartHalt(r *pb.Run) bool {
	return runHalted(r) && strings.Contains(r.GetStepNote(), detachedRefusal)
}

// retryClearHeld reports a Clear the developer failed, leaving the worker
// needing the developer: R stays on the row to retry it (FR-222).
func retryClearHeld(r *pb.Run) bool {
	st := currentStep(r)
	return st != nil && st.GetKind() == pb.StepKind_STEP_KIND_CLEAR && r.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
}

// stepSkippable reports whether S applies: not for Clear, the action step (the
// run would wait for a report that can't come) or a restart halt.
func stepSkippable(r *pb.Run) bool {
	st := currentStep(r)
	if st == nil || isRestartHalt(r) {
		return false
	}
	switch st.GetKind() {
	case pb.StepKind_STEP_KIND_CLEAR, pb.StepKind_STEP_KIND_ACTION_PROMPT:
		return false
	}
	return true
}

// mayHaveRun reports a halted step that may already have been typed: S is
// listed first and R asks first.
func mayHaveRun(r *pb.Run) bool { return r.GetStepSentBeforeRestart() || r.GetStepUnconfirmed() }

// stepKeysHelp is the R/S/X help of a run row, offered only on a halted run
// (R also on a held Clear). S comes first for a step that may have run.
func stepKeysHelp(r *pb.Run) helpBindings {
	if !runHalted(r) {
		if retryClearHeld(r) {
			return helpBindings{hkey("R", "retry Clear")}
		}
		return nil
	}
	retry := hkey("R", "retry step")
	if isRestartHalt(r) {
		retry = hkey("R", "restart setup")
	}
	var hb helpBindings
	if stepSkippable(r) {
		skip := hkey("S", "skip step")
		if mayHaveRun(r) {
			hb = helpBindings{skip, retry}
		} else {
			hb = helpBindings{retry, skip}
		}
	} else {
		hb = helpBindings{retry}
	}
	return append(hb, hkey("X", "fail run"))
}

// stepKey handles R, S and X on a run row. Each opens a confirm that defaults
// to No; X is offered only on halted runs and x only on queued ones, so a Shift
// slip opens a confirm or does nothing (contracts/jobs-ui.md).
func (m Model) stepKey(s orchSurface, t orchTarget, run *pb.Run, k string) (tea.Model, tea.Cmd) {
	op := m.opState(s)
	if !runHalted(run) && (k != "R" || !retryClearHeld(run)) {
		op.status = "R, S and X are for a run halted at a step"
		return m, nil
	}
	name := stepName(run)
	id := sanitizeLine(run.GetId(), 0)
	st := confirmState{returnTo: surfaceScreen(s), enterCancels: true}
	var res pb.StepResolution
	switch k {
	case "R":
		res = pb.StepResolution_STEP_RESOLUTION_RETRY
		switch {
		case isRestartHalt(run):
			st.title = "Restart setup of " + id + "?"
			st.body = []string{"Stop the detached agent" + m.lastSeenOf(run) + " and restart setup from the first step?"}
			st.warning = "Anything it was doing is lost."
		case mayHaveRun(run):
			st.title = "Retry " + name + " of " + id + "?"
			st.body = []string{"This step may already have run — check the worker's terminal."}
			st.warning = "Retry anyway? It may run twice."
		default:
			st.title = "Retry " + name + " of " + id + "?"
			st.body = []string{"The step is typed again once the worker is ready."}
			st.notice = "Nothing else changes."
		}
	case "S":
		if !stepSkippable(run) {
			op.status = name + " can't be skipped — retry or fail"
			if isRestartHalt(run) {
				op.status = "the agent was detached by a daemon restart — R restarts setup"
			}
			return m, nil
		}
		res = pb.StepResolution_STEP_RESOLUTION_SKIP
		st.title = "Skip " + name + " of " + id + "?"
		st.body = []string{"The run goes on with the next step."}
		st.warning = "The skipped step is not sent."
	case "X":
		res = pb.StepResolution_STEP_RESOLUTION_FAIL
		st.title = "Fail " + id + " at " + name + "?"
		st.body = []string{failConsequence(run)}
		st.warning = "The run fails; this cannot be undone."
	}
	od, ok := m.requireDaemon(s, t.host)
	if !ok {
		return m, nil
	}
	orchID, runID, step := t.id, run.GetId(), run.GetStep()
	verb := swb.StepResolutionLabel(res)
	st.onConfirm = func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := od.ResolveRunStep(ctx, orchID, runID, step, res); err != nil {
			if status.Code(err) == codes.FailedPrecondition {
				// The run moved on: say so and reload, so the row shows where it is.
				return orchDoneMsg{status: "error: " + status.Convert(err).Message(), reload: true, sink: s, target: t}
			}
			return orchDoneMsg{err: err, sink: s}
		}
		return orchDoneMsg{status: fmt.Sprintf("%s requested for %s (%s)", verb, runID, name), reload: true, sink: s, target: t}
	}
	return m.enterConfirm(st)
}

// failConsequence says what failing the run at its current step does (FR-212,
// FR-214, FR-216, FR-222).
func failConsequence(r *pb.Run) string {
	st := currentStep(r)
	switch {
	case st.GetKind() == pb.StepKind_STEP_KIND_CLEAR:
		return "Clear is abandoned; the worker is left needing the developer (R retries Clear)."
	case st.GetRole() == pb.StepRole_STEP_ROLE_CLEANUP:
		return "The remaining post-prompts are dropped; Clear (if enabled) is still sent."
	}
	return "No post-prompt follows; Clear (if enabled) is sent once the agent is idle."
}

// lastSeenOf is " (last seen working 10:42)" for a restart halt's detached
// agent, read from its worker's record when the TUI lists it; "" otherwise.
func (m Model) lastSeenOf(r *pb.Run) string {
	sb, _, why := m.runSandbox(r)
	if why != "" || sb.GetAgent().GetAgentCheck().GetState() != pb.AgentCheck_STATE_DETACHED {
		return ""
	}
	return " (last seen " + lastSeenText(sb.GetAgent().GetAgentCheck()) + ")"
}

// stepLines renders a run's steps for the prompt overlay (FR-220): pre, the
// action prompt (its composed text in the panel), post; ✓ marks finished steps
// and ▶ the current one. width bounds a step's line.
func (m Model) stepLines(r *pb.Run, width int) []string {
	cur := int(r.GetStep())
	done := r.GetStepStatus() == pb.StepStatus_STEP_STATUS_DONE
	queued := r.GetState() == pb.RunState_RUN_STATE_QUEUED
	var lines []string
	last := pb.StepRole_STEP_ROLE_UNSPECIFIED
	for i, st := range r.GetSteps() {
		label := ""
		if st.GetRole() != last {
			switch st.GetRole() {
			case pb.StepRole_STEP_ROLE_SETUP:
				label = "pre"
			case pb.StepRole_STEP_ROLE_ACTION:
				label = "action"
			case pb.StepRole_STEP_ROLE_CLEANUP:
				label = "post"
			}
			last = st.GetRole()
		}
		mark := "  "
		switch {
		case queued:
		case done || i < cur:
			mark = statusOKStyle.Render("✓") + " "
		case i == cur:
			mark = agentWorkStyle.Render("▶") + " "
		}
		text := sanitizeLine(st.GetText(), width)
		if st.GetKind() == pb.StepKind_STEP_KIND_ACTION_PROMPT {
			text = dimStyle.Render("(composed prompt, below)")
		}
		lines = append(lines, dimStyle.Render(fitCol(label, 8))+mark+text)
	}
	return lines
}
