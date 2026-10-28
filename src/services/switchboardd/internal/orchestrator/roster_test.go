package orchestrator

import (
	"math/rand"
	"slices"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- roster freshness (SWI-18 T009, contracts/dispatch.md "Roster freshness") ---

const rOrch = "orch-r"

var rNow = time.Unix(1_800_000_000, 0)

// rsb builds a record on host h1 at revision rev controlled by ctrl ("" = none).
func rsb(id string, rev uint64, ctrl string) *pb.Sandbox {
	sb := &pb.Sandbox{Id: id, HostId: "h1", Revision: rev, State: pb.SandboxState_SANDBOX_STATE_RUNNING}
	if ctrl != "" {
		sb.Controller = &pb.SandboxController{OrchestratorId: ctrl}
	}
	return sb
}

func rrec(workers ...*pb.Worker) *pb.Orchestrator {
	return &pb.Orchestrator{Id: rOrch, Workers: workers, Lanes: []*pb.Lane{{Id: "ln-a", Name: "a"}, {Id: "ln-b", Name: "b"}}}
}

func rworker(id string, rev, seq uint64) *pb.Worker {
	return &pb.Worker{SandboxId: id, HostId: "h1", Revision: rev, WrittenSeq: seq}
}

func onRoster(rec *pb.Orchestrator, id string) *pb.Worker {
	for _, w := range rec.GetWorkers() {
		if w.GetSandboxId() == id {
			return w
		}
	}
	return nil
}

func event(sb *pb.Sandbox, cache ...*pb.Sandbox) rosterInput {
	return withCache(rosterInput{source: srcEvent, hostID: "h1", records: []*pb.Sandbox{sb}}, cache...)
}

func withCache(in rosterInput, cache ...*pb.Sandbox) rosterInput {
	in.cache = map[string]*pb.Sandbox{}
	for _, c := range cache {
		in.cache[c.GetId()] = c
	}
	return in
}

// TestApplyRecordsFreshnessTable walks the rows of the freshness table.
func TestApplyRecordsFreshnessTable(t *testing.T) {
	type tc struct {
		name    string
		rec     *pb.Orchestrator
		in      rosterInput
		changed bool
		roster  []string // ids on the roster after
		check   func(t *testing.T, rec *pb.Orchestrator, res rosterResult)
	}
	marked := func(rev uint64, removed bool) *pb.Orchestrator {
		r := rrec()
		r.HighWaterMarks = map[string]*pb.HighWaterMark{"s": {Revision: rev, Removed: removed, HostId: "h1"}}
		return r
	}
	cases := []tc{
		{name: "fresh, still ours: cache only", rec: rrec(rworker("s", 3, 1)), in: event(rsb("s", 4, rOrch)), changed: false, roster: []string{"s"}},
		{name: "fresh, another controller: drop + mark", rec: rrec(rworker("s", 3, 1)), in: event(rsb("s", 4, "other")), changed: true, roster: nil,
			check: func(t *testing.T, rec *pb.Orchestrator, res rosterResult) {
				m := rec.GetHighWaterMarks()["s"]
				if m.GetRevision() != 4 || m.GetRemoved() || m.GetHostId() != "h1" || m.GetSetSeq() != rec.GetRosterSeq() {
					t.Errorf("mark = %+v", m)
				}
				if len(res.dropped) != 1 || res.dropped[0].removedEvent {
					t.Errorf("dropped = %+v", res.dropped)
				}
			}},
		{name: "fresh, no controller (non-roster): cache only", rec: rrec(), in: event(rsb("s", 4, "")), changed: false},
		{name: "fresh, names us (non-roster): adopt into the open lane", rec: rrec(), in: event(rsb("s", 4, rOrch)), changed: true, roster: []string{"s"},
			check: func(t *testing.T, rec *pb.Orchestrator, res rosterResult) {
				w := onRoster(rec, "s")
				if w.GetRevision() != 4 || w.GetWrittenSeq() != 1 || len(w.GetLaneIds()) != 0 || w.GetLanesPending() || !slices.Equal(res.adopted, []string{"s"}) {
					t.Errorf("entry = %+v adopted %v", w, res.adopted)
				}
			}},
		{name: "fresh over a mark: adopt, mark deleted", rec: marked(4, false), in: event(rsb("s", 5, rOrch)), changed: true, roster: []string{"s"},
			check: func(t *testing.T, rec *pb.Orchestrator, _ rosterResult) {
				if _, ok := rec.GetHighWaterMarks()["s"]; ok {
					t.Error("adoption should delete the mark")
				}
			}},
		{name: "stale against the mark: ignored", rec: marked(5, false), in: event(rsb("s", 5, rOrch)), changed: false},
		{name: "removed mark: terminal", rec: marked(5, true), in: event(rsb("s", 9, rOrch)), changed: false},
		{name: "stale for the entry", rec: rrec(rworker("s", 6, 1)), in: event(rsb("s", 5, "other")), changed: false, roster: []string{"s"}},
		{name: "repeated", rec: rrec(rworker("s", 6, 1)), in: event(rsb("s", 6, "other")), changed: false, roster: []string{"s"}},
		{name: "newer than the entry, older than the cache", rec: rrec(rworker("s", 3, 1)), in: event(rsb("s", 4, "other"), rsb("s", 5, rOrch)), changed: false, roster: []string{"s"}},
		{name: "revision-0 event never adopts", rec: rrec(), in: event(rsb("s", 0, rOrch)), changed: false},
		{name: "revision-0 event never drops", rec: rrec(rworker("s", 0, 1)), in: event(rsb("s", 0, "")), changed: false, roster: []string{"s"}},
		{name: "reasoning from an event: never", rec: rrec(), in: event(func() *pb.Sandbox {
			sb := rsb("s", 2, rOrch)
			sb.Controller.Reasoning = true
			return sb
		}()), changed: false},
		{name: "removal of an unrelated sandbox: nothing", rec: rrec(), in: rosterInput{source: srcRemoval, hostID: "h1", ids: []string{"x"}}, changed: false},
		{name: "removal of a cached sandbox naming us: removed mark", rec: rrec(),
			in: withCache(rosterInput{source: srcRemoval, hostID: "h1", ids: []string{"s"}}, rsb("s", 3, rOrch)), changed: true,
			check: func(t *testing.T, rec *pb.Orchestrator, res rosterResult) {
				if m := rec.GetHighWaterMarks()["s"]; !m.GetRemoved() || m.GetRevision() != 3 || m.GetHostId() != "h1" {
					t.Errorf("mark = %+v", m)
				}
				if !slices.Contains(res.evict, "s") {
					t.Error("removal evicts the cache entry")
				}
			}},
		{name: "removal of a roster worker: removed mark, evicted", rec: rrec(rworker("s", 3, 1)), in: rosterInput{source: srcRemoval, hostID: "h1", ids: []string{"s"}}, changed: true,
			check: func(t *testing.T, rec *pb.Orchestrator, res rosterResult) {
				if m := rec.GetHighWaterMarks()["s"]; !m.GetRemoved() || m.GetRemovedAt() == nil {
					t.Errorf("mark = %+v", m)
				}
				if len(res.dropped) != 1 || !res.dropped[0].removedEvent || !slices.Contains(res.evict, "s") {
					t.Errorf("res = %+v", res)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := proto.Clone(c.rec).(*pb.Orchestrator)
			res := applyRecords(rec, c.in, rNow)
			if res.changed != c.changed {
				t.Errorf("changed = %v, want %v", res.changed, c.changed)
			}
			var ids []string
			for _, w := range rec.GetWorkers() {
				ids = append(ids, w.GetSandboxId())
			}
			if !slices.Equal(ids, c.roster) {
				t.Errorf("roster = %v, want %v", ids, c.roster)
			}
			if !res.changed && !proto.Equal(rec, c.rec) {
				t.Errorf("no change reported but the record changed:\n%v\n%v", rec, c.rec)
			}
			if c.check != nil {
				c.check(t, rec, res)
			}
		})
	}
}

// A bare revision-0 DESTROYING record (today's destroy emit) for a roster
// worker drops it with a removed mark carrying the entry's host; a replayed
// older event then cannot re-adopt it.
func TestApplyRecordsBareDestroying(t *testing.T) {
	rec := rrec(&pb.Worker{SandboxId: "s", HostId: "h2", Revision: 4, WrittenSeq: 1})
	res := applyRecords(rec, rosterInput{source: srcEvent, hostID: "h2", records: []*pb.Sandbox{{Id: "s", State: pb.SandboxState_SANDBOX_STATE_DESTROYING}}}, rNow)
	if !res.changed || onRoster(rec, "s") != nil || !slices.Contains(res.evict, "s") {
		t.Fatalf("res = %+v roster = %v", res, rec.GetWorkers())
	}
	if m := rec.GetHighWaterMarks()["s"]; !m.GetRemoved() || m.GetHostId() != "h2" || m.GetRevision() != 4 {
		t.Fatalf("mark = %+v", m)
	}
	old := rsb("s", 3, rOrch)
	old.HostId = "h2"
	if res := applyRecords(rec, rosterInput{source: srcEvent, hostID: "h2", records: []*pb.Sandbox{old}}, rNow); res.changed {
		t.Fatal("a replayed older event re-adopted a removed sandbox")
	}
	newer := rsb("s", 9, rOrch)
	if res := applyRecords(rec, rosterInput{source: srcListing, hostID: "h2", records: []*pb.Sandbox{newer}, requestedSeq: rec.GetRosterSeq()}, rNow); onRoster(rec, "s") != nil {
		t.Fatalf("a removed sandbox is never re-adopted: %+v", res)
	}
}

func TestApplyRecordsListing(t *testing.T) {
	// An entry written after the listing was requested is kept; one written
	// before and absent is dropped as removed.
	rec := rrec(rworker("old", 2, 1), rworker("new", 2, 5), &pb.Worker{SandboxId: "far", HostId: "h2", WrittenSeq: 1})
	rec.RosterSeq = 5
	res := applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", requestedSeq: 4}, rNow)
	if onRoster(rec, "old") != nil || onRoster(rec, "new") == nil || onRoster(rec, "far") == nil {
		t.Fatalf("roster = %v", rec.GetWorkers())
	}
	if m := rec.GetHighWaterMarks()["old"]; !m.GetRemoved() || len(res.dropped) != 1 {
		t.Fatalf("mark = %+v dropped %+v", m, res.dropped)
	}
	// A listing of host A never prunes a mark of host B; a stale event from B
	// then cannot re-adopt.
	rec = rrec()
	rec.HighWaterMarks = map[string]*pb.HighWaterMark{"b1": {Revision: 7, HostId: "h2", SetSeq: 1}, "a1": {Revision: 3, HostId: "h1", SetSeq: 1}}
	applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", requestedSeq: 2}, rNow)
	if _, ok := rec.GetHighWaterMarks()["b1"]; !ok {
		t.Fatal("a listing of h1 pruned h2's mark")
	}
	if m := rec.GetHighWaterMarks()["a1"]; !m.GetRemoved() {
		t.Fatalf("a listing of h1 lacking a1 should mark it removed (pruned after the TTL): %+v", m)
	}
	stale := rsb("b1", 6, rOrch)
	stale.HostId = "h2"
	if res := applyRecords(rec, rosterInput{source: srcEvent, hostID: "h2", records: []*pb.Sandbox{stale}}, rNow); res.changed {
		t.Fatal("a stale event from h2 re-adopted")
	}
	// A mark set after the listing was requested survives it.
	rec.HighWaterMarks["a2"] = &pb.HighWaterMark{Revision: 3, HostId: "h1", SetSeq: 9}
	applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", requestedSeq: 2}, rNow)
	if _, ok := rec.GetHighWaterMarks()["a2"]; !ok {
		t.Fatal("a mark newer than the listing's request was pruned")
	}
	// Removed marks expire after removedMarkTTL; listings prune them.
	rec.HighWaterMarks["gone"] = &pb.HighWaterMark{Removed: true, HostId: "h2", RemovedAt: timestamppb.New(rNow.Add(-removedMarkTTL - time.Minute)), SetSeq: 99}
	applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", requestedSeq: 2}, rNow)
	if _, ok := rec.GetHighWaterMarks()["gone"]; ok {
		t.Fatal("expired removed mark kept")
	}
	rec.HighWaterMarks["gone"] = &pb.HighWaterMark{Removed: true, RemovedAt: timestamppb.New(rNow.Add(-removedMarkTTL - time.Minute))}
	if !pruneRemovedMarks(rec, rNow) || pruneRemovedMarks(rec, rNow) {
		t.Fatal("pruneRemovedMarks")
	}
	// A DESTROYING record in a listing is a removal.
	rec = rrec(rworker("d", 2, 1))
	applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", requestedSeq: 5, records: []*pb.Sandbox{{Id: "d", State: pb.SandboxState_SANDBOX_STATE_DESTROYING}}}, rNow)
	if !rec.GetHighWaterMarks()["d"].GetRemoved() {
		t.Fatal("DESTROYING in a listing should be a removal")
	}
	// A status-only listing changes nothing.
	rec = rrec(rworker("s", 2, 1))
	before := proto.Clone(rec)
	if res := applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", requestedSeq: 1, records: []*pb.Sandbox{rsb("s", 2, rOrch)}}, rNow); res.changed || !proto.Equal(before, rec) {
		t.Fatal("a status-only listing wrote")
	}
}

func TestApplyRecordsReplies(t *testing.T) {
	// An attach reply whose sandbox already has a newer cached status record
	// still adopts (from the cached record).
	rec := rrec()
	in := withCache(rosterInput{source: srcAttachReply, hostID: "h1", records: []*pb.Sandbox{rsb("s", 3, rOrch)}}, rsb("s", 5, rOrch))
	if res := applyRecords(rec, in, rNow); !slices.Equal(res.adopted, []string{"s"}) || onRoster(rec, "s").GetRevision() != 5 {
		t.Fatalf("adopted %v entry %+v", res.adopted, onRoster(rec, "s"))
	}
	// Attach then an immediate direct detach: the cached record names no
	// controller and is newer than the reply → not adopted.
	rec = rrec()
	in = withCache(rosterInput{source: srcAttachReply, hostID: "h1", records: []*pb.Sandbox{rsb("s", 3, rOrch)}}, rsb("s", 4, ""))
	if res := applyRecords(rec, in, rNow); res.changed || onRoster(rec, "s") != nil {
		t.Fatal("adopted a sandbox detached right after the attach")
	}
	// A detach reply older than a cached re-attach does not drop the worker.
	rec = rrec(rworker("s", 5, 1))
	in = withCache(rosterInput{source: srcDetachReply, hostID: "h1", records: []*pb.Sandbox{rsb("s", 6, "")}}, rsb("s", 7, rOrch))
	if res := applyRecords(rec, in, rNow); res.changed || onRoster(rec, "s") == nil {
		t.Fatal("an old detach reply dropped a re-attached worker")
	}
	// A launch reply adopts as reasoning, from a newer cached record too.
	rec = rrec()
	reply := rsb("r", 1, rOrch)
	reply.Controller.Reasoning = true
	cached := proto.Clone(reply).(*pb.Sandbox)
	cached.Revision = 3
	res := applyRecords(rec, withCache(rosterInput{source: srcLaunchReply, hostID: "h1", records: []*pb.Sandbox{reply}}, cached), rNow)
	if rec.GetReasoningSandboxId() != "r" || !onRoster(rec, "r").GetReasoning() || onRoster(rec, "r").GetRevision() != 3 || len(res.adopted) != 1 {
		t.Fatalf("reasoning adoption: %+v", rec)
	}
	// A late event from the destroyed reasoning sandbox does not re-adopt it
	// during a rebuild.
	res = applyRecords(rec, rosterInput{source: srcDestroyReasoning, hostID: "h1", ids: []string{"r"}}, rNow)
	if rec.GetReasoningSandboxId() != "" || onRoster(rec, "r") != nil || res.reasoningLost || !rec.GetHighWaterMarks()["r"].GetRemoved() {
		t.Fatalf("destroy: %+v %+v", rec, res)
	}
	late := proto.Clone(cached).(*pb.Sandbox)
	late.Revision = 4
	if res := applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", records: []*pb.Sandbox{late}}, rNow); res.changed && onRoster(rec, "r") != nil {
		t.Fatal("a late record re-adopted the destroyed reasoning sandbox")
	}
	// Listing recovery: an empty reasoning_sandbox_id adopts a listed
	// reasoning record; an event never does unless it is the recorded one.
	rec = rrec()
	r2 := rsb("r2", 2, rOrch)
	r2.Controller.Reasoning = true
	applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", records: []*pb.Sandbox{r2}}, rNow)
	if rec.GetReasoningSandboxId() != "r2" {
		t.Fatal("listing recovery")
	}
	// A listing requested before a reasoning launch commit does not drop the
	// new entry.
	rec = rrec()
	rec.RosterSeq = 3
	applyRecords(rec, withCache(rosterInput{source: srcLaunchReply, hostID: "h1", records: []*pb.Sandbox{reply}}), rNow)
	applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", requestedSeq: 3}, rNow)
	if onRoster(rec, "r") == nil {
		t.Fatal("a stale listing dropped a fresh reasoning entry")
	}
	// The reasoning sandbox leaving the roster otherwise is a loss.
	res = applyRecords(rec, rosterInput{source: srcRemoval, hostID: "h1", ids: []string{"r"}}, rNow)
	if !res.reasoningLost || rec.GetReasoningSandboxId() != "" {
		t.Fatalf("loss not reported: %+v", res)
	}
}

