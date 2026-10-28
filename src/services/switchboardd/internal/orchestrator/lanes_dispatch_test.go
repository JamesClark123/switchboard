package orchestrator

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Planner tests (research R8, contracts/dispatch.md): the pass runs over a
// fake deliverer, so routing is checked with no hosts involved.

const (
	excl = pb.LaneMode_LANE_MODE_EXCLUSIVE
	incl = pb.LaneMode_LANE_MODE_INCLUSIVE
)

// planCase is a roster (worker id → lane names, in roster order), free
// workers and a queue of runs (action lanes + mode; no lanes = un-laned).
type planWorkerSpec struct {
	id      string
	lanes   []string
	busy    bool // not free
	pending bool // lanes pending
	refuses bool // refuses deliveries
}

type planRunSpec struct {
	lanes []string
	mode  pb.LaneMode
	gone  bool // its action was deleted with no kept copy
}

// planRecord builds a record with one action per run (run i calls "a<i>").
func planRecord(workers []planWorkerSpec, runs []planRunSpec) (*pb.Orchestrator, []*pb.Run) {
	rec := &pb.Orchestrator{}
	laneID := func(name string) string {
		if l := laneByName(rec, name); l != nil {
			return l.GetId()
		}
		l := &pb.Lane{Id: "ln-" + name, Name: name}
		rec.Lanes = append(rec.Lanes, l)
		return l.GetId()
	}
	ids := func(names []string) []string {
		out := make([]string, 0, len(names))
		for _, n := range names {
			out = append(out, laneID(n))
		}
		return out
	}
	for _, w := range workers {
		rec.Workers = append(rec.Workers, &pb.Worker{SandboxId: w.id, HostId: "box", LaneIds: ids(w.lanes), LanesPending: w.pending})
	}
	var queued []*pb.Run
	for i, r := range runs {
		name := fmt.Sprintf("a%d", i)
		if !r.gone {
			rec.Actions = append(rec.Actions, &pb.Action{Name: name, Type: pb.ActionType_ACTION_TYPE_PROMPT, LaneIds: ids(r.lanes), LaneMode: r.mode})
		}
		queued = append(queued, &pb.Run{Id: fmt.Sprintf("r%d", i), Action: name, Type: pb.ActionType_ACTION_TYPE_PROMPT, State: pb.RunState_RUN_STATE_QUEUED})
	}
	return rec, queued
}

// planOutcome runs one pass and returns run id → worker id, the runs failed
// as missing, and the plan.
func planOutcome(workers []planWorkerSpec, runs []planRunSpec) (map[string]string, []string, *lanePlan, *pb.Orchestrator, []planRun) {
	rec, queued := planRecord(workers, runs)
	spec := map[string]planWorkerSpec{}
	for _, w := range workers {
		spec[w.id] = w
	}
	plan := buildPlan(rec, func(w *pb.Worker) bool { return !spec[w.GetSandboxId()].busy })
	prs := make([]planRun, len(queued))
	for i, run := range queued {
		ids, mode, ok := effectiveLanes(rec, run)
		prs[i] = planRun{run: run, ids: ids, mode: mode, ok: ok, mask: plan.maskOf(ids)}
	}
	got := map[string]string{}
	var missing []string
	plan.pass(prs, passHooks{
		reason:  func(*pb.Run) {},
		missing: func(r *pb.Run) { missing = append(missing, r.GetId()) },
		claim:   func(*pb.Worker, planRun) claimResult { return claimOK },
		deliver: func(r *pb.Run, w *pb.Worker) deliverResult {
			if spec[w.GetSandboxId()].refuses {
				return refused
			}
			got[r.GetId()] = w.GetSandboxId()
			return delivered
		},
	})
	return got, missing, plan, rec, prs
}

