package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SWI-13: reconciliation reads the runtime once per pass, never reads
// "couldn't tell" as "stopped", and leaves records mid-operation alone.

// putRecord stores a sandbox record directly (no launch) in the given state.
func putRecord(t *testing.T, reg *registry.Registry, id, name, ref string, state pb.SandboxState) *pb.Sandbox {
	t.Helper()
	sb := &pb.Sandbox{Id: id, DisplayName: name, ContainerRef: ref, State: state, UpdatedAt: timestamppb.Now()}
	if err := reg.Put(sb); err != nil {
		t.Fatal(err)
	}
	return sb
}

func stateOf(t *testing.T, reg *registry.Registry, id string) pb.SandboxState {
	t.Helper()
	got, err := reg.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return got.GetState()
}

// recordEmits counts the manager's change events per sandbox id.
func recordEmits(m *Manager) map[string]int {
	emits := map[string]int{}
	m.SetOnChange(func(s *pb.Sandbox) { emits[s.GetId()]++ })
	return emits
}

func TestReconcileUnreadableRuntimeKeepsState(t *testing.T) {
	m, reg, runner, _ := newTestManager(t)
	putRecord(t, reg, "id-run", "run", "run", pb.SandboxState_SANDBOX_STATE_RUNNING)
	putRecord(t, reg, "id-stop", "stop", "stop", pb.SandboxState_SANDBOX_STATE_STOPPED)
	runner.running["stop"] = true // would be re-adopted, if the runtime could be read
	runner.listErr = errors.New("docker daemon not running")
	emits := recordEmits(m)

	changed, err := m.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile with an unreadable runtime: want an error")
	}
	if len(changed) != 0 || len(emits) != 0 {
		t.Errorf("unreadable runtime changed %v / emitted %v; want nothing", changed, emits)
	}
	if got := stateOf(t, reg, "id-run"); got != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("running record = %v; want RUNNING kept", got)
	}
	if got := stateOf(t, reg, "id-stop"); got != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Errorf("stopped record = %v; want STOPPED kept", got)
	}

	// The startup path behaves the same.
	if err := m.Readopt(context.Background()); err == nil {
		t.Error("Readopt with an unreadable runtime: want an error")
	}
	if got := stateOf(t, reg, "id-run"); got != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("readopt: running record = %v; want RUNNING kept", got)
	}
}

func TestReconcileFollowsAReadableRuntime(t *testing.T) {
	m, reg, runner, _ := newTestManager(t)
	putRecord(t, reg, "id-a", "a", "a", pb.SandboxState_SANDBOX_STATE_RUNNING) // listed stopped
	putRecord(t, reg, "id-b", "b", "b", pb.SandboxState_SANDBOX_STATE_RUNNING) // absent from the listing
	putRecord(t, reg, "id-c", "c", "c", pb.SandboxState_SANDBOX_STATE_STOPPED) // listed running
	putRecord(t, reg, "id-d", "d", "d", pb.SandboxState_SANDBOX_STATE_RUNNING) // listed running: unchanged
	putRecord(t, reg, "id-e", "e", "", pb.SandboxState_SANDBOX_STATE_RUNNING)  // no container
	runner.running = map[string]bool{"a": false, "c": true, "d": true, "e": true}
	emits := recordEmits(m)

	changed, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]pb.SandboxState{
		"id-a": pb.SandboxState_SANDBOX_STATE_STOPPED,
		"id-b": pb.SandboxState_SANDBOX_STATE_STOPPED,
		"id-c": pb.SandboxState_SANDBOX_STATE_RUNNING,
		"id-d": pb.SandboxState_SANDBOX_STATE_RUNNING,
		"id-e": pb.SandboxState_SANDBOX_STATE_STOPPED,
	}
	for id, st := range want {
		if got := stateOf(t, reg, id); got != st {
			t.Errorf("%s = %v; want %v", id, got, st)
		}
	}
	if len(changed) != 4 {
		t.Errorf("changed %d records; want 4 (a, b, c, e)", len(changed))
	}
	for _, id := range []string{"id-a", "id-b", "id-c", "id-e"} {
		if emits[id] != 1 {
			t.Errorf("%s emitted %d times; want 1", id, emits[id])
		}
	}
	if emits["id-d"] != 0 {
		t.Errorf("unchanged id-d emitted %d times; want 0", emits["id-d"])
	}
	if runner.lists != 1 {
		t.Errorf("ListStatuses called %d times for 5 sandboxes; want 1", runner.lists)
	}

	// A second pass over an agreeing registry changes nothing.
	if changed, err := m.Reconcile(context.Background()); err != nil || len(changed) != 0 {
		t.Errorf("second Reconcile = %v, %v; want no changes", changed, err)
	}
}

