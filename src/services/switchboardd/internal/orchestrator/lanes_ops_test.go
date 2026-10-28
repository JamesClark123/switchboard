package orchestrator

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Lane writes (US3–US6): lanes, membership, guarded saves, tickets and the
// retired worker tag.

func laned(name string, mode pb.LaneMode, laneIDs ...string) *pb.Action {
	a := reviewPR()
	a.Name, a.LaneIds, a.LaneMode = name, laneIDs, mode
	return a
}

func TestPutLaneCreateRenameAndRefusals(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	fe, err := env.svc.PutLane(id, "", "frontend", "  UI work  ")
	if err != nil || !strings.HasPrefix(fe.GetId(), "ln-") || fe.GetDescription() != "UI work" || fe.GetCreatedAt() == nil {
		t.Fatalf("create = %+v / %v", fe, err)
	}
	if _, err := env.svc.PutLane(id, "", "frontend", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name = %v", err)
	}
	for _, bad := range []string{"", "Front End", "open", strings.Repeat("a", 65)} {
		if _, err := env.svc.PutLane(id, "", bad, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("name %q = %v", bad, err)
		}
	}
	if _, err := env.svc.PutLane(id, "", "x", strings.Repeat("d", 2000)); !errors.Is(err, ErrInvalid) {
		t.Errorf("long description = %v", err)
	}
	if _, err := env.svc.PutLane(id, "ln-nope", "x", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown lane = %v", err)
	}
	if _, err := env.svc.PutLane("nope", "", "x", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
	// Rename keeps the id; members and actions follow it.
	seedLanes(t, rt, map[string][]string{"frontend": {"wa"}})
	if _, err := putAction(env.svc, id, laned("ui", pb.LaneMode_LANE_MODE_EXCLUSIVE, fe.GetId())); err != nil {
		t.Fatal(err)
	}
	be, _ := env.svc.PutLane(id, "", "backend", "")
	if _, err := env.svc.PutLane(id, fe.GetId(), "backend", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("rename onto a taken name = %v", err)
	}
	web, err := env.svc.PutLane(id, fe.GetId(), "web", "")
	if err != nil || web.GetId() != fe.GetId() || web.GetName() != "web" {
		t.Fatalf("rename = %+v / %v", web, err)
	}
	rec := rt.record()
	if v := rt.workerView("wa"); !slices.Equal(v.GetLaneIds(), []string{fe.GetId()}) {
		t.Errorf("member lanes after rename = %v", v.GetLaneIds())
	}
	if !strings.Contains(renderBriefing(rec, false), "lanes web") && !strings.Contains(renderBriefing(rec, false), "lane web") {
		t.Errorf("briefing after rename:\n%s", renderBriefing(rec, false))
	}
	_ = be
	// The cap.
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		for i := len(o.Lanes); i < maxLanes; i++ {
			o.Lanes = append(o.Lanes, &pb.Lane{Id: fmt.Sprintf("ln-%d", i), Name: fmt.Sprintf("l%d", i)})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.PutLane(id, "", "one-more", ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), msgLaneCap) {
		t.Errorf("cap = %v", err)
	}
}

func TestPutActionLaneRefusals(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"ci": nil})
	put := func(req *pb.PutActionRequest) error {
		req.OrchestratorId = id
		_, err := env.svc.PutAction(req)
		return err
	}
	tagged := reviewPR()
	tagged.WorkerTag = "ci" //nolint:staticcheck // SA1019: an old client's request
	cases := map[string]struct {
		req  *pb.PutActionRequest
		want error
		text string
	}{
		"old client":         {&pb.PutActionRequest{Action: reviewPR()}, ErrPrecondition, msgOldClient},
		"worker tag":         {&pb.PutActionRequest{Action: tagged, SetsLanes: true}, ErrPrecondition, msgOldClient},
		"unknown lane":       {&pb.PutActionRequest{Action: laned("x", pb.LaneMode_LANE_MODE_EXCLUSIVE, "ln-gone"), SetsLanes: true}, ErrNotFound, "no longer exists"},
		"lanes with no mode": {&pb.PutActionRequest{Action: laned("x", pb.LaneMode_LANE_MODE_UNSPECIFIED, ids["ci"]), SetsLanes: true}, ErrInvalid, msgLaneMode},
		"reason with lanes": {&pb.PutActionRequest{Action: &pb.Action{
			Name: "think", Type: pb.ActionType_ACTION_TYPE_REASON, Body: "b", LaneIds: []string{ids["ci"]}, LaneMode: pb.LaneMode_LANE_MODE_EXCLUSIVE,
		}, SetsLanes: true}, ErrInvalid, msgReasonLanes},
		"invalid action": {&pb.PutActionRequest{Action: &pb.Action{Name: "bad"}, SetsLanes: true}, ErrInvalid, ""},
	}
	for name, c := range cases {
		if err := put(c.req); !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s = %v, want %v containing %q", name, err, c.want, c.text)
		}
	}
	// Duplicates are folded and an un-laned action's mode cleared.
	a := laned("dup", pb.LaneMode_LANE_MODE_INCLUSIVE, ids["ci"], ids["ci"])
	saved, err := putAction(env.svc, id, a)
	if err != nil || !slices.Equal(saved.GetLaneIds(), []string{ids["ci"]}) {
		t.Fatalf("dup = %+v / %v", saved, err)
	}
	plain := laned("plain", pb.LaneMode_LANE_MODE_INCLUSIVE)
	if saved, err := putAction(env.svc, id, plain); err != nil || saved.GetLaneMode() != pb.LaneMode_LANE_MODE_UNSPECIFIED {
		t.Fatalf("plain = %+v / %v", saved, err)
	}
	// A re-save keeps created_at.
	again, _ := putAction(env.svc, id, plain)
	if again.GetCreatedAt().AsTime() != saved.GetCreatedAt().AsTime() {
		t.Error("re-save changed created_at")
	}
}

