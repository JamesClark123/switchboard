package orchestrator

import (
	"context"
	"fmt"
	"slices"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Lane writes (SWI-18 FR-301–FR-308a). Every guarded write is one
// rt.updateRuns: the record and the queued runs come from the same
// transaction, the change is applied to them, the open-lane guard is
// evaluated on before/after, and the write is refused unless the developer's
// confirmation covers the guard's current list (research R10). CheckLaneChange
// runs the same builders over a read-only view.

// laneChange is what a guarded change's builder reports about itself.
type laneChange struct {
	// savesUnlaned: the change saves an un-laned prompt action, or a lane
	// delete leaves one (or a kept run's setting) un-laned (FR-308a).
	savesUnlaned bool
	// detaching: a worker that leaves the open-lane count (a detach).
	detaching string
	// deletedLane: the lane a delete removes (fills the delete impact).
	deletedLane string
	// rewritten: queued runs whose kept setting the change rewrote.
	rewritten []*pb.Run
	// marked: workers whose lane display changed (pushed after the commit).
	marked []string
	// briefing: the change alters the agent-facing action text.
	briefing bool
}

// changeFn applies a change to the record and (copies of) its queued runs.
type changeFn func(o *pb.Orchestrator, queued []*pb.Run) (laneChange, error)

func cloneRuns(runs []*pb.Run) []*pb.Run {
	out := make([]*pb.Run, len(runs))
	for i, r := range runs {
		out[i] = proto.Clone(r).(*pb.Run)
	}
	return out
}

// impactOf evaluates the guard (and, for a lane delete, the delete impact)
// of a change applied to after.
func (rt *runtime) impactOf(before, after *pb.Orchestrator, queuedBefore, queuedAfter []*pb.Run, ch laneChange) *pb.LaneChangeImpact {
	rt.mu.Lock()
	detBefore := make(map[string]struct{}, len(rt.detaching)+1)
	for id := range rt.detaching {
		detBefore[id] = struct{}{}
	}
	rt.mu.Unlock()
	detAfter := detBefore
	if ch.detaching != "" {
		detAfter = make(map[string]struct{}, len(detBefore)+1)
		for id := range detBefore {
			detAfter[id] = struct{}{}
		}
		detAfter[ch.detaching] = struct{}{}
	}
	impact := guardImpact(before, after, queuedAfter, detBefore, detAfter, ch.savesUnlaned)
	if ch.deletedLane != "" {
		laneDeleteImpact(impact, before, queuedBefore, ch.deletedLane, rt.displayName)
	}
	return impact
}

// guarded applies a guarded change in one updateRuns. A detach's mark is set
// inside the callback (refused while a run is being delivered to the worker)
// and removed again on any error, the commit included.
func (rt *runtime) guarded(confirm *pb.LaneGuardConfirm, fn changeFn) (*pb.Orchestrator, laneChange, *pb.LaneChangeImpact, error) {
	var ch laneChange
	var impact *pb.LaneChangeImpact
	marked := false
	rec, err := rt.updateRuns(func(o *pb.Orchestrator, runs RunTx) error {
		marked = false
		queued, err := runs.Queued()
		if err != nil {
			return err
		}
		before := proto.Clone(o).(*pb.Orchestrator)
		after := cloneRuns(queued)
		if ch, err = fn(o, after); err != nil {
			return err
		}
		impact = rt.impactOf(before, o, queued, after, ch)
		if err := refuseGuard(confirm, impact); err != nil {
			return err
		}
		for _, r := range ch.rewritten {
			if err := runs.Put(r); err != nil {
				return err
			}
		}
		if ch.detaching != "" {
			rt.mu.Lock()
			_, already := rt.detaching[ch.detaching]
			err := rt.markDetachingLocked(ch.detaching, rt.displayNameLocked(ch.detaching))
			rt.mu.Unlock()
			if err != nil {
				return err
			}
			marked = !already
		}
		return nil
	})
	if err != nil {
		if marked {
			rt.clearDetaching(ch.detaching)
		}
		return nil, laneChange{}, nil, err
	}
	return rec, ch, impact, nil
}

// check evaluates a change over a read-only view: its impact, nothing written.
func (rt *runtime) check(fn changeFn) (*pb.LaneChangeImpact, error) {
	var impact *pb.LaneChangeImpact
	err := rt.svc.reg.ViewOrchestratorRuns(rt.id, func(rec *pb.Orchestrator, queued []*pb.Run) error {
		o := proto.Clone(rec).(*pb.Orchestrator)
		after := cloneRuns(queued)
		ch, err := fn(o, after)
		if err != nil {
			return err
		}
		impact = rt.impactOf(rec, o, queued, after, ch)
		return nil
	})
	if err != nil {
		return nil, notFound(err)
	}
	return impact, nil
}

// afterLaneChange runs a committed lane change's follow-ups: display pushes,
// the briefing when action text changed, the event and a dispatch pass
// (FR-314a).
func (rt *runtime) afterLaneChange(ch laneChange) {
	if len(ch.marked) > 0 {
		rt.markLanes(ch.marked...)
	}
	if ch.briefing {
		rt.requestBriefing()
	}
	rt.svc.emitChanged(rt.id)
	rt.triggerDispatch()
}

// displayName is a worker's display name from its cached record, else its id.
func (rt *runtime) displayName(sandboxID string) string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.displayNameLocked(sandboxID)
}

