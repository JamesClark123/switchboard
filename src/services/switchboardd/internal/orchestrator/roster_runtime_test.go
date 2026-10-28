package orchestrator

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// SWI-18 T009a–T014: the runtime's roster paths go through applyRecords, no
// lock is held across the network, and pushes run in the background.

func rosterIDs(rec *pb.Orchestrator) []string {
	var ids []string
	for _, w := range rec.GetWorkers() {
		ids = append(ids, w.GetSandboxId())
	}
	return ids
}

func ours(o *pb.Orchestrator, sb *pb.Sandbox) *pb.Sandbox {
	sb.Controller = &pb.SandboxController{OrchestratorId: o.GetId(), Name: "review-bot", HostId: "host-m"}
	return sb
}

// T009a: attach then detach round trip through the engine; the detach leaves
// a high-water mark at the reply's revision.
func TestAttachDetachLeavesMark(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("wa", "a"))
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wa", nil); err != nil {
		t.Fatal(err)
	}
	entry := onRoster(rt.record(), "wa")
	if entry == nil || entry.GetRevision() != w.get("wa").GetRevision() || entry.GetWrittenSeq() == 0 {
		t.Fatalf("entry = %+v", entry)
	}
	quiesce(t, rt)
	if err := env.svc.DetachWorker(bg(), o.GetId(), "wa", nil); err != nil {
		t.Fatal(err)
	}
	rec := rt.record()
	if onRoster(rec, "wa") != nil {
		t.Fatal("still on the roster")
	}
	if m := rec.GetHighWaterMarks()["wa"]; m == nil || m.GetRemoved() || m.GetRevision() != w.get("wa").GetRevision() || m.GetHostId() != "box" {
		t.Fatalf("mark = %+v (host revision %d)", m, w.get("wa").GetRevision())
	}
	// A stale event from before the detach cannot re-adopt it.
	stale := ours(o, running("wa", "a"))
	stale.Revision = 1
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: stale}}
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: running("other", "o")}}
	time.Sleep(50 * time.Millisecond)
	if onRoster(rt.record(), "wa") != nil {
		t.Fatal("a stale event re-adopted a detached worker")
	}
}

// T010: an event arriving while a listing is in flight is not lost, and no
// lock is held across ListSandboxes (a record write succeeds meanwhile).
func TestReconcileHoldsNoLockAcrossListing(t *testing.T) {
	_, w, o, rt := remoteEnv(t)
	listed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.mu.Lock()
	w.onList = func() {
		once.Do(func() {
			close(listed)
			<-release
		})
	}
	w.mu.Unlock()
	api, _ := rt.apiFor("box")
	done := make(chan error, 1)
	go func() { done <- rt.reconcile(bg(), "box", api, false) }()
	<-listed
	// A write commits while the listing is blocked.
	if _, err := rt.update(func(x *pb.Orchestrator) error { x.Name = "renamed-during-list"; return nil }); err != nil {
		t.Fatal(err)
	}
	// A launched worker's first event adopts it while the listing is out.
	w.put(ours(o, running("nw", "new")))
	sb := w.get("nw")
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: sb}}
	waitFor(t, func() bool { return onRoster(rt.record(), "nw") != nil })
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The listing (requested before the adoption) did not drop it.
	if onRoster(rt.record(), "nw") == nil {
		t.Fatalf("roster after reconcile = %v", rosterIDs(rt.record()))
	}
}

// T010: an events_dropped marker on the link's subscription re-lists the host
// (coalesced) and pushes no names or briefings.
func TestEventsDroppedMarkerResyncs(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("wa", "a"))
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wa", nil); err != nil {
		t.Fatal(err)
	}
	quiesce(t, rt)
	before, briefs := w.listCount(), w.countPrefix("update wa name=\"review-bot\" briefing=true")
	// The worker was detached directly on the host while events were lost.
	w.update("wa", func(sb *pb.Sandbox) { sb.Controller = nil })
	w.events <- &pb.Event{Event: &pb.Event_EventsDropped{EventsDropped: &pb.EventsDropped{}}}
	waitFor(t, func() bool { return onRoster(rt.record(), "wa") == nil })
	if w.listCount() != before+1 {
		t.Errorf("lists = %d, want %d", w.listCount(), before+1)
	}
	quiesce(t, rt)
	if got := w.countPrefix("update wa name=\"review-bot\" briefing=true"); got != briefs {
		t.Errorf("a marker resync pushed briefings: %d → %d", briefs, got)
	}
	_ = o
}