func TestOpenLaneGuard(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	if _, err := putAction(env.svc, id, reviewPR()); err != nil {
		t.Fatal(err)
	}
	ln, _ := env.svc.PutLane(id, "", "ci", "")
	req := &pb.SetWorkerLanesRequest{OrchestratorId: id, SandboxId: "wa", LaneIds: []string{ln.GetId()}}
	// The check reports the list; the write is refused without confirming it.
	impact, err := env.svc.CheckLaneChange(&pb.CheckLaneChangeRequest{OrchestratorId: id, Change: &pb.CheckLaneChangeRequest_SetWorkerLanes{SetWorkerLanes: req}})
	if err != nil || !slices.Equal(impact.GetUnlanedActions(), []string{"review-pr"}) {
		t.Fatalf("check = %+v / %v", impact, err)
	}
	_, err = env.svc.SetWorkerLanes(req)
	var ge *GuardError
	if !errors.As(err, &ge) || !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "review-pr") {
		t.Fatalf("unconfirmed = %v", err)
	}
	st := ge.GRPCStatus()
	if st.Code() != codes.FailedPrecondition || len(st.Details()) != 1 {
		t.Errorf("status = %v %v", st.Code(), st.Details())
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Errorf("FromError = %v", s)
	}
	if v := rt.workerView("wa"); len(v.GetLaneIds()) != 0 {
		t.Errorf("refused write changed lanes: %v", v.GetLaneIds())
	}
	// A confirmation covering the list lets it through.
	req.Guard = &pb.LaneGuardConfirm{UnlanedActions: impact.GetUnlanedActions()}
	v, err := env.svc.SetWorkerLanes(req)
	if err != nil || !slices.Equal(v.GetLaneIds(), []string{ln.GetId()}) {
		t.Fatalf("confirmed = %+v / %v", v, err)
	}
	// Saving an un-laned action with no open-lane worker is guarded too.
	other := reviewPR()
	other.Name = "other"
	if _, err := env.svc.PutAction(&pb.PutActionRequest{OrchestratorId: id, Action: other, SetsLanes: true}); !errors.As(err, &ge) || !slices.Contains(ge.Impact.GetUnlanedActions(), "other") {
		t.Errorf("un-laned save = %v", err)
	}
	if _, err := putAction(env.svc, id, other); err != nil {
		t.Errorf("confirmed un-laned save = %v", err)
	}
	// A laned save is not guarded.
	if _, err := env.svc.PutAction(&pb.PutActionRequest{OrchestratorId: id, Action: laned("ci-only", pb.LaneMode_LANE_MODE_EXCLUSIVE, ln.GetId()), SetsLanes: true}); err != nil {
		t.Errorf("laned save = %v", err)
	}
}

