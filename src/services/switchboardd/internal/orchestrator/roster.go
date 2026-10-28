package orchestrator

import (
	"sort"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Roster freshness (SWI-18 research R4, contracts/dispatch.md "Roster
// freshness"). applyRecords is the only code that applies observed sandbox
// records — listings, events, attach/detach/launch replies, removals — to
// rec.workers, rec.reasoning_sandbox_id and rec.high_water_marks. Lane writes
// change only the lane fields of existing entries.

// rosterSource names where the records of a rosterInput came from.
type rosterSource int

const (
	// srcListing is a host's full ListSandboxes, requested at requestedSeq.
	srcListing rosterSource = iota
	// srcEvent is a sandbox_changed event from a host's subscription.
	srcEvent
	// srcAttachReply / srcDetachReply / srcLaunchReply are this
	// orchestrator's own calls' replies (launch: the reasoning sandbox).
	srcAttachReply
	srcDetachReply
	srcLaunchReply
	// srcRemoval is a removed event (ids only).
	srcRemoval
	// srcDestroyReasoning is the orchestrator destroying its reasoning sandbox.
	srcDestroyReasoning
)

// rosterInput is one batch of observed records.
type rosterInput struct {
	source rosterSource
	// hostID is the host the records came from (the listed host for a listing).
	hostID string
	// requestedSeq is roster_seq read before the listing was requested.
	requestedSeq uint64
	// records are the observed sandbox records (a listing: all of the host's).
	records []*pb.Sandbox
	// ids are the removed sandboxes (srcRemoval, srcDestroyReasoning).
	ids []string
	// cache is a snapshot of rt.sbs for the input's ids, taken under rt.mu
	// inside the update callback, so applyRecords reads no shared state.
	cache map[string]*pb.Sandbox
}

// droppedWorker is a roster entry applyRecords removed.
type droppedWorker struct {
	id      string
	hostID  string
	removed bool // removal or listing absence, not a controller change
	// removedEvent: a removed event or DESTROYING record (runs fail "sandbox
	// removed"; other drops fail them "worker detached").
	removedEvent bool
	reasoning    bool
}

// rosterResult says what applyRecords changed, so the caller can follow up
// after the commit (fail runs, push lanes and briefings, dispatch).
type rosterResult struct {
	// changed is false when nothing in the record changed: the caller only
	// refreshes its cache and writes nothing.
	changed bool
	adopted []string
	dropped []droppedWorker
	// evict lists cache entries to delete (drops), after the rules read them.
	evict []string
	// reasoningLost is set when the reasoning sandbox left the roster other
	// than through destroyReasoning.
	reasoningLost bool
}

// isReply reports whether the source is one of this orchestrator's own calls.
func (s rosterSource) isReply() bool {
	return s == srcAttachReply || s == srcDetachReply || s == srcLaunchReply
}

// applier carries one applyRecords call's state.
type applier struct {
	rec *pb.Orchestrator
	in  rosterInput
	now time.Time
	res rosterResult
	seq uint64 // roster_seq of this write, once bumped
}

// applyRecords folds observed sandbox records into rec. It is called inside
// rt.update, so the freshness check and the change are one atomic write.
func applyRecords(rec *pb.Orchestrator, in rosterInput, now time.Time) rosterResult {
	a := &applier{rec: rec, in: in, now: now}
	switch in.source {
	case srcRemoval:
		for _, id := range in.ids {
			a.remove(id)
		}
	case srcDestroyReasoning:
		for _, id := range in.ids {
			a.remove(id)
			if rec.GetReasoningSandboxId() == id {
				rec.ReasoningSandboxId = ""
				a.res.changed = true
			}
		}
		a.res.reasoningLost = false
	case srcListing:
		a.listing()
	default:
		for _, sb := range in.records {
			a.apply(sb)
		}
	}
	return a.res
}

// write bumps the roster clock once per call and returns this write's seq.
func (a *applier) write() uint64 {
	if a.seq == 0 {
		a.rec.RosterSeq++
		a.seq = a.rec.GetRosterSeq()
	}
	a.res.changed = true
	return a.seq
}

func (a *applier) entry(id string) *pb.Worker {
	for _, w := range a.rec.GetWorkers() {
		if w.GetSandboxId() == id {
			return w
		}
	}
	return nil
}

func (a *applier) mark(id string) *pb.HighWaterMark {
	return a.rec.GetHighWaterMarks()[id]
}

func (a *applier) setMark(id string, m *pb.HighWaterMark) {
	if a.rec.HighWaterMarks == nil {
		a.rec.HighWaterMarks = map[string]*pb.HighWaterMark{}
	}
	a.rec.HighWaterMarks[id] = m
	a.res.changed = true
}

func (a *applier) deleteMark(id string) {
	if _, ok := a.rec.GetHighWaterMarks()[id]; ok {
		delete(a.rec.HighWaterMarks, id)
		a.res.changed = true
	}
}

// drop removes a roster entry; the caller sets the mark.
func (a *applier) drop(w *pb.Worker, removed bool) {
	a.dropFor(w, removed, false)
}

func (a *applier) dropFor(w *pb.Worker, removed, removedEvent bool) {
	a.write()
	kept := a.rec.Workers[:0]
	for _, x := range a.rec.Workers {
		if x.GetSandboxId() != w.GetSandboxId() {
			kept = append(kept, x)
		}
	}
	a.rec.Workers = kept
	a.res.dropped = append(a.res.dropped, droppedWorker{id: w.GetSandboxId(), hostID: w.GetHostId(), removed: removed, removedEvent: removedEvent, reasoning: w.GetReasoning()})
	if a.rec.GetReasoningSandboxId() == w.GetSandboxId() {
		a.rec.ReasoningSandboxId = ""
		a.res.reasoningLost = true
	}
}

// foreign reports whether a roster entry or mark on host belongs to another
// host than the one the input came from: a host never drops, removes or marks
// another host's worker. Inputs with no host (local replies) are trusted.
func (a *applier) foreign(host string) bool {
	return a.in.hostID != "" && host != "" && host != a.in.hostID
}

// foreignTo reports whether sandbox id's roster entry or mark on rec belongs
// to another host than host (the cache must not take such a record either).
func foreignTo(rec *pb.Orchestrator, id, host string) bool {
	if host == "" {
		return false
	}
	if h := hostOf(rec, id); h != "" {
		return h != host
	}
	h := rec.GetHighWaterMarks()[id].GetHostId()
	return h != "" && h != host
}

// remove applies a removal (removed event, DESTROYING record, reasoning
// destroy): terminal, whatever the revision (research R4).
func (a *applier) remove(id string) {
	cached := a.in.cache[id]
	if w := a.entry(id); w != nil && a.foreign(w.GetHostId()) {
		return
	}
	if m := a.mark(id); m != nil && a.foreign(m.GetHostId()) {
		return
	}
	a.res.evict = append(a.res.evict, id)
	if w := a.entry(id); w != nil {
		a.dropFor(w, true, true)
		a.setMark(id, &pb.HighWaterMark{
			Revision: max(w.GetRevision(), cached.GetRevision()), Removed: true, SetSeq: a.seq,
			RemovedAt: timestamppb.New(a.now), HostId: w.GetHostId(),
		})
		return
	}
	m := a.mark(id)
	if m.GetRemoved() {
		return
	}
	// Not on the roster: only a sandbox already tracked (a mark, or a cached
	// record naming this orchestrator) gets a removed mark; any other removal
	// is ignored, so unrelated sandboxes never grow the record.
	if m == nil && cached.GetController().GetOrchestratorId() != a.rec.GetId() {
		return
	}
	host := m.GetHostId()
	if host == "" {
		host = cached.GetHostId()
	}
	a.setMark(id, &pb.HighWaterMark{
		Revision: max(m.GetRevision(), cached.GetRevision()), Removed: true, SetSeq: a.write(),
		RemovedAt: timestamppb.New(a.now), HostId: host,
	})
}

// listing applies a host's full listing: each record, then absences.
func (a *applier) listing() {
	present := map[string]bool{}
	for _, sb := range a.in.records {
		present[sb.GetId()] = true
		a.apply(sb)
	}
	for _, w := range append([]*pb.Worker(nil), a.rec.GetWorkers()...) {
		if w.GetHostId() != a.in.hostID || present[w.GetSandboxId()] {
			continue
		}
		// Absent: gone, unless the entry was written after the listing was
		// requested (an attach or adoption the listing could not see).
		if w.GetWrittenSeq() > a.in.requestedSeq {
			continue
		}
		a.drop(w, true)
		a.setMark(w.GetSandboxId(), &pb.HighWaterMark{
			Revision: w.GetRevision(), Removed: true, SetSeq: a.seq,
			RemovedAt: timestamppb.New(a.now), HostId: w.GetHostId(),
		})
		a.res.evict = append(a.res.evict, w.GetSandboxId())
	}
	for _, id := range sortedMarkIDs(a.rec) {
		m := a.rec.HighWaterMarks[id]
		switch {
		case a.entry(id) != nil:
		case m.GetRemoved() && a.now.Sub(m.GetRemovedAt().AsTime()) > removedMarkTTL:
			a.deleteMark(id)
		case !m.GetRemoved() && m.GetHostId() == a.in.hostID && !present[id] && m.GetSetSeq() <= a.in.requestedSeq:
			// Its own host no longer lists it: the sandbox is gone. The mark
			// turns removed and is pruned after removedMarkTTL rather than at
			// once, so an event the link's reader still holds from before
			// the listing cannot re-adopt it from an older record.
			a.setMark(id, &pb.HighWaterMark{
				Revision: m.GetRevision(), Removed: true, SetSeq: a.write(),
				RemovedAt: timestamppb.New(a.now), HostId: m.GetHostId(),
			})
		}
	}
	if sweepTickets(a.rec, a.now) {
		a.res.changed = true
	}
}

func sortedMarkIDs(rec *pb.Orchestrator) []string {
	ids := make([]string, 0, len(rec.GetHighWaterMarks()))
	for id := range rec.GetHighWaterMarks() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// apply folds one observed record into the roster.
func (a *applier) apply(sb *pb.Sandbox) {
	id := sb.GetId()
	if sb.GetState() == pb.SandboxState_SANDBOX_STATE_DESTROYING {
		a.remove(id)
		return
	}
	cached := a.in.cache[id]
	r := sb
	// A reply is applied as the newer of itself and the cached record, keeping
	// its own permissions (reasoning adoption, the attach ticket).
	if a.in.source.isReply() && cached != nil && cached.GetRevision() > sb.GetRevision() {
		r = cached
	}
	rev := r.GetRevision()
	// A revision-0 event (its host predates lanes) never adopts or drops.
	if rev == 0 && a.in.source == srcEvent {
		return
	}
	names := r.GetController().GetOrchestratorId() == a.rec.GetId()
	// The roster host is the host the record came from (its link).
	host := a.in.hostID
	if host == "" {
		host = r.GetHostId()
	}
	if w := a.entry(id); w != nil {
		if a.foreign(w.GetHostId()) {
			return // only the worker's own host speaks for it
		}
		if rev != 0 && (rev <= w.GetRevision() || rev < cached.GetRevision()) {
			return // stale or repeated
		}
		if names {
			return // still ours: status only, no write
		}
		a.drop(w, false)
		a.res.evict = append(a.res.evict, id)
		if rev != 0 {
			a.setMark(id, &pb.HighWaterMark{Revision: max(rev, w.GetRevision()), SetSeq: a.seq, HostId: w.GetHostId()})
		}
		return
	}
	m := a.mark(id)
	if m.GetRemoved() || (m != nil && a.foreign(m.GetHostId())) {
		return
	}
	if rev != 0 && ((m != nil && rev <= m.GetRevision()) || rev < cached.GetRevision()) {
		return
	}
	if !names {
		return
	}
	if r.GetController().GetReasoning() {
		ok := a.in.source == srcLaunchReply ||
			id == a.rec.GetReasoningSandboxId() ||
			(a.in.source == srcListing && a.rec.GetReasoningSandboxId() == "")
		if !ok {
			return
		}
		seq := a.write()
		a.rec.Workers = append(a.rec.Workers, &pb.Worker{
			SandboxId: id, HostId: host, AttachedAt: r.GetController().GetAttachedAt(), Reasoning: true,
			Revision: rev, WrittenSeq: seq,
		})
		a.rec.ReasoningSandboxId = id
		a.deleteMark(id)
		a.res.adopted = append(a.res.adopted, id)
		return
	}
	lanes, pending := a.ticketLanes(r, host)
	seq := a.write()
	a.rec.Workers = append(a.rec.Workers, &pb.Worker{
		SandboxId: id, HostId: host, AttachedAt: r.GetController().GetAttachedAt(),
		LaneIds: lanes, LanesPending: pending, Revision: rev, WrittenSeq: seq,
	})
	a.deleteMark(id)
	a.res.adopted = append(a.res.adopted, id)
}

// ticketLanes decides an adopted worker's lanes from its attach ticket
// (research R11), consuming the ticket it uses.
func (a *applier) ticketLanes(sb *pb.Sandbox, host string) (lanes []string, pending bool) {
	if tid := sb.GetController().GetAttachTicket(); tid != "" {
		t := a.takeTicket(func(t *pb.AttachTicket) bool { return t.GetId() == tid })
		if t == nil || !ticketValid(t, a.now) || t.GetHostId() != host ||
			(t.GetSandboxId() != "" && t.GetSandboxId() != sb.GetId()) {
			return nil, true
		}
		return existingLanes(a.rec, t.GetLaneIds()), false
	}
	if sb.GetRevision() != 0 {
		return nil, false
	}
	// A host that predates lanes drops the ticket id: fail closed (R11).
	if t := a.takeTicket(func(t *pb.AttachTicket) bool {
		return t.GetSandboxId() == sb.GetId() && t.GetHostId() == host && ticketValid(t, a.now)
	}); t != nil {
		return existingLanes(a.rec, t.GetLaneIds()), false
	}
	if a.in.source != srcListing {
		return nil, false
	}
	created := sb.GetCreatedAt().AsTime()
	var oldest *pb.AttachTicket
	for _, t := range a.rec.GetAttachTickets() {
		if t.GetSandboxId() != "" || t.GetHostId() != host || !ticketValid(t, a.now) ||
			t.GetIssuedAt().AsTime().After(created.Add(ticketSkew)) {
			continue
		}
		if oldest == nil || t.GetIssuedAt().AsTime().Before(oldest.GetIssuedAt().AsTime()) {
			oldest = t
		}
	}
	if oldest == nil {
		return nil, false
	}
	a.takeTicket(func(t *pb.AttachTicket) bool { return t == oldest })
	return nil, true
}

// takeTicket removes and returns the first ticket match accepts (nil if none).
func (a *applier) takeTicket(match func(*pb.AttachTicket) bool) *pb.AttachTicket {
	for i, t := range a.rec.GetAttachTickets() {
		if match(t) {
			a.rec.AttachTickets = append(a.rec.AttachTickets[:i], a.rec.AttachTickets[i+1:]...)
			a.res.changed = true
			return t
		}
	}
	return nil
}

// ticketValid reports whether a ticket is unexpired at now.
func ticketValid(t *pb.AttachTicket, now time.Time) bool {
	return now.Before(t.GetIssuedAt().AsTime().Add(attachTicketTTL))
}

// sweepTickets deletes expired tickets, reporting whether any went.
func sweepTickets(rec *pb.Orchestrator, now time.Time) bool {
	kept := rec.AttachTickets[:0]
	for _, t := range rec.GetAttachTickets() {
		if ticketValid(t, now) {
			kept = append(kept, t)
		}
	}
	swept := len(kept) != len(rec.GetAttachTickets())
	rec.AttachTickets = kept
	return swept
}

// pruneRemovedMarks deletes removed marks older than removedMarkTTL (load).
func pruneRemovedMarks(rec *pb.Orchestrator, now time.Time) bool {
	pruned := false
	for id, m := range rec.GetHighWaterMarks() {
		if m.GetRemoved() && now.Sub(m.GetRemovedAt().AsTime()) > removedMarkTTL {
			delete(rec.HighWaterMarks, id)
			pruned = true
		}
	}
	return pruned
}

// existingLanes keeps the ids that name an existing lane, in order, once each.
func existingLanes(rec *pb.Orchestrator, ids []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || laneByID(rec, id) == nil {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// rosterIDs lists the sandbox ids an input concerns (for the cache snapshot).
func (in rosterInput) rosterIDs() []string {
	ids := append([]string(nil), in.ids...)
	for _, sb := range in.records {
		ids = append(ids, sb.GetId())
	}
	return ids
}

// snapshotCacheLocked copies the cached records of ids (caller holds rt.mu).
func (rt *runtime) snapshotCacheLocked(ids []string) map[string]*pb.Sandbox {
	out := make(map[string]*pb.Sandbox, len(ids))
	for _, id := range ids {
		if sb, ok := rt.sbs[id]; ok {
			out[id] = proto.Clone(sb).(*pb.Sandbox)
		}
	}
	return out
}
