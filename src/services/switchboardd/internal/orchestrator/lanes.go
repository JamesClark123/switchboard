package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Worker lanes (SWI-18, data-model "Lane"): named groups of workers on one
// orchestrator, referenced by id everywhere, so a rename never changes routing.

// The data-model's validation messages, shown verbatim by the TUI.
const (
	msgLaneName        = "lane name must be 1–64 lowercase letters, digits or hyphens, starting with a letter or digit"
	msgLaneReserved    = `"open" is reserved: it names workers in no lane`
	msgLaneDescription = "description must be one line of at most 200 characters"
	msgLaneCap         = "this orchestrator already has 32 lanes"
	msgReasonLanes     = "reason actions run on the reasoning sandbox and take no lanes"
	msgLaneMode        = "choose exclusive or inclusive for an action with lanes"
	msgOldClient       = "this client predates lanes — update the client"
	msgActionDeleted   = "its action was deleted"
	// openLaneName is the reserved name of "no lane".
	openLaneName = "open"
)

// validateLaneName checks a lane name (FR-303).
func validateLaneName(name string) error {
	if name == openLaneName {
		return fmt.Errorf("%w: %s", ErrInvalid, msgLaneReserved)
	}
	if name == "" || len(name) > 64 || !actionNameRe.MatchString(name) {
		return fmt.Errorf("%w: %s", ErrInvalid, msgLaneName)
	}
	return nil
}

// validLaneName is validateLaneName as a predicate.
func validLaneName(name string) bool { return validateLaneName(name) == nil }

// validateLaneDescription trims a description and checks it is one line of at
// most maxLaneDescription runes with no control characters (FR-302).
func validateLaneDescription(desc string) (string, error) {
	d := strings.TrimSpace(desc)
	if !utf8.ValidString(d) || utf8.RuneCountInString(d) > maxLaneDescription ||
		strings.IndexFunc(d, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%w: %s", ErrInvalid, msgLaneDescription)
	}
	return d, nil
}

// newLaneID mints "ln-" + 12 random hex characters.
func newLaneID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return laneIDPrefix + hex.EncodeToString(b[:])
}

// newTicketID mints a 16-hex attach ticket id.
func newTicketID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// laneByID returns the lane with id (nil when none).
func laneByID(rec *pb.Orchestrator, id string) *pb.Lane {
	for _, l := range rec.GetLanes() {
		if l.GetId() == id {
			return l
		}
	}
	return nil
}

// laneByName returns the lane named name (nil when none).
func laneByName(rec *pb.Orchestrator, name string) *pb.Lane {
	for _, l := range rec.GetLanes() {
		if l.GetName() == name {
			return l
		}
	}
	return nil
}

// laneNames maps lane ids to names, skipping ids that name no lane.
func laneNames(rec *pb.Orchestrator, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if l := laneByID(rec, id); l != nil {
			out = append(out, l.GetName())
		}
	}
	return out
}

// isOpenLane reports whether a roster entry is an open-lane worker: a worker
// in no lane, not awaiting a lane assignment.
func isOpenLane(w *pb.Worker) bool {
	return !w.GetReasoning() && !w.GetLanesPending() && len(w.GetLaneIds()) == 0
}

// effectiveLanes is a queued run's lane setting (data-model "Effective lane
// setting"): its kept copy, else its action's current setting; ok is false
// when neither exists (the action was deleted with no copy). A setting with no
// lanes reads as un-laned, whatever its stored mode.
func effectiveLanes(rec *pb.Orchestrator, run *pb.Run) (ids []string, mode pb.LaneMode, ok bool) {
	if kept := run.GetKeptLanes(); kept != nil {
		if len(kept.GetLaneIds()) == 0 {
			return nil, pb.LaneMode_LANE_MODE_UNSPECIFIED, true
		}
		return kept.GetLaneIds(), kept.GetMode(), true
	}
	a := findAction(rec, run.GetAction())
	if a == nil {
		return nil, pb.LaneMode_LANE_MODE_UNSPECIFIED, false
	}
	if len(a.GetLaneIds()) == 0 {
		return nil, pb.LaneMode_LANE_MODE_UNSPECIFIED, true
	}
	return a.GetLaneIds(), a.GetLaneMode(), true
}

// allowed reports whether a worker's lanes allow it a run with this setting
// (FR-311–FR-313).
func allowed(workerLanes []string, ids []string, mode pb.LaneMode) bool {
	if len(ids) == 0 {
		return len(workerLanes) == 0
	}
	for _, l := range workerLanes {
		if slices.Contains(ids, l) {
			return true
		}
	}
	return mode == pb.LaneMode_LANE_MODE_INCLUSIVE && len(workerLanes) == 0
}

// --- the open-lane guard and change impact (research R10, FR-305, FR-308a) ---

// openLaneCount counts the live, non-detaching open-lane workers.
func openLaneCount(rec *pb.Orchestrator, detaching map[string]struct{}) int {
	n := 0
	for _, w := range rec.GetWorkers() {
		if _, d := detaching[w.GetSandboxId()]; !d && isOpenLane(w) {
			n++
		}
	}
	return n
}