func (rt *runtime) displayNameLocked(sandboxID string) string {
	if sb, ok := rt.sbs[sandboxID]; ok && sb.GetDisplayName() != "" {
		return sb.GetDisplayName()
	}
	return sandboxID
}

// errLaneGone is FR-316a's refusal for a lane id that no longer exists.
func errLaneGone(id string) error {
	return fmt.Errorf("%w: lane %q no longer exists", ErrNotFound, id)
}

// checkLanesExist refuses any id that names no lane.
func checkLanesExist(o *pb.Orchestrator, ids []string) error {
	for _, id := range ids {
		if laneByID(o, id) == nil {
			return errLaneGone(id)
		}
	}
	return nil
}

// dedupe keeps the first occurrence of each id, in order.
func dedupe(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// sameSet reports whether two id lists hold the same ids.
func sameSet(a, b []string) bool {
	a, b = dedupe(a), dedupe(b)
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// actionUsesLane reports whether any action names the lane.
func actionUsesLane(o *pb.Orchestrator, laneID string) bool {
	for _, a := range o.GetActions() {
		if slices.Contains(a.GetLaneIds(), laneID) {
			return true
		}
	}
	return false
}

// --- lanes (US3) ---

// PutLane creates a lane (empty laneID) or renames / re-describes one
// (FR-301–FR-304). Rename changes no routing: everything refers to the id.
func (s *Service) PutLane(id, laneID, name, description string) (*pb.Lane, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	if err := validateLaneName(name); err != nil {
		return nil, err
	}
	desc, err := validateLaneDescription(description)
	if err != nil {
		return nil, err
	}
	var saved *pb.Lane
	var ch laneChange
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		if other := laneByName(o, name); other != nil && other.GetId() != laneID {
			return fmt.Errorf("%w: a lane named %q already exists", ErrNameTaken, name)
		}
		if laneID == "" {
			if len(o.GetLanes()) >= maxLanes {
				return fmt.Errorf("%w: %s", ErrInvalid, msgLaneCap)
			}
			saved = &pb.Lane{Id: newLaneID(), Name: name, Description: desc, CreatedAt: timestamppb.New(s.clock.Now())}
			o.Lanes = append(o.Lanes, saved)
			return nil
		}
		l := laneByID(o, laneID)
		if l == nil {
			return errLaneGone(laneID)
		}
		if l.GetName() != name {
			for _, w := range o.GetWorkers() {
				if slices.Contains(w.GetLaneIds(), laneID) {
					ch.marked = append(ch.marked, w.GetSandboxId())
				}
			}
			ch.briefing = actionUsesLane(o, laneID)
		}
		l.Name, l.Description = name, desc
		saved = l
		return nil
	}); err != nil {
		return nil, err
	}
	rt.afterLaneChange(ch)
	return proto.Clone(saved).(*pb.Lane), nil
}