func TestPlannerScenarios(t *testing.T) {
	cases := map[string]struct {
		workers []planWorkerSpec
		runs    []planRunSpec
		want    map[string]string
	}{
		// US1
		"exclusive to a free lane worker": {
			workers: []planWorkerSpec{{id: "open"}, {id: "fe", lanes: []string{"frontend"}}},
			runs:    []planRunSpec{{lanes: []string{"frontend"}, mode: excl}},
			want:    map[string]string{"r0": "fe"},
		},
		"exclusive waits rather than use an open-lane worker": {
			workers: []planWorkerSpec{{id: "open"}, {id: "fe", lanes: []string{"frontend"}, busy: true}},
			runs:    []planRunSpec{{lanes: []string{"frontend"}, mode: excl}},
			want:    map[string]string{},
		},
		"one matching lane is enough": {
			workers: []planWorkerSpec{{id: "be", lanes: []string{"backend"}}},
			runs:    []planRunSpec{{lanes: []string{"frontend", "backend"}, mode: excl}},
			want:    map[string]string{"r0": "be"},
		},
		"un-laned never goes to a lane worker": {
			workers: []planWorkerSpec{{id: "fe", lanes: []string{"frontend"}}, {id: "open", busy: true}},
			runs:    []planRunSpec{{}},
			want:    map[string]string{},
		},
		"un-laned goes to an open-lane worker": {
			workers: []planWorkerSpec{{id: "fe", lanes: []string{"frontend"}}, {id: "open"}},
			runs:    []planRunSpec{{}},
			want:    map[string]string{"r0": "open"},
		},
		// US2
		"inclusive prefers its lane": {
			workers: []planWorkerSpec{{id: "open"}, {id: "rv", lanes: []string{"reviewers"}}},
			runs:    []planRunSpec{{lanes: []string{"reviewers"}, mode: incl}},
			want:    map[string]string{"r0": "rv"},
		},
		"inclusive falls back to an open-lane worker": {
			workers: []planWorkerSpec{{id: "open"}, {id: "rv", lanes: []string{"reviewers"}, busy: true}},
			runs:    []planRunSpec{{lanes: []string{"reviewers"}, mode: incl}},
			want:    map[string]string{"r0": "open"},
		},
		"inclusive never goes to an unrelated lane": {
			workers: []planWorkerSpec{{id: "fe", lanes: []string{"frontend"}}},
			runs:    []planRunSpec{{lanes: []string{"reviewers"}, mode: incl}},
			want:    map[string]string{},
		},
		"inclusive then exclusive: neither waits": {
			workers: []planWorkerSpec{{id: "rv", lanes: []string{"reviewers"}}, {id: "open"}},
			runs:    []planRunSpec{{lanes: []string{"reviewers"}, mode: incl}, {lanes: []string{"reviewers"}, mode: excl}},
			want:    map[string]string{"r0": "open", "r1": "rv"},
		},
		"inclusive takes a reserved lane worker when nothing else is free": {
			workers: []planWorkerSpec{{id: "rv", lanes: []string{"reviewers"}}},
			runs:    []planRunSpec{{lanes: []string{"reviewers"}, mode: incl}, {lanes: []string{"reviewers"}, mode: excl}},
			want:    map[string]string{"r0": "rv"},
		},
		// Reservations and refusals
		"reservation prefers fewest outside lanes": {
			workers: []planWorkerSpec{{id: "wide", lanes: []string{"a", "x", "y"}}, {id: "narrow", lanes: []string{"a"}}},
			runs:    []planRunSpec{{lanes: []string{"a"}, mode: excl}},
			want:    map[string]string{"r0": "narrow"},
		},
		"a refusal moves the run to its next candidate": {
			workers: []planWorkerSpec{{id: "a1", lanes: []string{"a"}, refuses: true}, {id: "a2", lanes: []string{"a"}}},
			runs:    []planRunSpec{{lanes: []string{"a"}, mode: excl}},
			want:    map[string]string{"r0": "a2"},
		},
		"a lanes-pending worker is never chosen": {
			workers: []planWorkerSpec{{id: "p", pending: true}},
			runs:    []planRunSpec{{}},
			want:    map[string]string{},
		},
		"earlier exclusive steals a later run's reservation": {
			workers: []planWorkerSpec{{id: "ab", lanes: []string{"a", "b"}}},
			runs:    []planRunSpec{{lanes: []string{"a"}, mode: excl}, {lanes: []string{"b"}, mode: excl}},
			want:    map[string]string{"r0": "ab"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, _, _, _, _ := planOutcome(c.workers, c.runs)
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("deliveries = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPlannerMissingSetting(t *testing.T) {
	got, missing, _, _, _ := planOutcome([]planWorkerSpec{{id: "open"}}, []planRunSpec{{gone: true}, {}})
	if !slices.Equal(missing, []string{"r0"}) || got["r1"] != "open" {
		t.Errorf("missing = %v, deliveries = %v", missing, got)
	}
}

// TestDispatchProperties checks guarantees 1–3 of contracts/dispatch.md over
// random rosters, queues and refusals.
func TestDispatchProperties(t *testing.T) {
	lanes := []string{"l0", "l1", "l2", "l3", "l4", "l5"}
	pick := func(rng *rand.Rand, max int) []string {
		n := rng.Intn(max + 1)
		out := slices.Clone(lanes[:rng.Intn(len(lanes))+1])
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		if n > len(out) {
			n = len(out)
		}
		return out[:n]
	}
	for seed := int64(0); seed < 100; seed++ {
		rng := rand.New(rand.NewSource(seed))
		for iter := 0; iter < 100; iter++ {
			var workers []planWorkerSpec
			for i := range rng.Intn(12) + 1 {
				workers = append(workers, planWorkerSpec{
					id: fmt.Sprintf("w%d", i), lanes: pick(rng, 4),
					busy: rng.Intn(5) == 0, pending: rng.Intn(10) == 0, refuses: rng.Intn(6) == 0,
				})
			}
			var runs []planRunSpec
			for range rng.Intn(12) + 1 {
				switch rng.Intn(3) {
				case 0:
					runs = append(runs, planRunSpec{})
				case 1:
					runs = append(runs, planRunSpec{lanes: pick(rng, 3), mode: excl})
				default:
					runs = append(runs, planRunSpec{lanes: pick(rng, 3), mode: incl})
				}
			}
			got, _, _, rec, prs := planOutcome(workers, runs)
			spec := map[string]planWorkerSpec{}
			for _, w := range workers {
				spec[w.id] = w
			}
			can := func(r planRun, id string) bool {
				w := onRosterEntry(rec, id)
				return !w.GetLanesPending() && allowed(w.GetLaneIds(), r.ids, r.mode)
			}
			taken := map[string]int{} // worker → index of the run it took
			for i, r := range prs {
				if id, ok := got[r.run.GetId()]; ok {
					taken[id] = i
				}
			}
			for i, r := range prs {
				id, ok := got[r.run.GetId()]
				if ok {
					// 1. every delivery is allowed.
					if !can(r, id) {
						t.Fatalf("seed %d/%d: run %d (%v %v) delivered to %s %v", seed, iter, i, r.ids, r.mode, id, onRosterEntry(rec, id).GetLaneIds())
					}
					continue
				}
				for _, w := range workers {
					if w.busy || w.refuses || !can(r, w.id) {
						continue
					}
					j, took := taken[w.id]
					// 2. no free accepting worker it could take is left unclaimed.
					if !took {
						t.Fatalf("seed %d/%d: run %d (%v %v) queued while %s %v is free", seed, iter, i, r.ids, r.mode, w.id, w.lanes)
					}
					// 3. no run waits for a later run.
					if j > i {
						t.Fatalf("seed %d/%d: run %d (%v %v) waits while later run %d took %s", seed, iter, i, r.ids, r.mode, j, w.id)
					}
				}
			}
		}
	}
}

// TestPlannerCost checks guarantee 4's order: the pass's list visits stay
// linear in the lane memberships of free workers plus the queued runs' lanes.
func TestPlannerCost(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	names := make([]string, maxLanes)
	for i := range names {
		names[i] = fmt.Sprintf("l%d", i)
	}
	var workers []planWorkerSpec
	members := 0
	for i := range 2000 {
		ls := []string{names[rng.Intn(maxLanes)], names[rng.Intn(maxLanes)]}
		if ls[0] == ls[1] {
			ls = ls[:1]
		}
		members += len(ls)
		workers = append(workers, planWorkerSpec{id: fmt.Sprintf("w%d", i), lanes: ls})
	}
	var runs []planRunSpec
	runLanes := 0
	for range 4000 {
		ls := []string{names[rng.Intn(maxLanes)]}
		runLanes += len(ls)
		mode := excl
		if rng.Intn(2) == 0 {
			mode = incl
		}
		runs = append(runs, planRunSpec{lanes: ls, mode: mode})
	}
	_, _, plan, _, _ := planOutcome(workers, runs)
	if bound := 2*members + 2*runLanes; plan.touches > bound {
		t.Errorf("touches = %d, want ≤ %d (members %d, run lanes %d)", plan.touches, bound, members, runLanes)
	}
}
