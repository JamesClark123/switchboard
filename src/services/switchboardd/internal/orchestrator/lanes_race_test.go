package orchestrator

import (
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Mid-pass lane changes (contracts/dispatch.md guarantee 5, research R8): a
// delivery blocked in the fake holds the pass while the test changes the
// record; the pass's later claims re-check against what was committed.

// blockDelivery makes the first delivery to sandbox id wait until release is
// closed; entered is closed once it is waiting.
func blockDelivery(w *fakeWorker, id string) (entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.mu.Lock()
	w.onDeliver = func(req *pb.DeliverWorkerPromptRequest) {
		if req.GetSandboxId() != id {
			return
		}
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	w.mu.Unlock()
	return entered, release
}

func runOf(t *testing.T, env *testEnv, orchID, runID string) *pb.Run {
	t.Helper()
	got, err := env.svc.GetRun(orchID, runID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDispatchLaneRace(t *testing.T) {
	type setup struct {
		env *testEnv
		w   *fakeWorker
		o   *pb.Orchestrator
		rt  *runtime
		ids map[string]string
	}
	cases := map[string]struct {
		inclusive bool
		busyWC    bool
		mutate    func(t *testing.T, s setup)
		want      string // where the second run goes; "" = stays queued
	}{
		"next candidate leaves the lane": {
			mutate: func(t *testing.T, s setup) {
				if _, err := s.rt.update(func(o *pb.Orchestrator) error {
					onRosterEntry(o, "wb").LaneIds = nil
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			},
			want: "wc",
		},
		"next candidate becomes lanes pending": {
			mutate: func(t *testing.T, s setup) {
				if _, err := s.rt.update(func(o *pb.Orchestrator) error {
					w := onRosterEntry(o, "wb")
					w.LaneIds, w.LanesPending = nil, true
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			},
			want: "wc",
		},
		"next candidate is marked detaching before its detach commits": {
			mutate: func(t *testing.T, s setup) {
				s.rt.mu.Lock()
				s.rt.detaching["wb"] = struct{}{}
				s.rt.mu.Unlock()
			},
			want: "wc",
		},
		"action switches from inclusive to exclusive": {
			inclusive: true,
			busyWC:    true,
			mutate: func(t *testing.T, s setup) {
				if _, err := s.rt.update(func(o *pb.Orchestrator) error {
					onRosterEntry(o, "wb").LaneIds = nil // wb is an open-lane candidate now
					findAction(o, "review-pr").LaneMode = pb.LaneMode_LANE_MODE_EXCLUSIVE
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			},
			want: "",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env, w, o, rt := dispatchEnv(t, 3, false)
			ids := seedLanes(t, rt, map[string][]string{"a": {"wa", "wb", "wc"}})
			mode := pb.LaneMode_LANE_MODE_EXCLUSIVE
			if c.inclusive {
				mode = pb.LaneMode_LANE_MODE_INCLUSIVE
			}
			if _, err := putAction(env.svc, o.GetId(), laned("review-pr", mode, ids["a"])); err != nil {
				t.Fatal(err)
			}
			// Both runs queue while every worker is busy, so one pass plans
			// them together; it blocks delivering the first.
			for _, id := range []string{"wa", "wb", "wc"} {
				setAgent(rt, w, id, pb.AgentStatus_AGENT_STATUS_WORKING)
			}
			r0 := call(t, env, o.GetId(), "1")
			r1 := call(t, env, o.GetId(), "2")
			settle(t, rt)
			entered, release := blockDelivery(w, "wa")
			for _, id := range []string{"wa", "wb", "wc"} {
				if id != "wc" || !c.busyWC {
					setAgent(rt, w, id, pb.AgentStatus_AGENT_STATUS_IDLE)
				}
			}
			rt.triggerDispatch()
			<-entered
			c.mutate(t, setup{env, w, o, rt, ids})
			close(release)
			settle(t, rt)
			waitFor(t, func() bool { return runOf(t, env, o.GetId(), r0.GetId()).GetSandboxId() == "wa" })
			got := runOf(t, env, o.GetId(), r1.GetId())
			if got.GetSandboxId() != c.want {
				t.Errorf("r1 went to %q (%v), want %q", got.GetSandboxId(), got.GetState(), c.want)
			}
			if c.want == "" && got.GetState() != pb.RunState_RUN_STATE_QUEUED {
				t.Errorf("r1 = %v, want queued", got.GetState())
			}
			rt.clearDetaching("wb")
		})
	}
}

// A detach through the orchestrator is refused while a run is being
// delivered to that worker.
func TestDetachRefusedMidDelivery(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false)
	if _, err := putAction(env.svc, o.GetId(), plain(reviewPR())); err != nil {
		t.Fatal(err)
	}
	settle(t, rt)
	entered, release := blockDelivery(w, "wa")
	call(t, env, o.GetId(), "1")
	<-entered
	err := env.svc.DetachWorker(bg(), o.GetId(), "wa", &pb.LaneGuardConfirm{UnlanedActions: []string{"review-pr"}})
	close(release)
	if !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "receiving a run") {
		t.Errorf("detach mid-delivery = %v", err)
	}
	settle(t, rt)
}

// An action deleted mid-pass: the claim for its run skips (the snapshot run
// has no kept copy), the run is not failed, and the delete's own pass
// delivers it by the kept setting.
func TestDeleteActionMidPass(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": {"wa", "wb"}})
	if _, err := putAction(env.svc, id, laned("review-pr", pb.LaneMode_LANE_MODE_EXCLUSIVE, ids["a"])); err != nil {
		t.Fatal(err)
	}
	setAgent(rt, w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING)
	setAgent(rt, w, "wb", pb.AgentStatus_AGENT_STATUS_WORKING)
	call(t, env, id, "1")
	r1 := call(t, env, id, "2")
	settle(t, rt)
	entered, release := blockDelivery(w, "wa")
	setAgent(rt, w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE)
	setAgent(rt, w, "wb", pb.AgentStatus_AGENT_STATUS_IDLE)
	rt.triggerDispatch()
	<-entered
	if _, err := env.svc.DeleteAction(id, "review-pr"); err != nil {
		t.Fatal(err)
	}
	close(release)
	settle(t, rt)
	waitFor(t, func() bool {
		got := runOf(t, env, id, r1.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING
	})
	if got := runOf(t, env, id, r1.GetId()); got.GetSandboxId() != "wb" || got.GetKeptLanes() == nil {
		t.Errorf("r1 = %+v", got)
	}
}

// A deleted action's run with no kept copy fails; dispatch re-reads it first.
func TestMissingSettingFails(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	run := &pb.Run{Id: "run-x-1", OrchestratorId: id, Action: "gone", Type: pb.ActionType_ACTION_TYPE_PROMPT, State: pb.RunState_RUN_STATE_QUEUED}
	if err := env.reg.PutRun(run); err != nil {
		t.Fatal(err)
	}
	rt.triggerDispatch()
	settle(t, rt)
	waitFor(t, func() bool {
		got := runOf(t, env, id, run.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_FAILED && got.GetReason() == msgActionDeleted
	})
}

// Random lane writes between deliveries (seeded): every delivery is allowed
// by the record committed when it was claimed. Writes happen only inside the
// delivery hook, so the record seen there is the one its claim checked.
func TestDispatchLaneRaceSeeded(t *testing.T) {
	delivered := 0 // across seeds: a dispatch that stopped delivering fails
	for seed := int64(0); seed < 30; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			env, w, o, rt := dispatchEnv(t, 5, false)
			id := o.GetId()
			ids := seedLanes(t, rt, map[string][]string{"a": nil, "b": nil, "c": nil})
			lanes := []string{ids["a"], ids["b"], ids["c"]}
			if _, err := rt.update(func(o *pb.Orchestrator) error {
				for _, wk := range o.GetWorkers() {
					if rng.Intn(3) > 0 {
						wk.LaneIds = []string{lanes[rng.Intn(3)]}
					}
				}
				for i := range 3 {
					a := laned(fmt.Sprintf("act%d", i), pb.LaneMode_LANE_MODE_EXCLUSIVE, lanes[i])
					if rng.Intn(2) == 0 {
						a.LaneMode = pb.LaneMode_LANE_MODE_INCLUSIVE
					}
					a.CreatedAt = nil
					o.Actions = append(o.Actions, a)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex // guards bad and hookRng
			var bad []string
			hookRng := rand.New(rand.NewSource(seed + 1000))
			w.mu.Lock()
			w.onDeliver = func(req *pb.DeliverWorkerPromptRequest) {
				mu.Lock()
				defer mu.Unlock()
				delivered++
				rec := rt.record()
				wk := onRosterEntry(rec, req.GetSandboxId())
				a := findAction(rec, req.GetAction())
				if wk == nil || wk.GetLanesPending() || a == nil || !allowed(wk.GetLaneIds(), a.GetLaneIds(), a.GetLaneMode()) {
					bad = append(bad, fmt.Sprintf("%s → %s %v (action %v %v)", req.GetRunId(), req.GetSandboxId(), wk.GetLaneIds(), a.GetLaneIds(), a.GetLaneMode()))
				}
				// A random lane write before the next claim.
				_, _ = rt.update(func(o *pb.Orchestrator) error {
					wk := o.GetWorkers()[hookRng.Intn(len(o.GetWorkers()))]
					switch hookRng.Intn(3) {
					case 0:
						wk.LaneIds = []string{lanes[hookRng.Intn(3)]}
					case 1:
						wk.LaneIds = nil
					default:
						a := o.GetActions()[hookRng.Intn(len(o.GetActions()))]
						if a.GetLaneMode() == pb.LaneMode_LANE_MODE_EXCLUSIVE {
							a.LaneMode = pb.LaneMode_LANE_MODE_INCLUSIVE
						} else {
							a.LaneMode = pb.LaneMode_LANE_MODE_EXCLUSIVE
						}
					}
					return nil
				})
			}
			w.mu.Unlock()
			for i := range 8 {
				if _, err := env.svc.CallAction(id, fmt.Sprintf("act%d", rng.Intn(3)), map[string]string{"number": fmt.Sprint(i), "repo": "r"}, pb.EventKind_EVENT_KIND_USER, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			settle(t, rt)
			mu.Lock()
			defer mu.Unlock()
			if len(bad) > 0 {
				t.Errorf("deliveries not allowed at their claim: %v", bad)
			}
		})
	}
	if delivered == 0 {
		t.Error("no seed delivered any run: the test checked nothing")
	}
}

// staleRewrite is a stepper's read-modify-write of a queued run (claim,
// unclaim) with change running between the read and the write: change must
// wait for runs.mu, so its write lands after the stale one, not under it.
func staleRewrite(t *testing.T, rt *runtime, runID string, change func() error) {
	t.Helper()
	rt.runs.mu.Lock()
	stale, err := rt.svc.reg.GetRun(rt.id, runID)
	if err != nil {
		rt.runs.mu.Unlock()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- change() }()
	select {
	case err := <-done:
		rt.runs.mu.Unlock()
		t.Fatalf("the change committed while runs.mu was held (err %v)", err)
	case <-time.After(50 * time.Millisecond):
	}
	err = rt.putRun(stale)
	rt.runs.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDeletesWaitForAStepperRewrite(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	ids := seedLanes(t, rt, map[string][]string{"a": nil})
	if _, err := putAction(env.svc, id, laned("review-pr", pb.LaneMode_LANE_MODE_EXCLUSIVE, ids["a"])); err != nil {
		t.Fatal(err)
	}
	run := call(t, env, id, "1")
	staleRewrite(t, rt, run.GetId(), func() error {
		_, err := env.svc.DeleteAction(id, "review-pr")
		return err
	})
	if kept := runOf(t, env, id, run.GetId()).GetKeptLanes(); kept == nil || !slices.Equal(kept.GetLaneIds(), []string{ids["a"]}) {
		t.Fatalf("the action delete's kept setting was lost: %+v", kept)
	}
	staleRewrite(t, rt, run.GetId(), func() error {
		_, err := env.svc.DeleteLane(id, ids["a"], &pb.LaneGuardConfirm{})
		return err
	})
	if kept := runOf(t, env, id, run.GetId()).GetKeptLanes(); kept == nil || len(kept.GetLaneIds()) != 0 {
		t.Fatalf("the lane delete's strip was lost: %+v", kept)
	}
}