func ticket(id, host, sandbox string, issued time.Time, lanes ...string) *pb.AttachTicket {
	return &pb.AttachTicket{Id: id, HostId: host, SandboxId: sandbox, IssuedAt: timestamppb.New(issued), LaneIds: lanes}
}

func TestApplyRecordsTickets(t *testing.T) {
	withTicket := func(rev uint64, tid string) *pb.Sandbox {
		sb := rsb("s", rev, rOrch)
		sb.Controller.AttachTicket = tid
		return sb
	}
	adopt := func(rec *pb.Orchestrator, src rosterSource, sb *pb.Sandbox) *pb.Worker {
		applyRecords(rec, rosterInput{source: src, hostID: "h1", records: []*pb.Sandbox{sb}}, rNow)
		return onRoster(rec, sb.GetId())
	}
	cases := []struct {
		name    string
		tickets []*pb.AttachTicket
		sb      *pb.Sandbox
		lanes   []string
		pending bool
		left    int
	}{
		{"valid", []*pb.AttachTicket{ticket("t1", "h1", "s", rNow, "ln-a", "ln-x")}, withTicket(2, "t1"), []string{"ln-a"}, false, 0},
		{"expired", []*pb.AttachTicket{ticket("t1", "h1", "s", rNow.Add(-attachTicketTTL-time.Second), "ln-a")}, withTicket(2, "t1"), nil, true, 0},
		{"wrong host", []*pb.AttachTicket{ticket("t1", "h9", "", rNow, "ln-a")}, withTicket(2, "t1"), nil, true, 0},
		{"wrong sandbox", []*pb.AttachTicket{ticket("t1", "h1", "other", rNow, "ln-a")}, withTicket(2, "t1"), nil, true, 0},
		{"consumed", nil, withTicket(2, "t1"), nil, true, 0},
		{"lanes all deleted: open lane", []*pb.AttachTicket{ticket("t1", "h1", "s", rNow, "ln-gone")}, withTicket(2, "t1"), nil, false, 0},
		{"no ticket id: open lane", []*pb.AttachTicket{ticket("t1", "h1", "", rNow, "ln-a")}, withTicket(2, ""), nil, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := rrec()
			rec.AttachTickets = c.tickets
			w := adopt(rec, srcEvent, c.sb)
			if w == nil || !slices.Equal(w.GetLaneIds(), c.lanes) || w.GetLanesPending() != c.pending || len(rec.GetAttachTickets()) != c.left {
				t.Fatalf("entry = %+v tickets = %v", w, rec.GetAttachTickets())
			}
		})
	}
	// Revision-0 hosts drop the ticket id: fail closed.
	t.Run("rev0 sandbox-bound ticket", func(t *testing.T) {
		rec := rrec()
		rec.AttachTickets = []*pb.AttachTicket{ticket("t1", "h1", "s", rNow, "ln-b")}
		w := adopt(rec, srcAttachReply, rsb("s", 0, rOrch))
		if !slices.Equal(w.GetLaneIds(), []string{"ln-b"}) || len(rec.GetAttachTickets()) != 0 {
			t.Fatalf("entry = %+v", w)
		}
	})
	t.Run("rev0 attach reply ignores launch tickets", func(t *testing.T) {
		rec := rrec()
		rec.AttachTickets = []*pb.AttachTicket{ticket("t1", "h1", "", rNow, "ln-b")}
		w := adopt(rec, srcAttachReply, rsb("s", 0, rOrch))
		if w.GetLanesPending() || len(w.GetLaneIds()) != 0 || len(rec.GetAttachTickets()) != 1 {
			t.Fatalf("entry = %+v", w)
		}
	})
	t.Run("rev0 listing consumes the oldest launch ticket within the skew", func(t *testing.T) {
		rec := rrec()
		rec.AttachTickets = []*pb.AttachTicket{
			ticket("late", "h1", "", rNow.Add(-1*time.Minute), "ln-a"),
			ticket("early", "h1", "", rNow.Add(-2*time.Minute), "ln-a"),
		}
		// The host's clock runs 5 min behind the ticket: still within ticketSkew.
		first := rsb("s1", 0, rOrch)
		first.CreatedAt = timestamppb.New(rNow.Add(-7 * time.Minute))
		applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", records: []*pb.Sandbox{first}}, rNow)
		if !onRoster(rec, "s1").GetLanesPending() || len(rec.GetAttachTickets()) != 1 || rec.GetAttachTickets()[0].GetId() != "late" {
			t.Fatalf("first = %+v tickets %v", onRoster(rec, "s1"), rec.GetAttachTickets())
		}
		// 15 min behind: no match, open lane.
		far := rsb("s2", 0, rOrch)
		far.CreatedAt = timestamppb.New(rNow.Add(-16 * time.Minute))
		applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", records: []*pb.Sandbox{far}}, rNow)
		if w := onRoster(rec, "s2"); w.GetLanesPending() || len(rec.GetAttachTickets()) != 1 {
			t.Fatalf("far = %+v", w)
		}
		// A second ticketless adoption consumes the last ticket; a third joins
		// the open lane.
		for _, id := range []string{"s3", "s4"} {
			sb := rsb(id, 0, rOrch)
			sb.CreatedAt = timestamppb.New(rNow)
			applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", records: []*pb.Sandbox{sb}}, rNow)
		}
		if !onRoster(rec, "s3").GetLanesPending() || onRoster(rec, "s4").GetLanesPending() || len(rec.GetAttachTickets()) != 0 {
			t.Fatalf("roster = %v", rec.GetWorkers())
		}
	})
	t.Run("rev0 host: events never adopt or drop, listings do", func(t *testing.T) {
		rec := rrec()
		if res := applyRecords(rec, event(rsb("s", 0, rOrch)), rNow); res.changed {
			t.Fatal("rev-0 event adopted")
		}
		applyRecords(rec, rosterInput{source: srcListing, hostID: "h1", records: []*pb.Sandbox{rsb("s", 0, rOrch)}}, rNow)
		if onRoster(rec, "s") == nil {
			t.Fatal("rev-0 listing should adopt")
		}
		// A detach reply, then a buffered pre-detach event: stays detached.
		applyRecords(rec, rosterInput{source: srcDetachReply, hostID: "h1", records: []*pb.Sandbox{rsb("s", 0, "")}}, rNow)
		if onRoster(rec, "s") != nil || rec.GetHighWaterMarks()["s"] != nil {
			t.Fatal("rev-0 detach reply should drop, leaving no mark")
		}
		if res := applyRecords(rec, event(rsb("s", 0, rOrch)), rNow); res.changed {
			t.Fatal("buffered rev-0 event re-adopted")
		}
	})
	// sweepTickets drops expired tickets only.
	rec := rrec()
	rec.AttachTickets = []*pb.AttachTicket{ticket("a", "h1", "", rNow), ticket("b", "h1", "", rNow.Add(-attachTicketTTL))}
	if !sweepTickets(rec, rNow) || len(rec.GetAttachTickets()) != 1 || sweepTickets(rec, rNow) {
		t.Fatal("sweepTickets")
	}
}