// deleteLaneChange removes a lane everywhere it is referenced (FR-305).
func deleteLaneChange(laneID string) changeFn {
	return func(o *pb.Orchestrator, queued []*pb.Run) (laneChange, error) {
		ch := laneChange{deletedLane: laneID}
		if laneByID(o, laneID) == nil {
			return ch, errLaneGone(laneID)
		}
		strip := func(ids []string) ([]string, bool) {
			if !slices.Contains(ids, laneID) {
				return ids, false
			}
			return slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == laneID }), true
		}
		o.Lanes = slices.DeleteFunc(o.Lanes, func(l *pb.Lane) bool { return l.GetId() == laneID })
		for _, w := range o.GetWorkers() {
			if ids, ok := strip(w.GetLaneIds()); ok {
				w.LaneIds = ids
				ch.marked = append(ch.marked, w.GetSandboxId())
			}
		}
		for _, a := range o.GetActions() {
			if ids, ok := strip(a.GetLaneIds()); ok {
				a.LaneIds = ids
				if len(ids) == 0 {
					a.LaneMode = pb.LaneMode_LANE_MODE_UNSPECIFIED
					// Left un-laned: guarded like saving it so (FR-308a).
					ch.savesUnlaned = ch.savesUnlaned || a.GetType() == pb.ActionType_ACTION_TYPE_PROMPT
				}
				ch.briefing = true
			}
		}
		for _, r := range queued {
			if kept := r.GetKeptLanes(); kept != nil {
				if ids, ok := strip(kept.GetLaneIds()); ok {
					kept.LaneIds = ids
					if len(ids) == 0 {
						kept.Mode = pb.LaneMode_LANE_MODE_UNSPECIFIED
						ch.savesUnlaned = ch.savesUnlaned || r.GetType() == pb.ActionType_ACTION_TYPE_PROMPT
					}
					ch.rewritten = append(ch.rewritten, r)
				}
			}
		}
		for _, t := range o.GetAttachTickets() {
			t.LaneIds, _ = strip(t.GetLaneIds())
		}
		return ch, nil
	}
}

// DeleteLane deletes a lane, guarded by the open-lane guard, and returns the
// impact it applied (FR-305, FR-308a).
func (s *Service) DeleteLane(id, laneID string, confirm *pb.LaneGuardConfirm) (*pb.LaneChangeImpact, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	_, ch, impact, err := rt.guarded(confirm, deleteLaneChange(laneID))
	if err != nil {
		return nil, err
	}
	rt.afterLaneChange(ch)
	return impact, nil
}

// --- membership (US3/US4) ---

// errStaleWorker / errStaleLane are FR-307a's refusals.
func errStaleWorker(name string) error {
	return fmt.Errorf("%w: %s's lanes changed since the picker opened — reloaded", ErrAborted, name)
}

func errStaleLane(name string) error {
	return fmt.Errorf("%w: lane %s's members changed since the editor opened — reloaded", ErrAborted, name)
}

// setWorkerLanesChange replaces one worker's lanes (FR-306) if they and its
// pending flag still equal what the picker showed (FR-307a).
func (rt *runtime) setWorkerLanesChange(req *pb.SetWorkerLanesRequest) changeFn {
	return func(o *pb.Orchestrator, _ []*pb.Run) (laneChange, error) {
		var ch laneChange
		id := req.GetSandboxId()
		w := onRosterEntry(o, id)
		name := rt.displayName(id)
		switch {
		case w == nil:
			return ch, fmt.Errorf("%w: %s is no longer a worker of %s — reloaded", ErrAborted, name, o.GetName())
		case w.GetReasoning():
			return ch, fmt.Errorf("%w: the reasoning sandbox takes no lanes", ErrInvalid)
		case !sameSet(w.GetLaneIds(), req.GetExpectedLaneIds()) || w.GetLanesPending() != req.GetExpectedLanesPending():
			return ch, errStaleWorker(name)
		}
		for _, l := range req.GetLaneIds() {
			if laneByID(o, l) == nil {
				return ch, fmt.Errorf("%w: lane %q was deleted — reloaded", ErrAborted, l)
			}
		}
		w.LaneIds, w.LanesPending = dedupe(req.GetLaneIds()), false
		ch.marked = []string{id}
		return ch, nil
	}
}