func TestSetWorkerLanesStaleAndRefusals(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 2, true)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": {"wa"}, "b": nil})
	set := func(req *pb.SetWorkerLanesRequest) error {
		req.OrchestratorId = id
		_, err := env.svc.SetWorkerLanes(req)
		return err
	}
	if err := set(&pb.SetWorkerLanesRequest{SandboxId: "wa", LaneIds: []string{ids["b"]}}); !errors.Is(err, ErrAborted) || !strings.Contains(err.Error(), "changed since the picker opened") {
		t.Errorf("stale = %v", err)
	}
	if err := set(&pb.SetWorkerLanesRequest{SandboxId: "wa", LaneIds: []string{"ln-gone"}, ExpectedLaneIds: []string{ids["a"]}}); !errors.Is(err, ErrAborted) || !strings.Contains(err.Error(), "was deleted") {
		t.Errorf("deleted lane = %v", err)
	}
	if err := set(&pb.SetWorkerLanesRequest{SandboxId: "nope"}); !errors.Is(err, ErrAborted) {
		t.Errorf("not a worker = %v", err)
	}
	if err := set(&pb.SetWorkerLanesRequest{SandboxId: "reasoning-1", LaneIds: []string{ids["a"]}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("reasoning = %v", err)
	}
	if err := set(&pb.SetWorkerLanesRequest{SandboxId: "wa", LaneIds: []string{ids["a"], ids["b"], ids["b"]}, ExpectedLaneIds: []string{ids["a"]}}); err != nil {
		t.Fatal(err)
	}
	if v := rt.workerView("wa"); !slices.Equal(v.GetLaneIds(), []string{ids["a"], ids["b"]}) {
		t.Errorf("lanes = %v", v.GetLaneIds())
	}
	// Choosing lanes for a lanes-pending worker clears the flag.
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		onRosterEntry(o, "wb").LanesPending = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := set(&pb.SetWorkerLanesRequest{SandboxId: "wb", LaneIds: []string{ids["b"]}}); !errors.Is(err, ErrAborted) {
		t.Errorf("pending flag not expected = %v", err)
	}
	if err := set(&pb.SetWorkerLanesRequest{SandboxId: "wb", ExpectedLanesPending: true, Guard: &pb.LaneGuardConfirm{}}); err != nil {
		t.Fatal(err)
	}
	if v := rt.workerView("wb"); v.GetLanesPending() || len(v.GetLaneIds()) != 0 {
		t.Errorf("open lane = %+v", v)
	}
	if _, err := env.svc.SetWorkerLanes(&pb.SetWorkerLanesRequest{OrchestratorId: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
}

func TestSetLaneMembers(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 3, true)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": {"wa"}})
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		onRosterEntry(o, "wc").LanesPending = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	set := func(members, expected []string) error {
		_, err := env.svc.SetLaneMembers(&pb.SetLaneMembersRequest{OrchestratorId: id, LaneId: ids["a"], SandboxIds: members, ExpectedSandboxIds: expected, Guard: &pb.LaneGuardConfirm{}})
		return err
	}
	if err := set([]string{"wb"}, nil); !errors.Is(err, ErrAborted) || !strings.Contains(err.Error(), "members changed") {
		t.Errorf("stale = %v", err)
	}
	if err := set([]string{"nope"}, []string{"wa"}); !errors.Is(err, ErrAborted) {
		t.Errorf("unknown worker = %v", err)
	}
	if err := set([]string{"reasoning-1"}, []string{"wa"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("reasoning = %v", err)
	}
	if _, err := env.svc.SetLaneMembers(&pb.SetLaneMembersRequest{OrchestratorId: id, LaneId: "ln-gone"}); !errors.Is(err, ErrAborted) {
		t.Errorf("deleted lane = %v", err)
	}
	l, err := env.svc.SetLaneMembers(&pb.SetLaneMembersRequest{OrchestratorId: id, LaneId: ids["a"], SandboxIds: []string{"wb", "wc", "wb"}, ExpectedSandboxIds: []string{"wa"}})
	if err != nil || l.GetId() != ids["a"] {
		t.Fatalf("save = %+v / %v", l, err)
	}
	for sb, want := range map[string]bool{"wa": false, "wb": true, "wc": true} {
		v := rt.workerView(sb)
		if slices.Contains(v.GetLaneIds(), ids["a"]) != want || v.GetLanesPending() {
			t.Errorf("%s = %+v", sb, v)
		}
	}
	if _, err := env.svc.SetLaneMembers(&pb.SetLaneMembersRequest{OrchestratorId: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
}

func TestDeleteLaneImpactAndStrip(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": {"wa"}, "b": nil})
	setAgent(rt, w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING)
	if _, err := putAction(env.svc, id, laned("only-a", pb.LaneMode_LANE_MODE_EXCLUSIVE, ids["a"])); err != nil {
		t.Fatal(err)
	}
	if _, err := putAction(env.svc, id, laned("a-or-b", pb.LaneMode_LANE_MODE_INCLUSIVE, ids["a"], ids["b"])); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CallAction(id, "only-a", map[string]string{"number": "1", "repo": "r"}, pb.EventKind_EVENT_KIND_USER, "", ""); err != nil {
		t.Fatal(err)
	}
	settle(t, rt)
	impact, err := env.svc.CheckLaneChange(&pb.CheckLaneChangeRequest{OrchestratorId: id, Change: &pb.CheckLaneChangeRequest_DeleteLane{DeleteLane: &pb.DeleteLaneRequest{LaneId: ids["a"]}}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(impact.GetWorkers(), []string{"worker-wa"}) || len(impact.GetActions()) != 2 ||
		!slices.Equal(impact.GetActionsLeftUnlaned(), []string{"only-a"}) || impact.GetReroutedRuns() != 1 {
		t.Errorf("impact = %+v", impact)
	}
	applied, err := env.svc.DeleteLane(id, ids["a"], &pb.LaneGuardConfirm{UnlanedActions: []string{"only-a"}})
	if err != nil || applied.GetReroutedRuns() != 1 {
		t.Fatalf("delete = %+v / %v", applied, err)
	}
	rec := rt.record()
	if laneByID(rec, ids["a"]) != nil || len(onRosterEntry(rec, "wa").GetLaneIds()) != 0 {
		t.Errorf("lane left behind: %+v", rec)
	}
	if a := findAction(rec, "only-a"); len(a.GetLaneIds()) != 0 || a.GetLaneMode() != pb.LaneMode_LANE_MODE_UNSPECIFIED {
		t.Errorf("only-a = %+v", a)
	}
	if a := findAction(rec, "a-or-b"); !slices.Equal(a.GetLaneIds(), []string{ids["b"]}) {
		t.Errorf("a-or-b = %+v", a)
	}
	if _, err := env.svc.DeleteLane(id, ids["a"], nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again = %v", err)
	}
	if _, err := env.svc.DeleteLane("nope", ids["a"], nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
}

func TestDeleteActionKeepsLaneSetting(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": nil, "b": nil})
	if _, err := putAction(env.svc, id, laned("review-pr", pb.LaneMode_LANE_MODE_EXCLUSIVE, ids["a"])); err != nil {
		t.Fatal(err)
	}
	run := call(t, env, id, "1")
	if _, err := env.svc.DeleteAction(id, "review-pr"); err != nil {
		t.Fatal(err)
	}
	got, _ := env.svc.GetRun(id, run.GetId())
	if kept := got.GetKeptLanes(); kept == nil || !slices.Equal(kept.GetLaneIds(), []string{ids["a"]}) || kept.GetMode() != pb.LaneMode_LANE_MODE_EXCLUSIVE {
		t.Fatalf("kept = %+v", got.GetKeptLanes())
	}
	// A new action with the same name does not change the kept routing.
	if _, err := putAction(env.svc, id, reviewPR()); err != nil {
		t.Fatal(err)
	}
	settle(t, rt)
	got, _ = env.svc.GetRun(id, run.GetId())
	if got.GetState() != pb.RunState_RUN_STATE_QUEUED || got.GetWaitingReason() != "waiting for a worker in a" {
		t.Errorf("kept run = %v %q", got.GetState(), got.GetWaitingReason())
	}
	// Deleting the kept lane strips the copy (empty: un-laned) and counts the run.
	applied, err := env.svc.DeleteLane(id, ids["a"], &pb.LaneGuardConfirm{})
	if err != nil || applied.GetReroutedRuns() != 1 {
		t.Fatalf("delete lane = %+v / %v", applied, err)
	}
	settle(t, rt)
	waitFor(t, func() bool {
		got, _ := env.svc.GetRun(id, run.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING && got.GetSandboxId() == "wa"
	})
	if _, err := env.svc.DeleteAction(id, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown action = %v", err)
	}
}

func TestCheckLaneChangeRequests(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": nil})
	if _, err := putAction(env.svc, id, reviewPR()); err != nil {
		t.Fatal(err)
	}
	check := func(c *pb.CheckLaneChangeRequest) (*pb.LaneChangeImpact, error) {
		c.OrchestratorId = id
		return env.svc.CheckLaneChange(c)
	}
	if imp, err := check(&pb.CheckLaneChangeRequest{}); err != nil || guardFired(imp) {
		t.Errorf("no change = %+v / %v", imp, err)
	}
	if _, err := check(&pb.CheckLaneChangeRequest{Change: &pb.CheckLaneChangeRequest_DeleteLane{DeleteLane: &pb.DeleteLaneRequest{OrchestratorId: "other"}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("nested id = %v", err)
	}
	imp, err := check(&pb.CheckLaneChangeRequest{Change: &pb.CheckLaneChangeRequest_SetLaneMembers{SetLaneMembers: &pb.SetLaneMembersRequest{LaneId: ids["a"], SandboxIds: []string{"wa"}}}})
	if err != nil || !slices.Equal(imp.GetUnlanedActions(), []string{"review-pr"}) {
		t.Errorf("members = %+v / %v", imp, err)
	}
	imp, err = check(&pb.CheckLaneChangeRequest{Change: &pb.CheckLaneChangeRequest_DetachWorker{DetachWorker: &pb.DetachWorkerRequest{SandboxId: "wa"}}})
	if err != nil || !slices.Equal(imp.GetUnlanedActions(), []string{"review-pr"}) {
		t.Errorf("detach = %+v / %v", imp, err)
	}
	if _, err := check(&pb.CheckLaneChangeRequest{Change: &pb.CheckLaneChangeRequest_DetachWorker{DetachWorker: &pb.DetachWorkerRequest{SandboxId: "nope"}}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("detach unknown = %v", err)
	}
	if _, err := check(&pb.CheckLaneChangeRequest{Change: &pb.CheckLaneChangeRequest_PutAction{PutAction: &pb.PutActionRequest{Action: reviewPR()}}}); !errors.Is(err, ErrPrecondition) {
		t.Errorf("old-client put = %v", err)
	}
	for _, c := range []*pb.CheckLaneChangeRequest{
		{Change: &pb.CheckLaneChangeRequest_SetWorkerLanes{SetWorkerLanes: &pb.SetWorkerLanesRequest{OrchestratorId: "x"}}},
		{Change: &pb.CheckLaneChangeRequest_SetLaneMembers{SetLaneMembers: &pb.SetLaneMembersRequest{OrchestratorId: "x"}}},
		{Change: &pb.CheckLaneChangeRequest_PutAction{PutAction: &pb.PutActionRequest{OrchestratorId: "x"}}},
		{Change: &pb.CheckLaneChangeRequest_DetachWorker{DetachWorker: &pb.DetachWorkerRequest{OrchestratorId: "x"}}},
	} {
		if _, err := check(c); !errors.Is(err, ErrInvalid) {
			t.Errorf("nested id %T = %v", c.GetChange(), err)
		}
	}
	if _, err := env.svc.CheckLaneChange(&pb.CheckLaneChangeRequest{OrchestratorId: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
}

func TestDetachGuardAndMark(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	if _, err := putAction(env.svc, id, reviewPR()); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.DetachWorker(bg(), id, "wa", nil); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "review-pr") {
		t.Fatalf("unconfirmed = %v", err)
	}
	if rt.isDetaching("wa") {
		t.Error("a refused detach left the mark")
	}
	// A worker being delivered to is not marked.
	rt.mu.Lock()
	rt.delivering["wa"] = struct{}{}
	rt.mu.Unlock()
	if err := env.svc.DetachWorker(bg(), id, "wa", &pb.LaneGuardConfirm{UnlanedActions: []string{"review-pr"}}); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "receiving a run") {
		t.Errorf("mid-delivery = %v", err)
	}
	rt.endDelivery("wa")
	if err := env.svc.DetachWorker(bg(), id, "wa", &pb.LaneGuardConfirm{UnlanedActions: []string{"review-pr"}}); err != nil {
		t.Fatal(err)
	}
	if rt.isDetaching("wa") || onRosterEntry(rt.record(), "wa") != nil {
		t.Error("detach left the worker or its mark")
	}
}

func TestAttachWithLanesUsesATicket(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": nil})
	w.put(running("sb-1", "one"))
	if _, err := env.svc.AttachWorker(bg(), id, "box", "sb-1", []string{"ln-gone"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown lane = %v", err)
	}
	v, err := env.svc.AttachWorker(bg(), id, "box", "sb-1", []string{ids["a"]})
	if err != nil || !slices.Equal(v.GetLaneIds(), []string{ids["a"]}) || v.GetLanesPending() {
		t.Fatalf("attach = %+v / %v", v, err)
	}
	if n := len(rt.record().GetAttachTickets()); n != 0 {
		t.Errorf("ticket not consumed: %d left", n)
	}
	if _, err := env.svc.AttachWorker(bg(), id, "box", "sb-1", []string{ids["a"]}); !errors.Is(err, ErrPrecondition) {
		t.Errorf("lanes for a roster worker = %v", err)
	}
	// A definite refusal deletes the ticket.
	if _, err := env.svc.AttachWorker(bg(), id, "box", "missing", []string{ids["a"]}); status.Code(err) != codes.NotFound {
		t.Errorf("missing sandbox = %v", err)
	}
	if n := len(rt.record().GetAttachTickets()); n != 0 {
		t.Errorf("refused attach left %d tickets", n)
	}
}

func TestBundleForTickets(t *testing.T) {
	env, _, o, rt := remoteEnv(t)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": nil})
	b, err := env.svc.BundleFor(id, "", nil)
	if err != nil || b.GetController().GetAttachTicket() != "" {
		t.Fatalf("plain = %+v / %v", b, err)
	}
	if _, err := env.svc.BundleFor(id, "elsewhere", []string{ids["a"]}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "unknown host elsewhere") {
		t.Errorf("unknown host = %v", err)
	}
	if _, err := env.svc.BundleFor(id, "box", []string{"ln-gone"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown lane = %v", err)
	}
	for _, host := range []string{"box", env.svc.HostID()} {
		b, err = env.svc.BundleFor(id, host, []string{ids["a"]})
		if err != nil || b.GetController().GetAttachTicket() == "" {
			t.Fatalf("laned on %s = %+v / %v", host, b, err)
		}
	}
	tickets := rt.record().GetAttachTickets()
	if len(tickets) != 2 || tickets[0].GetHostId() != "box" || tickets[0].GetSandboxId() != "" || !slices.Equal(tickets[0].GetLaneIds(), []string{ids["a"]}) {
		t.Errorf("tickets = %+v", tickets)
	}
	// The cap.
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		now := timestamppb.New(env.clock.Now())
		for i := len(o.AttachTickets); i < maxAttachTickets; i++ {
			o.AttachTickets = append(o.AttachTickets, &pb.AttachTicket{Id: fmt.Sprintf("t%d", i), HostId: "box", IssuedAt: now})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.BundleFor(id, "box", []string{ids["a"]}); !errors.Is(err, ErrExhausted) {
		t.Errorf("cap = %v", err)
	}
	// Expired tickets are swept before the cap is checked.
	env.clock.Advance(25 * time.Hour)
	if _, err := env.svc.BundleFor(id, "box", []string{ids["a"]}); err != nil {
		t.Errorf("after expiry = %v", err)
	}
	if _, err := env.svc.BundleFor("nope", "", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
}

func TestDroppedWorkerTags(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	var logged []string
	env.setLogHook(func(line string) { logged = append(logged, line) })
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		a := reviewPR()
		a.WorkerTag = "ci" //nolint:staticcheck // SA1019: a record written before lanes
		o.Actions = append(o.Actions, a)
		o.AttachTickets = append(o.AttachTickets, &pb.AttachTicket{Id: "old", IssuedAt: timestamppb.New(env.clock.Now().Add(-48 * time.Hour))})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec := env.svc.loadTidy(rt.record())
	if a := findAction(rec, "review-pr"); a.GetWorkerTag() != "" { //nolint:staticcheck // SA1019: checking the tag is gone
		t.Errorf("tag kept: %+v", a)
	}
	if d := rec.GetDroppedWorkerTags(); len(d) != 1 || d[0].GetAction() != "review-pr" || d[0].GetTag() != "ci" {
		t.Errorf("dropped = %+v", d)
	}
	if len(rec.GetAttachTickets()) != 0 {
		t.Errorf("expired ticket kept: %+v", rec.GetAttachTickets())
	}
	env.setLogHook(nil)
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, `dropped worker tag "ci" from action "review-pr"`) }) {
		t.Errorf("log = %v", logged)
	}
	// Nothing to tidy: the record is returned as is.
	if again := env.svc.loadTidy(rec); len(again.GetDroppedWorkerTags()) != 1 {
		t.Errorf("second tidy = %+v", again.GetDroppedWorkerTags())
	}
	// Dismiss clears the notice; a second dismiss changes nothing.
	rt.mu.Lock()
	rt.rec = rec
	rt.mu.Unlock()
	out, err := env.svc.DismissDroppedWorkerTags(id)
	if err != nil || len(out.GetDroppedWorkerTags()) != 0 {
		t.Fatalf("dismiss = %+v / %v", out, err)
	}
	if _, err := env.svc.DismissDroppedWorkerTags(id); err != nil {
		t.Errorf("second dismiss = %v", err)
	}
	if _, err := env.svc.DismissDroppedWorkerTags("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
}

func TestClientsNeverSeeHighWaterMarks(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		o.HighWaterMarks = map[string]*pb.HighWaterMark{"gone": {Revision: 3, Removed: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := env.svc.List()
	if len(list) != 1 || list[0].GetOrchestrator().GetHighWaterMarks() != nil {
		t.Errorf("list = %+v", list)
	}
	renamed, _ := env.svc.Rename(o.GetId(), "renamed")
	if renamed.GetHighWaterMarks() != nil || len(rt.record().GetHighWaterMarks()) != 1 {
		t.Error("rename leaked or lost the marks")
	}
	if forClient(nil) != nil {
		t.Error("forClient(nil)")
	}
}

// FR-314a: an action save is a dispatch trigger. A queued un-laned run with
// only a lane worker free is delivered once the save puts the action in that
// lane, with no other event.
func TestActionSaveTriggersDispatch(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": {"wa"}})
	if _, err := putAction(env.svc, id, reviewPR()); err != nil {
		t.Fatal(err)
	}
	run := call(t, env, id, "1")
	settle(t, rt)
	if got := runOf(t, env, id, run.GetId()); got.GetState() != pb.RunState_RUN_STATE_QUEUED {
		t.Fatalf("un-laned run = %v", got.GetState())
	}
	if _, err := putAction(env.svc, id, laned("review-pr", pb.LaneMode_LANE_MODE_EXCLUSIVE, ids["a"])); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return runOf(t, env, id, run.GetId()).GetSandboxId() == "wa" })
}

func TestDroppedWorkerTagsKeepTheNewest(t *testing.T) {
	o := &pb.Orchestrator{}
	for i := range maxDroppedTags {
		o.DroppedWorkerTags = append(o.DroppedWorkerTags, &pb.DroppedWorkerTag{Action: fmt.Sprintf("old%d", i)})
	}
	a := reviewPR()
	a.WorkerTag = "ci" //nolint:staticcheck // SA1019: a record written before lanes
	o.Actions = []*pb.Action{a}
	dropped := dropWorkerTags(o, timestamppb.Now())
	if len(dropped) != 1 || len(o.GetDroppedWorkerTags()) != maxDroppedTags || o.GetDroppedWorkerTags()[maxDroppedTags-1].GetAction() != "review-pr" || o.GetDroppedWorkerTags()[0].GetAction() != "old1" {
		t.Errorf("dropped = %v, list = %d ending %v", dropped, len(o.GetDroppedWorkerTags()), o.GetDroppedWorkerTags()[maxDroppedTags-1])
	}
}

// FR-319: a sandbox's tag no longer affects dispatch.
func TestSandboxTagDoesNotRoute(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false)
	tagged := w.update("wa", func(sb *pb.Sandbox) { sb.Tag = "ci" })
	rt.mu.Lock()
	rt.sbs["wa"] = tagged
	rt.mu.Unlock()
	if _, err := putAction(env.svc, o.GetId(), reviewPR()); err != nil {
		t.Fatal(err)
	}
	r1, r2 := call(t, env, o.GetId(), "1"), call(t, env, o.GetId(), "2")
	settle(t, rt)
	waitFor(t, func() bool {
		a, b := runOf(t, env, o.GetId(), r1.GetId()), runOf(t, env, o.GetId(), r2.GetId())
		return a.GetSandboxId() != "" && b.GetSandboxId() != "" && a.GetSandboxId() != b.GetSandboxId()
	})
}

// FR-322: after a restart, lanes, lanes pending, tickets, removal marks,
// action settings and kept settings are back; detaching, delivering and
// waiting reasons are not (they are in memory or derived).
func TestLanesSurviveRestart(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": {"wa"}})
	setAgent(rt, w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING)
	if _, err := putAction(env.svc, id, laned("review-pr", pb.LaneMode_LANE_MODE_INCLUSIVE, ids["a"])); err != nil {
		t.Fatal(err)
	}
	setAgent(rt, w, "wb", pb.AgentStatus_AGENT_STATUS_WORKING)
	run := call(t, env, id, "1")
	if _, err := env.svc.DeleteAction(id, "review-pr"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.BundleFor(id, "box", []string{ids["a"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		onRosterEntry(o, "wb").LanesPending = true
		o.HighWaterMarks = map[string]*pb.HighWaterMark{"gone": {Revision: 2, Removed: true, RemovedAt: timestamppb.New(env.clock.Now())}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.detaching["wa"] = struct{}{}
	rt.mu.Unlock()
	settle(t, rt)
	env.svc.Stop()

	svc2 := NewService(Deps{Store: env.reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test"})
	if err := svc2.Start(bg()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc2.Stop)
	rt2, _ := svc2.runtimeFor(id)
	rec := rt2.record()
	if laneByID(rec, ids["a"]) == nil || !slices.Equal(onRosterEntry(rec, "wa").GetLaneIds(), []string{ids["a"]}) || !onRosterEntry(rec, "wb").GetLanesPending() {
		t.Errorf("roster lanes = %+v", rec.GetWorkers())
	}
	if len(rec.GetAttachTickets()) != 1 || rec.GetHighWaterMarks()["gone"] == nil {
		t.Errorf("tickets %d, marks %v", len(rec.GetAttachTickets()), rec.GetHighWaterMarks())
	}
	stored, _ := env.reg.GetRun(id, run.GetId())
	if kept := stored.GetKeptLanes(); kept == nil || kept.GetMode() != pb.LaneMode_LANE_MODE_INCLUSIVE || stored.GetWaitingReason() != "" {
		t.Errorf("run = %+v", stored)
	}
	rt2.mu.Lock()
	delivering := len(rt2.delivering)
	rt2.mu.Unlock()
	if rt2.isDetaching("wa") || delivering != 0 {
		t.Error("in-memory marks survived the restart")
	}
}

// A lane delete that leaves a prompt action un-laned is guarded even when no
// open-lane worker existed before it (FR-308a: like saving it un-laned).
func TestDeleteLaneGuardsWithNoOpenLaneWorker(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": {"wa"}, "b": nil})
	if _, err := putAction(env.svc, id, laned("build", pb.LaneMode_LANE_MODE_EXCLUSIVE, ids["b"])); err != nil {
		t.Fatal(err)
	}
	impact, err := env.svc.CheckLaneChange(&pb.CheckLaneChangeRequest{OrchestratorId: id, Change: &pb.CheckLaneChangeRequest_DeleteLane{DeleteLane: &pb.DeleteLaneRequest{LaneId: ids["b"]}}})
	if err != nil || !slices.Contains(impact.GetUnlanedActions(), "build") {
		t.Fatalf("check = %+v / %v", impact, err)
	}
	if _, err := env.svc.DeleteLane(id, ids["b"], nil); !errors.Is(err, ErrPrecondition) {
		t.Errorf("unconfirmed delete = %v", err)
	}
	if _, err := env.svc.DeleteLane(id, ids["b"], &pb.LaneGuardConfirm{UnlanedActions: impact.GetUnlanedActions(), UnlanedRuns: impact.GetUnlanedRuns()}); err != nil {
		t.Errorf("confirmed delete = %v", err)
	}
}