// TestRosterFreshness is the seeded property test: random interleavings of
// attach/detach replies, late, duplicated and reordered events, listings
// requested before or after changes, revision-0 hosts and bare DESTROYING
// records never re-adopt a detached or removed sandbox from an older record,
// and never drop an entry written after a listing was requested.
func TestRosterFreshness(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		rosterTrial(t, seed)
	}
}

type pendingListing struct {
	seq     uint64
	records []*pb.Sandbox
}

// debugSeed, when set to a failing seed, logs that trial's inputs.
var debugSeed int64

func rosterTrial(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	rev0 := rng.Intn(4) == 0 // a host that predates lanes
	ids := []string{"a", "b", "c"}
	truth := map[string]*pb.Sandbox{} // the host's records
	removed := map[string]bool{}
	var history []*pb.Sandbox // every record ever written, for late events
	cache := map[string]*pb.Sandbox{}
	rec := rrec()
	dropRev := map[string]uint64{} // highest revision that dropped each sandbox
	write := func(id string, ctrl string) *pb.Sandbox {
		var rev uint64
		if cur := truth[id]; cur != nil {
			rev = cur.GetRevision()
		}
		sb := rsb(id, rev+1, ctrl)
		if rev0 {
			sb.Revision = 0
		}
		truth[id] = sb
		history = append(history, proto.Clone(sb).(*pb.Sandbox))
		return sb
	}
	observe := func(sb *pb.Sandbox) {
		if sb.GetState() == pb.SandboxState_SANDBOX_STATE_DESTROYING {
			return
		}
		if cur := cache[sb.GetId()]; cur == nil || sb.GetRevision() == 0 || sb.GetRevision() > cur.GetRevision() {
			cache[sb.GetId()] = proto.Clone(sb).(*pb.Sandbox)
		}
	}
	apply := func(in rosterInput) rosterResult {
		for _, sb := range in.records {
			observe(sb)
		}
		in.cache = map[string]*pb.Sandbox{}
		for _, id := range in.rosterIDs() {
			if c := cache[id]; c != nil {
				in.cache[id] = proto.Clone(c).(*pb.Sandbox)
			}
		}
		before := proto.Clone(rec).(*pb.Orchestrator)
		removedMark := map[string]bool{}
		for id, m := range before.GetHighWaterMarks() {
			removedMark[id] = m.GetRemoved()
		}
		res := applyRecords(rec, in, rNow)
		if seed == debugSeed {
			var recs []string
			for _, r := range in.records {
				recs = append(recs, r.GetId()+"@"+string(rune('0'+r.GetRevision()))+":"+r.GetController().GetOrchestratorId())
			}
			t.Logf("src=%d recs=%v ids=%v req=%d → adopted=%v dropped=%+v marks=%v", in.source, recs, in.ids, in.requestedSeq, res.adopted, res.dropped, rec.GetHighWaterMarks())
		}
		for _, id := range res.evict {
			delete(cache, id)
		}
		for _, id := range res.adopted {
			w := onRoster(rec, id)
			// A removal the orchestrator recorded is terminal. (A removal of a
			// sandbox it never tracked is ignored by design; the next
			// listing drops whatever a late record adopts.)
			if removedMark[id] {
				t.Fatalf("seed %d: removed sandbox %s re-adopted", seed, id)
			}
			if !rev0 && w.GetRevision() <= dropRev[id] {
				t.Fatalf("seed %d: %s re-adopted at %d after a drop at %d (source %d)", seed, id, w.GetRevision(), dropRev[id], in.source)
			}
		}
		for _, d := range res.dropped {
			if in.source == srcListing && !slices.Contains(in.rosterIDs(), d.id) {
				if w := onRoster(before, d.id); w.GetWrittenSeq() > in.requestedSeq {
					t.Fatalf("seed %d: listing requested at %d dropped %s written at %d", seed, in.requestedSeq, d.id, w.GetWrittenSeq())
				}
			}
			if m := rec.GetHighWaterMarks()[d.id]; m != nil && m.GetRevision() > dropRev[d.id] {
				dropRev[d.id] = m.GetRevision()
			}
		}
		if res.changed == proto.Equal(before, rec) {
			t.Fatalf("seed %d: changed=%v but record equal=%v", seed, res.changed, proto.Equal(before, rec))
		}
		return res
	}
	var listings []pendingListing
	for step := 0; step < 60; step++ {
		id := ids[rng.Intn(len(ids))]
		switch op := rng.Intn(9); {
		case op == 0 && !removed[id]: // attach through the orchestrator
			apply(rosterInput{source: srcAttachReply, hostID: "h1", records: []*pb.Sandbox{write(id, rOrch)}})
		case op == 1 && !removed[id] && onRoster(rec, id) != nil: // detach through the orchestrator
			apply(rosterInput{source: srcDetachReply, hostID: "h1", records: []*pb.Sandbox{write(id, "")}})
		case op == 2 && !removed[id]: // a direct change on the host (attach elsewhere, detach, launch)
			ctrl := []string{"", rOrch, "other"}[rng.Intn(3)]
			write(id, ctrl)
		case op == 3 && len(history) > 0: // a late, duplicated or reordered event
			apply(rosterInput{source: srcEvent, hostID: "h1", records: []*pb.Sandbox{history[rng.Intn(len(history))]}})
		case op == 4 && len(history) > 0: // the latest event, in order
			if sb := truth[id]; sb != nil {
				apply(rosterInput{source: srcEvent, hostID: "h1", records: []*pb.Sandbox{sb}})
			}
		case op == 5: // request a listing (taken now, applied later)
			var recs []*pb.Sandbox
			for _, sb := range truth {
				recs = append(recs, proto.Clone(sb).(*pb.Sandbox))
			}
			listings = append(listings, pendingListing{seq: rec.GetRosterSeq(), records: recs})
		case op == 6 && len(listings) > 0: // apply a pending listing, possibly late
			i := rng.Intn(len(listings))
			l := listings[i]
			listings = append(listings[:i], listings[i+1:]...)
			apply(rosterInput{source: srcListing, hostID: "h1", requestedSeq: l.seq, records: l.records})
		case op == 7 && truth[id] != nil && rng.Intn(3) == 0: // destroy
			delete(truth, id)
			removed[id] = true
			if rng.Intn(2) == 0 {
				apply(rosterInput{source: srcRemoval, hostID: "h1", ids: []string{id}})
			} else {
				apply(rosterInput{source: srcEvent, hostID: "h1", records: []*pb.Sandbox{{Id: id, State: pb.SandboxState_SANDBOX_STATE_DESTROYING}}})
			}
		}
	}
	// Convergence: a fresh listing makes the roster match the host.
	var recs []*pb.Sandbox
	for _, sb := range truth {
		recs = append(recs, proto.Clone(sb).(*pb.Sandbox))
	}
	apply(rosterInput{source: srcListing, hostID: "h1", requestedSeq: rec.GetRosterSeq(), records: recs})
	for _, id := range ids {
		want := truth[id].GetController().GetOrchestratorId() == rOrch
		got := onRoster(rec, id) != nil
		if removed[id] && truth[id] == nil {
			want = false
		}
		// A sandbox dropped at a revision it never beat afterwards stays
		// dropped until a newer record names us; with truth naming us at a
		// newer revision a fresh listing must adopt it.
		if got != want && (!want || rev0 || truth[id].GetRevision() > dropRev[id]) {
			t.Fatalf("seed %d: %s on roster = %v, host says %v (rev %d, drop %d)", seed, id, got, want, truth[id].GetRevision(), dropRev[id])
		}
	}
}