// SetWorkerLanes replaces one worker's lanes (FR-306/FR-307a), guarded.
func (s *Service) SetWorkerLanes(req *pb.SetWorkerLanesRequest) (*pb.WorkerView, error) {
	rt, err := s.runtimeFor(req.GetOrchestratorId())
	if err != nil {
		return nil, err
	}
	_, ch, _, err := rt.guarded(req.GetGuard(), rt.setWorkerLanesChange(req))
	if err != nil {
		return nil, err
	}
	rt.afterLaneChange(ch)
	return rt.workerView(req.GetSandboxId()), nil
}

// setLaneMembersChange replaces one lane's members (FR-307) if they still
// equal what the editor showed (FR-307a). A save covering a worker clears its
// lanes-pending flag.
func setLaneMembersChange(req *pb.SetLaneMembersRequest) changeFn {
	return func(o *pb.Orchestrator, _ []*pb.Run) (laneChange, error) {
		var ch laneChange
		laneID := req.GetLaneId()
		l := laneByID(o, laneID)
		if l == nil {
			return ch, fmt.Errorf("%w: lane %q was deleted — reloaded", ErrAborted, laneID)
		}
		var current []string
		for _, w := range o.GetWorkers() {
			if slices.Contains(w.GetLaneIds(), laneID) {
				current = append(current, w.GetSandboxId())
			}
		}
		if !sameSet(current, req.GetExpectedSandboxIds()) {
			return ch, errStaleLane(l.GetName())
		}
		want := dedupe(req.GetSandboxIds())
		for _, id := range want {
			w := onRosterEntry(o, id)
			if w == nil {
				return ch, fmt.Errorf("%w: %s is no longer a worker — reloaded", ErrAborted, id)
			}
			if w.GetReasoning() {
				return ch, fmt.Errorf("%w: the reasoning sandbox takes no lanes", ErrInvalid)
			}
		}
		for _, w := range o.GetWorkers() {
			in, has := slices.Contains(want, w.GetSandboxId()), slices.Contains(w.GetLaneIds(), laneID)
			switch {
			case in && !has:
				w.LaneIds = append(w.LaneIds, laneID)
				w.LanesPending = false
				ch.marked = append(ch.marked, w.GetSandboxId())
			case in && w.GetLanesPending():
				w.LanesPending = false
				ch.marked = append(ch.marked, w.GetSandboxId())
			case !in && has:
				w.LaneIds = slices.DeleteFunc(slices.Clone(w.GetLaneIds()), func(x string) bool { return x == laneID })
				ch.marked = append(ch.marked, w.GetSandboxId())
			}
		}
		return ch, nil
	}
}

// SetLaneMembers replaces one lane's members (FR-307/FR-307a), guarded.
func (s *Service) SetLaneMembers(req *pb.SetLaneMembersRequest) (*pb.Lane, error) {
	rt, err := s.runtimeFor(req.GetOrchestratorId())
	if err != nil {
		return nil, err
	}
	rec, ch, _, err := rt.guarded(req.GetGuard(), setLaneMembersChange(req))
	if err != nil {
		return nil, err
	}
	rt.afterLaneChange(ch)
	return proto.Clone(laneByID(rec, req.GetLaneId())).(*pb.Lane), nil
}

func onRosterEntry(o *pb.Orchestrator, id string) *pb.Worker {
	for _, w := range o.GetWorkers() {
		if w.GetSandboxId() == id {
			return w
		}
	}
	return nil
}

// --- actions (US1/US5) ---

// putActionChange upserts an action whose lane ids must all exist (FR-316a).
func putActionChange(a *pb.Action, now *timestamppb.Timestamp) changeFn {
	return func(o *pb.Orchestrator, _ []*pb.Run) (laneChange, error) {
		ch := laneChange{briefing: true, savesUnlaned: a.GetType() == pb.ActionType_ACTION_TYPE_PROMPT && len(a.GetLaneIds()) == 0}
		if err := checkLanesExist(o, a.GetLaneIds()); err != nil {
			return ch, err
		}
		saved := proto.Clone(a).(*pb.Action)
		for i, ex := range o.Actions {
			if ex.GetName() == a.GetName() {
				saved.CreatedAt, saved.UpdatedAt = ex.GetCreatedAt(), now
				o.Actions[i] = saved
				return ch, nil
			}
		}
		saved.CreatedAt, saved.UpdatedAt = now, now
		o.Actions = append(o.Actions, saved)
		return ch, nil
	}
}