// unlanedWork is the guard's list: the un-laned prompt actions, and the
// queued runs of deleted actions whose kept setting is un-laned (runs of
// existing actions are covered by listing the action).
func unlanedWork(rec *pb.Orchestrator, queued []*pb.Run) (actions, runs []string) {
	for _, a := range rec.GetActions() {
		if a.GetType() == pb.ActionType_ACTION_TYPE_PROMPT && len(a.GetLaneIds()) == 0 {
			actions = append(actions, a.GetName())
		}
	}
	for _, r := range queued {
		if r.GetType() == pb.ActionType_ACTION_TYPE_PROMPT && r.GetKeptLanes() != nil && len(r.GetKeptLanes().GetLaneIds()) == 0 {
			runs = append(runs, r.GetId())
		}
	}
	return actions, runs
}

// guardFires is FR-308a's rule: the change takes the last open-lane worker
// away while un-laned work exists, or saves an un-laned prompt action while
// there is none. detaching is the set after the change.
func guardFires(before, after *pb.Orchestrator, queuedAfter []*pb.Run, detachingBefore, detachingAfter map[string]struct{}, savesUnlaned bool) bool {
	openAfter := openLaneCount(after, detachingAfter)
	if openAfter != 0 {
		return false
	}
	if savesUnlaned {
		return true
	}
	actions, runs := unlanedWork(after, queuedAfter)
	return openLaneCount(before, detachingBefore) >= 1 && (len(actions) > 0 || len(runs) > 0)
}

// guardImpact returns the guard's list when it fires (an empty impact when it
// does not).
func guardImpact(before, after *pb.Orchestrator, queuedAfter []*pb.Run, detachingBefore, detachingAfter map[string]struct{}, savesUnlaned bool) *pb.LaneChangeImpact {
	out := &pb.LaneChangeImpact{}
	if guardFires(before, after, queuedAfter, detachingBefore, detachingAfter, savesUnlaned) {
		out.UnlanedActions, out.UnlanedRuns = unlanedWork(after, queuedAfter)
	}
	return out
}

// laneDeleteImpact fills the delete-only fields of an impact (FR-305): the
// lane's member workers (display names via name), the actions using it, the
// actions it leaves un-laned and the queued runs whose routing changes.
func laneDeleteImpact(out *pb.LaneChangeImpact, rec *pb.Orchestrator, queued []*pb.Run, laneID string, name func(sandboxID string) string) {
	for _, w := range rec.GetWorkers() {
		if slices.Contains(w.GetLaneIds(), laneID) {
			out.Workers = append(out.Workers, name(w.GetSandboxId()))
		}
	}
	affected := map[string]bool{}
	for _, a := range rec.GetActions() {
		if !slices.Contains(a.GetLaneIds(), laneID) {
			continue
		}
		affected[a.GetName()] = true
		out.Actions = append(out.Actions, &pb.LaneActionUse{Action: a.GetName(), Mode: a.GetLaneMode()})
		if len(a.GetLaneIds()) == 1 {
			out.ActionsLeftUnlaned = append(out.ActionsLeftUnlaned, a.GetName())
		}
	}
	for _, r := range queued {
		if kept := r.GetKeptLanes(); kept != nil {
			if slices.Contains(kept.GetLaneIds(), laneID) {
				out.ReroutedRuns++
			}
		} else if affected[r.GetAction()] {
			out.ReroutedRuns++
		}
	}
}

// confirmCovers reports whether the developer's confirmation covers the
// current guard list (current ⊆ confirmed).
func confirmCovers(confirm *pb.LaneGuardConfirm, impact *pb.LaneChangeImpact) bool {
	for _, a := range impact.GetUnlanedActions() {
		if !slices.Contains(confirm.GetUnlanedActions(), a) {
			return false
		}
	}
	for _, r := range impact.GetUnlanedRuns() {
		if !slices.Contains(confirm.GetUnlanedRuns(), r) {
			return false
		}
	}
	return true
}

// guardFired reports whether an impact carries a guard list.
func guardFired(impact *pb.LaneChangeImpact) bool {
	return len(impact.GetUnlanedActions()) > 0 || len(impact.GetUnlanedRuns()) > 0
}

// GuardError is a guarded write the open-lane guard refused (FR-308a). The
// gRPC layer answers FAILED_PRECONDITION with Impact as a status detail.
type GuardError struct{ Impact *pb.LaneChangeImpact }

func (e *GuardError) Error() string {
	var parts []string
	if a := e.Impact.GetUnlanedActions(); len(a) > 0 {
		parts = append(parts, "actions "+strings.Join(a, ", "))
	}
	if r := e.Impact.GetUnlanedRuns(); len(r) > 0 {
		parts = append(parts, "queued runs "+strings.Join(r, ", "))
	}
	return "no open-lane worker would be left for " + strings.Join(parts, " and ") + " — confirm to continue"
}

func (e *GuardError) Is(target error) bool { return target == ErrPrecondition }

// GRPCStatus renders the refusal with its impact attached, so the client can
// show the current list in its confirm.
func (e *GuardError) GRPCStatus() *status.Status {
	st := status.New(codes.FailedPrecondition, e.Error())
	if withDetail, err := st.WithDetails(proto.Clone(e.Impact).(*pb.LaneChangeImpact)); err == nil {
		return withDetail
	}
	return st
}

// refuseGuard checks a guarded change: nil when the guard does not fire or
// the confirmation covers its list.
func refuseGuard(confirm *pb.LaneGuardConfirm, impact *pb.LaneChangeImpact) error {
	if !guardFired(impact) || confirmCovers(confirm, impact) {
		return nil
	}
	return &GuardError{Impact: proto.Clone(impact).(*pb.LaneChangeImpact)}
}