// Another host cannot drop, remove or mark a worker that lives on h1, nor
// adopt over h1's mark (PR #24 review: a host speaks only for its own
// sandboxes).
func TestApplyRecordsIgnoresForeignHosts(t *testing.T) {
	foreign := func(in rosterInput) rosterInput { in.hostID = "h2"; return in }
	rec := rrec(rworker("s", 3, 1))
	res := applyRecords(rec, foreign(event(rsb("s", ^uint64(0), "other"))), rNow)
	if res.changed || onRoster(rec, "s") == nil || rec.GetHighWaterMarks()["s"] != nil {
		t.Fatalf("a foreign event dropped or marked the worker: %+v", rec)
	}
	res = applyRecords(rec, rosterInput{source: srcRemoval, hostID: "h2", ids: []string{"s"}}, rNow)
	if res.changed || onRoster(rec, "s") == nil {
		t.Fatalf("a foreign removal dropped the worker: %+v", rec)
	}
	marked := rrec()
	marked.HighWaterMarks = map[string]*pb.HighWaterMark{"s": {Revision: 5, HostId: "h1"}}
	res = applyRecords(marked, foreign(event(rsb("s", 9, rOrch))), rNow)
	if res.changed || onRoster(marked, "s") != nil {
		t.Fatalf("a foreign event adopted over h1's mark: %+v", marked)
	}
	if !foreignTo(rec, "s", "h2") || foreignTo(rec, "s", "h1") || foreignTo(rec, "s", "") || foreignTo(rec, "new", "h2") {
		t.Error("foreignTo")
	}
	// h1 itself still applies.
	if res := applyRecords(rec, event(rsb("s", 4, "other")), rNow); !res.changed || onRoster(rec, "s") != nil {
		t.Errorf("h1's own event should drop: %+v", rec)
	}
}