// prepareAction checks a PutAction request before any write (FR-316,
// FR-318a) and returns the normalized action.
func prepareAction(req *pb.PutActionRequest) (*pb.Action, error) {
	a := req.GetAction()
	if !req.GetSetsLanes() || a.GetWorkerTag() != "" { //nolint:staticcheck // SA1019: an old client still sends the retired worker tag (FR-318a)
		return nil, fmt.Errorf("%w: %s", ErrPrecondition, msgOldClient)
	}
	a = normalizeAction(a)
	if err := validateAction(a, nil); err != nil {
		return nil, err
	}
	a.LaneIds = dedupe(a.GetLaneIds())
	switch {
	case len(a.GetLaneIds()) == 0:
		a.LaneMode = pb.LaneMode_LANE_MODE_UNSPECIFIED
	case a.GetType() == pb.ActionType_ACTION_TYPE_REASON:
		return nil, fmt.Errorf("%w: %s", ErrInvalid, msgReasonLanes)
	case a.GetLaneMode() != pb.LaneMode_LANE_MODE_EXCLUSIVE && a.GetLaneMode() != pb.LaneMode_LANE_MODE_INCLUSIVE:
		return nil, fmt.Errorf("%w: %s", ErrInvalid, msgLaneMode)
	}
	return a, nil
}

// --- detach (US4) ---

// detachChange takes a worker out of the open-lane count for the guard; the
// detach itself happens on the owning host afterwards.
func (rt *runtime) detachChange(sandboxID string) changeFn {
	return func(o *pb.Orchestrator, _ []*pb.Run) (laneChange, error) {
		if onRosterEntry(o, sandboxID) == nil {
			return laneChange{}, fmt.Errorf("%w: sandbox %s is not a worker of %s", ErrNotFound, sandboxID, o.GetName())
		}
		return laneChange{detaching: sandboxID}, nil
	}
}

// --- CheckLaneChange (T017) ---

// CheckLaneChange reports what a guarded change would affect, writing
// nothing (FR-305, FR-308a). The top-level orchestrator id wins; a nested
// request's own id must be empty or equal.
func (s *Service) CheckLaneChange(req *pb.CheckLaneChangeRequest) (*pb.LaneChangeImpact, error) {
	id := req.GetOrchestratorId()
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	nested := func(inner string) error {
		if inner != "" && inner != id {
			return fmt.Errorf("%w: the nested request names orchestrator %s, not %s", ErrInvalid, inner, id)
		}
		return nil
	}
	var fn changeFn
	switch c := req.GetChange().(type) {
	case *pb.CheckLaneChangeRequest_SetWorkerLanes:
		if err := nested(c.SetWorkerLanes.GetOrchestratorId()); err != nil {
			return nil, err
		}
		fn = rt.setWorkerLanesChange(c.SetWorkerLanes)
	case *pb.CheckLaneChangeRequest_SetLaneMembers:
		if err := nested(c.SetLaneMembers.GetOrchestratorId()); err != nil {
			return nil, err
		}
		fn = setLaneMembersChange(c.SetLaneMembers)
	case *pb.CheckLaneChangeRequest_PutAction:
		if err := nested(c.PutAction.GetOrchestratorId()); err != nil {
			return nil, err
		}
		a, err := prepareAction(c.PutAction)
		if err != nil {
			return nil, err
		}
		fn = putActionChange(a, timestamppb.New(s.clock.Now()))
	case *pb.CheckLaneChangeRequest_DeleteLane:
		if err := nested(c.DeleteLane.GetOrchestratorId()); err != nil {
			return nil, err
		}
		fn = deleteLaneChange(c.DeleteLane.GetLaneId())
	case *pb.CheckLaneChangeRequest_DetachWorker:
		if err := nested(c.DetachWorker.GetOrchestratorId()); err != nil {
			return nil, err
		}
		fn = rt.detachChange(c.DetachWorker.GetSandboxId())
	default:
		return &pb.LaneChangeImpact{}, nil
	}
	return rt.check(fn)
}

// --- attach tickets (FR-306a/FR-306b, research R11) ---