// T011: events adopt and drop; a stale event is ignored; a removal is
// terminal and evicts the cache.
func TestEventsAdoptAndDrop(t *testing.T) {
	_, w, o, rt := remoteEnv(t)
	w.put(ours(o, running("ev", "launched")))
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: w.get("ev")}}
	waitFor(t, func() bool { return onRoster(rt.record(), "ev") != nil })
	old := w.get("ev")
	// The controller changes on the host: the next event drops it.
	gone := w.update("ev", func(sb *pb.Sandbox) { sb.Controller = &pb.SandboxController{OrchestratorId: "someone-else"} })
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: gone}}
	waitFor(t, func() bool { return onRoster(rt.record(), "ev") == nil })
	// The older record arriving late is ignored.
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: old}}
	back := w.update("ev", func(sb *pb.Sandbox) { ours(o, sb) })
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: back}}
	waitFor(t, func() bool { return onRoster(rt.record(), "ev") != nil })
	if onRoster(rt.record(), "ev").GetRevision() != back.GetRevision() {
		t.Fatalf("re-adopted from %d, want %d", onRoster(rt.record(), "ev").GetRevision(), back.GetRevision())
	}
	// Removal: terminal, cache gone.
	w.events <- &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: "ev"}}}
	waitFor(t, func() bool { return onRoster(rt.record(), "ev") == nil && rt.sandboxRecord("ev") == nil })
	if !rt.record().GetHighWaterMarks()["ev"].GetRemoved() {
		t.Fatal("removal should leave a removed mark")
	}
	newer := proto.Clone(back).(*pb.Sandbox)
	newer.Revision += 10
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: newer}}
	time.Sleep(50 * time.Millisecond)
	if onRoster(rt.record(), "ev") != nil {
		t.Fatal("a removed sandbox was re-adopted")
	}
}

// T013: a save returns while a host blocks the briefing push, and ten
// triggers coalesce into at most two pushes per worker.
func TestBriefingPushIsBackgroundAndCoalesced(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("wa", "a"))
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wa", nil); err != nil {
		t.Fatal(err)
	}
	quiesce(t, rt)
	block := make(chan struct{})
	entered := make(chan struct{}, 64)
	w.mu.Lock()
	w.onUpdate = func(req *pb.UpdateWorkerRequest) error {
		if req.Briefing != nil {
			entered <- struct{}{}
			<-block
		}
		return nil
	}
	w.mu.Unlock()
	before := w.countPrefix("update wa name=")
	saved := make(chan error, 1)
	go func() { _, err := putAction(env.svc, o.GetId(), reviewPR()); saved <- err }()
	select {
	case err := <-saved:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(testWaitTimeout):
		t.Fatal("PutAction waited on a blocked host")
	}
	<-entered
	for i := 0; i < 10; i++ {
		rt.requestBriefing()
	}
	close(block)
	quiesce(t, rt)
	if n := w.countPrefix("update wa name=") - before; n < 1 || n > 2 {
		t.Errorf("briefing pushes = %d, want 1..2", n)
	}
}

// T014: an adopted worker gets one lanes push (so lanes_known is set); a
// marked worker's display follows its lanes; a transiently failing push is
// retried; the reconnect push carries lanes.
func TestLaneDisplayPushes(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("wa", "a"))
	w.put(running("wb", "b"))
	for _, id := range []string{"wa", "wb"} {
		if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", id, nil); err != nil {
			t.Fatal(err)
		}
	}
	quiesce(t, rt)
	if c := w.get("wa").GetController(); !c.GetLanesKnown() || len(c.GetLaneNames()) != 0 {
		t.Fatalf("adoption push: %+v", c)
	}
	// wa joins lane fast; only wa is marked and pushed.
	if _, err := rt.update(func(x *pb.Orchestrator) error {
		x.Lanes = []*pb.Lane{{Id: "ln-1", Name: "fast"}}
		onRoster(x, "wa").LaneIds = []string{"ln-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The first push fails transiently and is retried.
	var fails sync.Once
	w.mu.Lock()
	w.onUpdate = func(req *pb.UpdateWorkerRequest) error {
		if req.GetLanes() != nil && req.GetSandboxId() == "wa" {
			var err error
			fails.Do(func() { err = status.Error(codes.Unavailable, "blip") })
			return err
		}
		return nil
	}
	w.mu.Unlock()
	wbPushes := w.countPrefix("update wb")
	rt.markLanes("wa")
	waitFor(t, func() bool { return slices.Equal(w.get("wa").GetController().GetLaneNames(), []string{"fast"}) })
	quiesce(t, rt)
	if w.countPrefix("update wb") != wbPushes {
		t.Error("an unmarked worker was pushed")
	}
	// A reconnect pushes every worker's lanes with the name and briefing.
	w.update("wa", func(sb *pb.Sandbox) { sb.Controller.LaneNames = nil })
	api, _ := rt.apiFor("box")
	rt.pushWorkerStateTo(context.Background(), "box", api)
	if !slices.Equal(w.get("wa").GetController().GetLaneNames(), []string{"fast"}) {
		t.Fatalf("reconnect push: %+v", w.get("wa").GetController())
	}
}

// T014: a lane push and a reconnect push racing leave the latest lanes on
// the host, because both push under the worker's pushMu and read the record
// while holding it.
func TestLanePushRaceKeepsLatest(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("wa", "a"))
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wa", nil); err != nil {
		t.Fatal(err)
	}
	quiesce(t, rt)
	api, _ := rt.apiFor("box")
	for i := 0; i < 20; i++ {
		name := []string{"red", "blue"}[i%2]
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = rt.update(func(x *pb.Orchestrator) error {
				x.Lanes = []*pb.Lane{{Id: "ln-1", Name: name}}
				onRoster(x, "wa").LaneIds = []string{"ln-1"}
				return nil
			})
			rt.markLanes("wa")
		}()
		go func() { defer wg.Done(); rt.pushWorkerStateTo(bg(), "box", api) }()
		wg.Wait()
		quiesce(t, rt)
		if got := w.get("wa").GetController().GetLaneNames(); !slices.Equal(got, []string{name}) {
			t.Fatalf("round %d: host shows %v, want %s", i, got, name)
		}
	}
}