func TestReconcileMatchesByNameOrID(t *testing.T) {
	m, reg, runner, _ := newTestManager(t)
	putRecord(t, reg, "id-1", "by-name", "by-name", pb.SandboxState_SANDBOX_STATE_STOPPED)
	putRecord(t, reg, "id-2", "renamed", "old", pb.SandboxState_SANDBOX_STATE_STOPPED)
	// sbx lists the first by its name, the second only under the id.
	runner.running = map[string]bool{"by-name": true, "id-2": true}
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"id-1", "id-2"} {
		if got := stateOf(t, reg, id); got != pb.SandboxState_SANDBOX_STATE_RUNNING {
			t.Errorf("%s = %v; want RUNNING", id, got)
		}
	}
}

func TestReconcileLeavesOperationsAlone(t *testing.T) {
	m, reg, runner, _ := newTestManager(t)
	for _, st := range []pb.SandboxState{
		pb.SandboxState_SANDBOX_STATE_CREATING,
		pb.SandboxState_SANDBOX_STATE_DESTROYING,
		pb.SandboxState_SANDBOX_STATE_ERROR,
	} {
		putRecord(t, reg, "id-"+st.String(), st.String(), st.String(), st)
	}
	putRecord(t, reg, "id-busy", "busy", "busy", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if err := m.ops.acquire("id-busy", opRefresh); err != nil {
		t.Fatal(err)
	}
	runner.running = map[string]bool{} // nothing listed: every record would go STOPPED

	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, st := range []pb.SandboxState{
		pb.SandboxState_SANDBOX_STATE_CREATING,
		pb.SandboxState_SANDBOX_STATE_DESTROYING,
		pb.SandboxState_SANDBOX_STATE_ERROR,
	} {
		if got := stateOf(t, reg, "id-"+st.String()); got != st {
			t.Errorf("%v record = %v; want untouched", st, got)
		}
	}
	if got := stateOf(t, reg, "id-busy"); got != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("latched record = %v; want untouched", got)
	}

	// Startup has nothing in flight: Readopt settles stale transitional records.
	m.ops.release("id-busy")
	if err := m.Readopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, reg, "id-SANDBOX_STATE_CREATING"); got != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Errorf("readopt: stale CREATING = %v; want STOPPED", got)
	}
}

// A record written while the runtime was being read (a Stop or Restart that
// finished in between) keeps the newer write.
func TestReconcileSkipsARecordWrittenMeanwhile(t *testing.T) {
	m, reg, _, _ := newTestManager(t)
	putRecord(t, reg, "id-x", "x", "x", pb.SandboxState_SANDBOX_STATE_RUNNING)
	runner := &racingRunner{fakeRunner: newFakeRunner(), during: func() {
		if _, err := reg.Update("id-x", func(s *pb.Sandbox) error {
			s.UpdatedAt = timestamppb.New(s.GetUpdatedAt().AsTime().Add(time.Second))
			return nil
		}); err != nil {
			t.Error(err)
		}
	}}
	m.runner = runner // "x" absent from the listing: a stale pass would stop it
	emits := recordEmits(m)

	changed, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 || emits["id-x"] != 0 {
		t.Errorf("reconcile overwrote a newer write: changed %v, emits %v", changed, emits)
	}
	if got := stateOf(t, reg, "id-x"); got != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("id-x = %v; want RUNNING kept", got)
	}
}

// racingRunner runs during while the listing is being read.
type racingRunner struct {
	*fakeRunner
	during func()
}

func (r *racingRunner) ListStatuses(ctx context.Context) (map[string]string, error) {
	r.during()
	return r.fakeRunner.ListStatuses(ctx)
}