// issueTicket writes an attach ticket for lanes picked at launch or attach,
// after checking every lane exists and the cap.
func (rt *runtime) issueTicket(hostID, sandboxID string, laneIDs []string) (string, error) {
	tid := newTicketID()
	now := rt.svc.clock.Now()
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		sweepTickets(o, now)
		if err := checkLanesExist(o, laneIDs); err != nil {
			return err
		}
		if len(o.GetAttachTickets()) >= maxAttachTickets {
			return fmt.Errorf("%w: too many pending attaches — try again later", ErrExhausted)
		}
		o.AttachTickets = append(o.AttachTickets, &pb.AttachTicket{
			Id: tid, LaneIds: dedupe(laneIDs), HostId: hostID, SandboxId: sandboxID, IssuedAt: timestamppb.New(now),
		})
		return nil
	}); err != nil {
		return "", err
	}
	return tid, nil
}

// dropTicket deletes a ticket (a definite attach refusal).
func (rt *runtime) dropTicket(tid string) {
	_, _ = rt.update(func(o *pb.Orchestrator) error {
		n := len(o.GetAttachTickets())
		o.AttachTickets = slices.DeleteFunc(o.AttachTickets, func(t *pb.AttachTicket) bool { return t.GetId() == tid })
		if len(o.AttachTickets) == n {
			return errNoChange
		}
		return nil
	})
}

// knownHost reports whether hostID is the orchestrator's own host or one of
// its host entries.
func (rt *runtime) knownHost(hostID string) bool {
	if hostID == rt.svc.hostID {
		return true
	}
	for _, h := range rt.record().GetHosts() {
		if h.GetHostId() == hostID {
			return true
		}
	}
	return false
}

// BundleFor is Bundle with the launch's lanes (FR-306a): with lanes, a ticket
// bound to the target daemon's host is issued and carried in the controller.
func (s *Service) BundleFor(id, hostID string, laneIDs []string) (*pb.WorkerBundle, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	b := rt.bundle()
	if len(laneIDs) == 0 {
		return b, nil
	}
	if hostID == "" || !rt.knownHost(hostID) {
		return nil, fmt.Errorf("%w: unknown host %s", ErrInvalid, hostID)
	}
	tid, err := rt.issueTicket(hostID, "", laneIDs)
	if err != nil {
		return nil, err
	}
	b.Controller.AttachTicket = tid
	s.emitChanged(id)
	return b, nil
}

// --- dropped worker tags (FR-318b) ---

// DismissDroppedWorkerTags clears the dropped-tag notice.
func (s *Service) DismissDroppedWorkerTags(id string) (*pb.Orchestrator, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	rec, err := rt.update(func(o *pb.Orchestrator) error {
		if len(o.GetDroppedWorkerTags()) == 0 {
			return errNoChange
		}
		o.DroppedWorkerTags = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.emitChanged(id)
	return forClient(rec), nil
}

// maxDroppedTags is how many dropped-tag notices a record keeps (newest).
const maxDroppedTags = 100

// dropWorkerTags moves every action's retired worker tag into the notice
// list (load time, FR-318b; the newest maxDroppedTags are kept) and returns
// the tags it dropped.
func dropWorkerTags(o *pb.Orchestrator, now *timestamppb.Timestamp) []*pb.DroppedWorkerTag {
	var dropped []*pb.DroppedWorkerTag
	for _, a := range o.GetActions() {
		if tag := a.GetWorkerTag(); tag != "" { //nolint:staticcheck // SA1019: read only to drop the retired worker tag (FR-318b)
			dropped = append(dropped, &pb.DroppedWorkerTag{Action: a.GetName(), Tag: tag, DroppedAt: now})
			a.WorkerTag = "" //nolint:staticcheck // SA1019: clearing the retired worker tag (FR-318b)
		}
	}
	o.DroppedWorkerTags = append(o.DroppedWorkerTags, dropped...)
	if n := len(o.DroppedWorkerTags); n > maxDroppedTags {
		o.DroppedWorkerTags = o.DroppedWorkerTags[n-maxDroppedTags:]
	}
	return dropped
}

// forClient is a record as clients see it: high-water marks and attach
// tickets are internal (T037).
func forClient(rec *pb.Orchestrator) *pb.Orchestrator {
	if rec == nil {
		return nil
	}
	out := proto.Clone(rec).(*pb.Orchestrator)
	out.HighWaterMarks, out.AttachTickets = nil, nil
	return out
}

var _ = context.Background
