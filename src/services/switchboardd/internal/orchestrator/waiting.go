package orchestrator

import (
	"fmt"
	"slices"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Waiting reasons (FR-315, research R9): derived for QUEUED prompt runs on
// every read, never stored. A run says why it waits only when no live worker
// could take it, or every one that could is stopped or unreachable; a run
// that merely waits for a busy worker says nothing.

// maxWaitingNames bounds the worker names in a reason.
const maxWaitingNames = 5

// laneCounts indexes the roster once per read: per lane id, and "" for the
// open lane, the live workers and how many of them are available (RUNNING,
// host up). Detaching workers are live, and available while RUNNING (spec
// Clarifications: they stay on the live roster until the detach completes).
type laneCounts struct {
	rec     *pb.Orchestrator
	live    map[string]int
	avail   map[string]int
	names   map[string][]string // live workers' display names, roster order
	pending int                 // workers awaiting a lane assignment
}

// laneCounts builds the index from the record, the cached sandbox records and
// host reachability.
func (rt *runtime) laneCounts() *laneCounts {
	rec := rt.record()
	c := &laneCounts{rec: rec, live: map[string]int{}, avail: map[string]int{}, names: map[string][]string{}}
	ups := map[string]bool{}
	for _, w := range rec.GetWorkers() {
		if w.GetReasoning() {
			continue
		}
		if w.GetLanesPending() {
			c.pending++
			continue
		}
		up, seen := ups[w.GetHostId()]
		if !seen {
			up, _ = rt.reachability(w.GetHostId())
			ups[w.GetHostId()] = up
		}
		sb := rt.sandboxRecord(w.GetSandboxId())
		name := w.GetSandboxId()
		if sb.GetDisplayName() != "" {
			name = sb.GetDisplayName()
		}
		available := up && sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING
		keys := w.GetLaneIds()
		if len(keys) == 0 {
			keys = []string{""}
		}
		for _, k := range keys {
			c.live[k]++
			c.names[k] = append(c.names[k], name)
			if available {
				c.avail[k]++
			}
		}
	}
	return c
}

// waitingReason is a queued prompt run's reason, or "" when it has none.
// forAgent names no workers (the run's agent-facing check, R9).
func waitingReason(run *pb.Run, c *laneCounts, forAgent bool) string {
	if run.GetState() != pb.RunState_RUN_STATE_QUEUED || run.GetType() != pb.ActionType_ACTION_TYPE_PROMPT {
		return ""
	}
	ids, mode, ok := effectiveLanes(c.rec, run)
	if !ok {
		return "" // dispatch fails it
	}
	var keys []string
	switch {
	case len(ids) == 0:
		keys = []string{""}
	case mode == pb.LaneMode_LANE_MODE_INCLUSIVE:
		keys = append(slices.Clone(ids), "")
	default:
		keys = ids
	}
	live, avail := 0, 0
	for _, k := range keys {
		live += c.live[k]
		avail += c.avail[k]
	}
	var reason string
	switch {
	case live == 0:
		lanes := strings.Join(laneNames(c.rec, ids), ", ")
		switch {
		case len(ids) == 0:
			reason = "waiting for an open-lane worker"
		case mode == pb.LaneMode_LANE_MODE_INCLUSIVE:
			reason = "waiting for a worker in " + lanes + " or an open-lane worker"
		default:
			reason = "waiting for a worker in " + lanes
		}
	case avail == 0 && forAgent:
		reason = "waiting: every worker that could take it is stopped or unreachable"
	case avail == 0:
		var names []string
		for _, k := range keys {
			for _, n := range c.names[k] {
				if !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
		}
		shown := names
		if len(names) > maxWaitingNames {
			shown = names[:maxWaitingNames]
		}
		reason = "waiting for " + strings.Join(shown, ", ")
		if more := len(names) - len(shown); more > 0 {
			reason += fmt.Sprintf(" +%d more", more)
		}
		reason += ", stopped or unreachable"
	default:
		return ""
	}
	switch {
	case c.pending == 1:
		reason += "; 1 worker awaiting lane assignment"
	case c.pending > 1:
		reason += fmt.Sprintf("; %d workers awaiting lane assignment", c.pending)
	}
	return reason
}
