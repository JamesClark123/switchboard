package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The four tool semantics behind link call frames (research R5/R6,
// contracts/orchestrator-mcp.md §Tools). The worker's daemon attributed the
// call to a sandbox before relaying it; here every tool refuses callers that
// are not current sandboxes of this orchestrator on that host (FR-144/FR-155).

const (
	toolListActions      = "orchestrator_list_actions"
	toolCallAction       = "orchestrator_call_action"
	toolCheckRun         = "orchestrator_check_run"
	toolReportCompletion = "orchestrator_report_completion"
)

// Tool payloads (JSON in / JSON out).
type toolArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default"`
}

type toolAction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Type        string         `json:"type"`
	WorkerTag   string         `json:"worker_tag"`
	Arguments   []toolArgument `json:"arguments"`
}

type listActionsOut struct {
	Orchestrator string       `json:"orchestrator"`
	Actions      []toolAction `json:"actions"`
}

type callActionIn struct {
	Action    string            `json:"action"`
	Arguments map[string]string `json:"arguments"`
}

type callActionOut struct {
	RunID    string `json:"run_id"`
	Position uint32 `json:"position"`
	State    string `json:"state"`
}

type checkRunIn struct {
	RunID string `json:"run_id"`
}

type checkRunOut struct {
	RunID             string `json:"run_id"`
	Action            string `json:"action"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	Summary           string `json:"summary"`
	ClosedByDeveloper bool   `json:"closed_by_developer"`
}

type reportIn struct {
	Summary string `json:"summary"`
}

type reportOut struct {
	OK    bool   `json:"ok"`
	RunID string `json:"run_id"`
	State string `json:"state"`
}

// maxSummaryLen bounds a completion report's summary (contract: 1–2000 chars).
const maxSummaryLen = 2000

// handleCall answers one relayed tool call.
func (rt *runtime) handleCall(_ context.Context, hostID string, call *pb.OrchestratorLinkFrame_Call) *pb.OrchestratorLinkFrame_Result {
	body, err := rt.runTool(hostID, call.GetSandboxId(), call.GetTool(), call.GetArgsJson())
	if err != nil {
		return &pb.OrchestratorLinkFrame_Result{Id: call.GetId(), Ok: false, Error: err.Error()}
	}
	return &pb.OrchestratorLinkFrame_Result{Id: call.GetId(), Ok: true, BodyJson: body}
}

// runTool dispatches a tool for an attributed caller and renders its JSON result.
func (rt *runtime) runTool(hostID, sandboxID, tool, argsJSON string) (string, error) {
	rec := rt.record()
	if hostOf(rec, sandboxID) != hostID {
		return "", fmt.Errorf("not a worker of orchestrator %s", rec.GetName())
	}
	var out any
	var err error
	switch tool {
	case toolListActions:
		out = rt.toolListActions(rec)
	case toolCallAction:
		var in callActionIn
		if err := unmarshalArgs(argsJSON, &in); err != nil {
			return "", err
		}
		out, err = rt.toolCallAction(sandboxID, in)
	case toolCheckRun:
		var in checkRunIn
		if err := unmarshalArgs(argsJSON, &in); err != nil {
			return "", err
		}
		out, err = rt.toolCheckRun(in)
	case toolReportCompletion:
		var in reportIn
		if err := unmarshalArgs(argsJSON, &in); err != nil {
			return "", err
		}
		out, err = rt.toolReportCompletion(sandboxID, in)
	default:
		return "", fmt.Errorf("unknown tool %s", tool)
	}
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalArgs(argsJSON string, into any) error {
	if argsJSON == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(argsJSON), into); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

func actionTypeLabel(t pb.ActionType) string {
	if t == pb.ActionType_ACTION_TYPE_REASON {
		return "reason"
	}
	return "prompt"
}

func (rt *runtime) toolListActions(rec *pb.Orchestrator) listActionsOut {
	out := listActionsOut{Orchestrator: rec.GetName(), Actions: []toolAction{}}
	for _, a := range rec.GetActions() {
		ta := toolAction{Name: a.GetName(), Description: a.GetDescription(), Type: actionTypeLabel(a.GetType()), WorkerTag: a.GetWorkerTag(), Arguments: []toolArgument{}}
		for _, arg := range a.GetArguments() {
			ta.Arguments = append(ta.Arguments, toolArgument{Name: arg.GetName(), Description: arg.GetDescription(), Required: arg.GetRequired(), Default: arg.GetDefaultValue()})
		}
		out.Actions = append(out.Actions, ta)
	}
	return out
}

// toolCallAction queues a TRIGGERED run with the caller as origin (FR-144).
func (rt *runtime) toolCallAction(sandboxID string, in callActionIn) (callActionOut, error) {
	label := sandboxID
	if sb := rt.sandboxRecord(sandboxID); sb != nil && sb.GetDisplayName() != "" {
		label = sb.GetDisplayName()
	}
	run, err := rt.callAction(in.Action, in.Arguments, pb.EventKind_EVENT_KIND_TRIGGERED, sandboxID, label)
	if err != nil {
		return callActionOut{}, err
	}
	return callActionOut{RunID: run.GetId(), Position: run.GetPosition(), State: "queued"}, nil
}

func (rt *runtime) toolCheckRun(in checkRunIn) (checkRunOut, error) {
	run, err := rt.svc.reg.GetRun(rt.id, in.RunID)
	if err != nil {
		return checkRunOut{}, fmt.Errorf("unknown run %s", in.RunID)
	}
	return checkRunOut{RunID: run.GetId(), Action: run.GetAction(), State: stateLabel(run.GetState()), Reason: run.GetReason(), Summary: run.GetSummary(), ClosedByDeveloper: run.GetClosedByDeveloper()}, nil
}

// toolReportCompletion closes the run recorded on the calling sandbox (FR-155).
func (rt *runtime) toolReportCompletion(sandboxID string, in reportIn) (reportOut, error) {
	switch {
	case in.Summary == "":
		return reportOut{}, fmt.Errorf("a summary is required")
	case len(in.Summary) > maxSummaryLen:
		return reportOut{}, fmt.Errorf("the summary is longer than %d characters", maxSummaryLen)
	}
	run := rt.runOn(sandboxID)
	if run == nil {
		return reportOut{}, fmt.Errorf("no run is in flight on this sandbox")
	}
	if run.GetSummary() != "" {
		return reportOut{}, fmt.Errorf("run %s already has a summary", run.GetId())
	}
	rt.mu.Lock()
	rt.reported[run.GetId()] = true
	rt.mu.Unlock()
	closed, err := rt.completeRun(run, in.Summary, false, false)
	if err != nil {
		return reportOut{}, err
	}
	return reportOut{OK: true, RunID: closed.GetId(), State: stateLabel(closed.GetState())}, nil
}
