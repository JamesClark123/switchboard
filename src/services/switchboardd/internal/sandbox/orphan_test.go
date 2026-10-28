package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// --- fakes (SWI-17, research R14) ---

// probeRunner is a fakeRunner whose `sbx exec` answers the probe and reap
// scripts with canned output, recording every runtime call in order. hook
// (may block) runs on each exec and runtime call.
type probeRunner struct {
	*fakeRunner
	pmu      sync.Mutex
	out      map[string]string // ref -> probe answer; missing = the exec fails
	slow     map[string]bool   // ref -> the probe exec hangs until its context ends
	reapOut  string
	reapFail bool
	stopErr  error
	kitErr   error
	calls    []string
	hook     func(kind, ref string)
}

func newProbeRunner() *probeRunner {
	return &probeRunner{fakeRunner: newFakeRunner(), out: map[string]string{}, slow: map[string]bool{}, reapOut: "nofile\nreaped\t1\n"}
}

func (p *probeRunner) record(kind, ref string) {
	p.pmu.Lock()
	p.calls = append(p.calls, kind+" "+ref)
	hook := p.hook
	p.pmu.Unlock()
	if hook != nil {
		hook(kind, ref)
	}
}

func (p *probeRunner) setHook(fn func(kind, ref string)) {
	p.pmu.Lock()
	p.hook = fn
	p.pmu.Unlock()
}

func (p *probeRunner) setOut(ref, out string) {
	p.pmu.Lock()
	p.out[ref] = out
	p.pmu.Unlock()
}

func (p *probeRunner) callLog() []string {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *probeRunner) count(kind string) int {
	n := 0
	for _, c := range p.callLog() {
		if strings.HasPrefix(c, kind+" ") {
			n++
		}
	}
	return n
}

func (p *probeRunner) Exec(ctx context.Context, ref string, argv []string) *exec.Cmd {
	script := argv[len(argv)-1]
	kind := "probe"
	if strings.Contains(script, "reaped") {
		kind = "reap"
	}
	p.record(kind, ref)
	p.pmu.Lock()
	defer p.pmu.Unlock()
	if kind == "reap" {
		if p.reapFail {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "printf", "%s", p.reapOut)
	}
	if p.slow[ref] {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	out, ok := p.out[ref]
	if !ok {
		return exec.CommandContext(ctx, "false")
	}
	return exec.CommandContext(ctx, "printf", "%s", out)
}

func (p *probeRunner) Stop(ctx context.Context, ref string) error {
	p.record("stop", ref)
	p.pmu.Lock()
	err := p.stopErr
	p.pmu.Unlock()
	if err != nil {
		return err
	}
	return p.fakeRunner.Stop(ctx, ref)
}

func (p *probeRunner) Start(ctx context.Context, ref string) error {
	p.record("start", ref)
	return p.fakeRunner.Start(ctx, ref)
}

func (p *probeRunner) Destroy(ctx context.Context, ref string) error {
	p.record("destroy", ref)
	return p.fakeRunner.Destroy(ctx, ref)
}

func (p *probeRunner) AddKit(ctx context.Context, ref, src string, log func(string)) error {
	p.record("kit", ref)
	p.pmu.Lock()
	err := p.kitErr
	p.pmu.Unlock()
	if err != nil {
		return err
	}
	return p.fakeRunner.AddKit(ctx, ref, src, log)
}

// fakeTokens is the hook-token index.
type fakeTokens struct {
	mu        sync.Mutex
	unretired map[string][]int64
	counter   map[string]int64
	bound     []agent.ProbedAgent
}

func newFakeTokens() *fakeTokens {
	return &fakeTokens{unretired: map[string][]int64{}, counter: map[string]int64{}}
}

func (f *fakeTokens) issue(id string, gen int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unretired[id] = append(f.unretired[id], gen)
	f.counter[id] = max(f.counter[id], gen)
}

func (f *fakeTokens) Unretired(id string) []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.unretired[id]...)
}

func (f *fakeTokens) Generation(id string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counter[id]
}

func (f *fakeTokens) Retire(id string, gen int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unretired[id] = slices.DeleteFunc(f.unretired[id], func(g int64) bool { return g == gen })
}

func (f *fakeTokens) BindFromProbe(_ string, p agent.ProbedAgent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bound = append(f.bound, p)
	return nil
}

type fakeLive struct {
	mu sync.Mutex
	m  map[string]int64
}

func (f *fakeLive) LiveGen(id string) (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.m[id]
	return g, ok
}

type fakeNotes struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeNotes) EmitNotification(id string, kind pb.NotificationKind, msg string, _ time.Time) *pb.NotificationEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, fmt.Sprintf("%s %s %s", id, kind, msg))
	return nil
}

func (f *fakeNotes) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

type orphanEnv struct {
	m       *Manager
	reg     *registry.Registry
	run     *probeRunner
	tokens  *fakeTokens
	live    *fakeLive
	notes   *fakeNotes
	dir     string
	cmu     sync.Mutex
	cleared []string
}

// fastOrphans shortens the tracker's bounds for a test.
func fastOrphans(t *testing.T) {
	t.Helper()
	saved := []time.Duration{orphanProbeTimeout, orphanBackstop, orphanUnknownLimit, orphanKillWait, orphanRefusalDebounce, orphanRetryBase}
	orphanProbeTimeout, orphanBackstop, orphanUnknownLimit = 2*time.Second, time.Hour, time.Hour
	orphanKillWait, orphanRefusalDebounce, orphanRetryBase = 10*time.Millisecond, time.Hour, 5*time.Millisecond
	t.Cleanup(func() {
		orphanProbeTimeout, orphanBackstop, orphanUnknownLimit = saved[0], saved[1], saved[2]
		orphanKillWait, orphanRefusalDebounce, orphanRetryBase = saved[3], saved[4], saved[5]
	})
}

func newOrphanEnv(t *testing.T) *orphanEnv {
	t.Helper()
	fastOrphans(t)
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	e := &orphanEnv{reg: reg, run: newProbeRunner(), tokens: newFakeTokens(), live: &fakeLive{m: map[string]int64{}}, notes: &fakeNotes{}, dir: dir}
	e.m = NewManager(reg, e.run, filepath.Join(dir, "workspaces"), "host-1")
	e.m.SetOrphanDeps(OrphanDeps{Agents: e.live, Tokens: e.tokens, Notify: e.notes, OnClear: func(sb *pb.Sandbox) {
		e.cmu.Lock()
		e.cleared = append(e.cleared, sb.GetId())
		e.cmu.Unlock()
	}})
	return e
}

// put records a sandbox directly (its ref is its id).
func (e *orphanEnv) put(t *testing.T, id string, state pb.SandboxState, status pb.AgentStatus, run *pb.WorkerRun) {
	t.Helper()
	sb := &pb.Sandbox{Id: id, DisplayName: id, ContainerRef: id, State: state, Agent: &pb.AgentSession{Status: status}}
	if run != nil {
		sb.Controller = &pb.SandboxController{OrchestratorId: "o", Run: run}
	}
	if err := e.reg.Put(sb); err != nil {
		t.Fatal(err)
	}
	e.run.mu.Lock()
	e.run.running[id] = state == pb.SandboxState_SANDBOX_STATE_RUNNING
	e.run.mu.Unlock()
}

func (e *orphanEnv) get(t *testing.T, id string) *pb.Sandbox {
	t.Helper()
	sb, err := e.m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func (e *orphanEnv) state(id string) pb.AgentCheck_State {
	sb, _ := e.m.Get(id)
	return sb.GetAgent().GetAgentCheck().GetState()
}

func (e *orphanEnv) waitState(t *testing.T, id string, want pb.AgentCheck_State) {
	t.Helper()
	waitUntil(t, func() bool { return e.state(id) == want && !e.probing(id) })
}

func (e *orphanEnv) probing(id string) bool {
	e.m.orphans.mu.Lock()
	defer e.m.orphans.mu.Unlock()
	st := e.m.orphans.st[id]
	return st != nil && st.probing
}

func (e *orphanEnv) wasCleared(id string) bool {
	e.cmu.Lock()
	defer e.cmu.Unlock()
	return slices.Contains(e.cleared, id)
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func aliveOut(gen int64) string {
	return fmt.Sprintf("agent\t/c\nref\t0\t\nfile\t%d\t42\t999\nproc\t42\t999\t0\t/c\t\tclaude\x1f--dangerously-skip-permissions\n", gen)
}

const goneOut = "agent\t/c\nref\t0\t\nfile\t3\t42\t999\n"

// --- daemon start ---

func TestOrphansAtStart(t *testing.T) {
	e := newOrphanEnv(t)
	running, stopped := pb.SandboxState_SANDBOX_STATE_RUNNING, pb.SandboxState_SANDBOX_STATE_STOPPED
	idle := pb.AgentStatus_AGENT_STATUS_IDLE
	e.put(t, "stopped", stopped, idle, nil)
	e.tokens.issue("stopped", 2)
	e.put(t, "clear", running, idle, nil)
	e.tokens.counter["clear"] = 3 // an agent that is known gone: no unretired token
	e.put(t, "orphan", running, pb.AgentStatus_AGENT_STATUS_WORKING, &pb.WorkerRun{Id: "r1", StepRole: pb.StepRole_STEP_ROLE_ACTION})
	e.tokens.issue("orphan", 4)
	e.run.setOut("orphan", aliveOut(4))
	e.put(t, "gone", running, idle, nil)
	e.tokens.issue("gone", 5)
	e.run.setOut("gone", goneOut)
	e.put(t, "cleanup", running, idle, &pb.WorkerRun{Id: "r2", StepRole: pb.StepRole_STEP_ROLE_CLEANUP})
	e.tokens.issue("cleanup", 6)
	e.run.setOut("cleanup", aliveOut(6))
	e.put(t, "upgrade", running, idle, nil) // no token entry, a set status: may have an agent
	e.run.setOut("upgrade", "nofile\n")
	e.put(t, "upgrade-exited", running, pb.AgentStatus_AGENT_STATUS_EXITED, nil)

	// Probes are held until every sandbox has been classified.
	release := make(chan struct{})
	e.run.setHook(func(kind, _ string) {
		if kind == "probe" {
			<-release
		}
	})
	e.m.CheckOrphansAtStart(context.Background())
	for id, want := range map[string]pb.AgentCheck_State{
		"orphan": pb.AgentCheck_STATE_UNKNOWN, "gone": pb.AgentCheck_STATE_UNKNOWN, "upgrade": pb.AgentCheck_STATE_UNKNOWN,
		"clear": pb.AgentCheck_STATE_CLEAR, "upgrade-exited": pb.AgentCheck_STATE_CLEAR, "stopped": pb.AgentCheck_STATE_CLEAR,
	} {
		if got := e.state(id); got != want {
			t.Errorf("%s: check %v, want %v", id, got, want)
		}
	}
	// The gate refuses every unknown sandbox meanwhile; a clear one spawns.
	if _, err := e.m.Admit("orphan"); !errors.Is(err, agent.ErrChecking) {
		t.Errorf("Admit(unknown) = %v", err)
	}
	if rel, err := e.m.Admit("clear"); err != nil {
		t.Errorf("Admit(clear) = %v", err)
	} else {
		rel()
	}
	// A stopped sandbox is never probed: its tokens are retired and it is EXITED.
	if e.get(t, "stopped").GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED || len(e.tokens.Unretired("stopped")) != 0 {
		t.Error("a stopped sandbox gets EXITED and its tokens retired")
	}
	close(release)

	e.waitState(t, "orphan", pb.AgentCheck_STATE_DETACHED)
	e.waitState(t, "gone", pb.AgentCheck_STATE_CLEAR)
	e.waitState(t, "cleanup", pb.AgentCheck_STATE_DETACHED)
	e.waitState(t, "upgrade", pb.AgentCheck_STATE_CLEAR)
	// The refused Admit above asks for one more probe of "orphan" (single-flight).
	probed := map[string]bool{}
	for _, c := range e.run.callLog() {
		probed[strings.TrimPrefix(c, "probe ")] = true
	}
	if len(probed) != 4 || !probed["orphan"] || !probed["gone"] || !probed["cleanup"] || !probed["upgrade"] {
		t.Errorf("probed %v, want orphan, gone, cleanup, upgrade", e.run.callLog())
	}
	for _, c := range e.run.callLog() {
		if strings.HasSuffix(c, " stopped") || strings.HasSuffix(c, " clear") || strings.HasSuffix(c, "upgrade-exited") {
			t.Errorf("probed %s", c)
		}
	}

	o := e.get(t, "orphan")
	chk := o.GetAgent().GetAgentCheck()
	if chk.GetLastStatus() != pb.AgentStatus_AGENT_STATUS_WORKING || chk.GetProcesses() != 1 {
		t.Errorf("detached check = %v", chk)
	}
	if o.GetController().GetRun().GetHold() != pb.WorkerHold_WORKER_HOLD_NONE || o.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_WORKING {
		t.Error("an ACTION run is left in flight")
	}
	if _, err := e.m.Admit("orphan"); !errors.Is(err, agent.ErrDetached) {
		t.Errorf("Admit(orphan) = %v", err)
	}
	if len(e.tokens.bound) < 2 || e.tokens.bound[0].Gen == 0 {
		t.Errorf("binds from agent.pid = %+v", e.tokens.bound)
	}
	c := e.get(t, "cleanup").GetController().GetRun()
	if c.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER || c.GetHoldGen() != 6 {
		t.Errorf("a cleanup run is held for the developer at the orphan's generation: %v", c)
	}
	g := e.get(t, "gone")
	if g.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED || len(e.tokens.Unretired("gone")) != 0 || !e.wasCleared("gone") {
		t.Error("a gone agent is EXITED, its token retired, and the worker re-kicked")
	}
	// One notification per orphan record, never per re-probe.
	if e.notes.n() != 2 {
		t.Errorf("notifications %v", e.notes.msgs)
	}
	before := e.run.count("probe")
	e.m.RecheckOrphan("orphan")
	waitUntil(t, func() bool { return e.run.count("probe") == before+1 && !e.probing("orphan") })
	if e.notes.n() != 2 {
		t.Error("a re-probe re-notified")
	}
}

// A listing that can't answer retires nothing and writes no EXITED; the
// sandbox stays unknown, its probes retry with backoff, and after 3 failures
// it is CHECK_FAILED (t offers Restart sandbox / Wait). Once the runtime is
// readable again a gone probe clears it.
func TestOrphansAtStartListingError(t *testing.T) {
	e := newOrphanEnv(t)
	e.put(t, "a", pb.SandboxState_SANDBOX_STATE_STOPPED, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	e.tokens.issue("a", 1)
	e.put(t, "b", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	e.tokens.issue("b", 1)
	e.run.mu.Lock()
	e.run.listErr = errors.New("runtime down")
	e.run.mu.Unlock()
	e.m.CheckOrphansAtStart(context.Background())
	if len(e.tokens.Unretired("a")) != 1 || e.get(t, "a").GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_EXITED {
		t.Error("an unreadable listing retired a token or wrote EXITED")
	}
	if e.state("a") != pb.AgentCheck_STATE_UNKNOWN {
		t.Error("a may-have-agent sandbox is unknown while the listing errs")
	}
	e.waitState(t, "b", pb.AgentCheck_STATE_CHECK_FAILED)
	if err := e.get(t, "b").GetAgent().GetAgentCheck().GetError(); !strings.Contains(err, "runtime down") {
		t.Errorf("check error %q", err)
	}
	if e.run.count("probe") != 0 {
		t.Error("no `sbx exec` without a listing that says running")
	}
	if _, err := e.m.Admit("b"); !errors.Is(err, agent.ErrChecking) {
		t.Errorf("Admit = %v", err)
	}
	e.run.setOut("b", goneOut)
	e.run.mu.Lock()
	e.run.listErr = nil
	e.run.mu.Unlock()
	e.waitState(t, "b", pb.AgentCheck_STATE_CLEAR)
}

// A probe that times out leaves the sandbox refused and re-probed.
func TestOrphanProbeTimeout(t *testing.T) {
	e := newOrphanEnv(t)
	orphanProbeTimeout = 50 * time.Millisecond
	e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	e.tokens.issue("s", 1)
	e.run.slow["s"] = true
	e.m.CheckOrphansAtStart(context.Background())
	waitUntil(t, func() bool { return e.run.count("probe") >= 2 })
	if _, err := e.m.Admit("s"); !errors.Is(err, agent.ErrChecking) {
		t.Errorf("Admit = %v", err)
	}
	e.run.pmu.Lock()
	e.run.slow["s"] = false
	e.run.out["s"] = goneOut
	e.run.pmu.Unlock()
	e.waitState(t, "s", pb.AgentCheck_STATE_CLEAR)
}

// Start-up probes run at most orphanProbeConcurrency at a time.
func TestOrphanProbesBounded(t *testing.T) {
	e := newOrphanEnv(t)
	var mu sync.Mutex
	in, peak := 0, 0
	release := make(chan struct{})
	e.run.setHook(func(kind, _ string) {
		if kind != "probe" {
			return
		}
		mu.Lock()
		in++
		peak = max(peak, in)
		mu.Unlock()
		<-release
		mu.Lock()
		in--
		mu.Unlock()
	})
	for i := range 20 {
		id := fmt.Sprintf("s%02d", i)
		e.put(t, id, pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
		e.tokens.issue(id, 1)
		e.run.setOut(id, goneOut)
	}
	e.m.CheckOrphansAtStart(context.Background())
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return in == orphanProbeConcurrency })
	time.Sleep(20 * time.Millisecond)
	close(release)
	for i := range 20 {
		e.waitState(t, fmt.Sprintf("s%02d", i), pb.AgentCheck_STATE_CLEAR)
	}
	if peak != orphanProbeConcurrency {
		t.Errorf("peak %d concurrent probes, want %d", peak, orphanProbeConcurrency)
	}
}

// --- the runtime lock (ordering) ---

// launched is a sandbox launched through the manager (ref = its name).
func (e *orphanEnv) launched(t *testing.T, name string) *pb.Sandbox {
	t.Helper()
	sb, _ := launchOne(t, e.m, e.dir, name)
	return sb
}

// orphanOn gives a running sandbox an orphan record (its client exited and
// the probe found the agent alive).
func (e *orphanEnv) orphanOn(t *testing.T, id, ref string) {
	t.Helper()
	e.tokens.issue(id, 7)
	e.run.setOut(ref, aliveOut(7))
	e.m.AgentClientExited(id, 7)
	e.waitState(t, id, pb.AgentCheck_STATE_DETACHED)
}

// For every lifecycle operation: a probe in flight finishes before its
// runtime call, and while it holds the lock the gate refuses (busy), a
// probe runs no `sbx exec` and a Replace is refused busy.
func TestRuntimeLockOrdersOperations(t *testing.T) {
	ops := map[string]struct {
		call  string // the runtime call that holds the lock
		start pb.SandboxState
		run   func(m *Manager, id string) error
	}{
		"stop":    {"stop", pb.SandboxState_SANDBOX_STATE_RUNNING, func(m *Manager, id string) error { _, err := m.Stop(context.Background(), id); return err }},
		"refresh": {"stop", pb.SandboxState_SANDBOX_STATE_RUNNING, func(m *Manager, id string) error { _, err := m.Refresh(context.Background(), id, nil, nil); return err }},
		"destroy": {"destroy", pb.SandboxState_SANDBOX_STATE_RUNNING, func(m *Manager, id string) error { _, err := m.Destroy(context.Background(), id); return err }},
		"addkit": {"kit", pb.SandboxState_SANDBOX_STATE_RUNNING, func(m *Manager, id string) error {
			_, err := m.AddKit(context.Background(), id, "/kits/k", nil, nil, nil)
			return err
		}},
		"restart": {"start", pb.SandboxState_SANDBOX_STATE_STOPPED, func(m *Manager, id string) error { _, err := m.Restart(context.Background(), id, nil); return err }},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			e := newOrphanEnv(t)
			sb := e.launched(t, "p")
			id := sb.GetId()
			if op.start == pb.SandboxState_SANDBOX_STATE_STOPPED {
				if _, err := e.m.Stop(context.Background(), id); err != nil {
					t.Fatal(err)
				}
			} else {
				e.tokens.issue(id, 1)
				e.run.setOut("p", goneOut)
				e.m.orphans.setUnknown(id)
			}
			probeIn, probeGo := make(chan struct{}), make(chan struct{})
			opIn, opGo := make(chan struct{}), make(chan struct{})
			var probeOnce, opOnce sync.Once
			e.run.setHook(func(kind, _ string) {
				switch kind {
				case "probe":
					probeOnce.Do(func() { close(probeIn); <-probeGo })
				case op.call:
					opOnce.Do(func() { close(opIn); <-opGo })
				}
			})
			if op.start != pb.SandboxState_SANDBOX_STATE_STOPPED {
				go e.m.orphans.probe(id)
				<-probeIn
			}
			done := make(chan error, 1)
			go func() { done <- op.run(e.m, id) }()
			if op.start != pb.SandboxState_SANDBOX_STATE_STOPPED {
				select {
				case <-opIn:
					t.Fatal("the operation's runtime call overtook an in-flight probe")
				case <-time.After(30 * time.Millisecond):
				}
				close(probeGo)
			}
			<-opIn
			if _, err := e.m.Admit(id); !errors.Is(err, agent.ErrBusy) && !errors.Is(err, agent.ErrOrphan) {
				t.Errorf("Admit during %s = %v", name, err)
			}
			if rel, ok := e.m.TryLockRuntime(id); ok {
				rel()
				t.Errorf("%s does not hold the runtime lock", name)
			}
			e.m.orphans.setUnknown(id)
			before := e.run.count("probe")
			e.m.orphans.probe(id)
			if e.run.count("probe") != before {
				t.Error("a probe during the operation ran `sbx exec`")
			}
			if err := e.m.ReapOrphan(context.Background(), id, ReapReplace); !errors.Is(err, agent.ErrBusy) && err != nil && !strings.Contains(err.Error(), "sandbox is") {
				t.Errorf("Replace during %s = %v", name, err)
			}
			close(opGo)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			// No `sbx exec` follows a stop.
			log := e.run.callLog()
			if i := slices.Index(log, "stop p"); i >= 0 && name == "stop" {
				for _, c := range log[i+1:] {
					if strings.HasPrefix(c, "probe") || strings.HasPrefix(c, "reap") {
						t.Errorf("%q after the stop: %v", c, log)
					}
				}
			}
		})
	}
}

// The gate passes, then a Stop starts while the factory is still starting
// the agent: the Stop waits for the session to be registered.
func TestStopWaitsForAnAdmittedSpawn(t *testing.T) {
	e := newOrphanEnv(t)
	sb := e.launched(t, "p")
	release, err := e.m.Admit(sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _, _ = e.m.Stop(context.Background(), sb.GetId()); close(done) }()
	time.Sleep(30 * time.Millisecond)
	if e.run.count("stop") != 0 {
		t.Fatal("Stop ran while a spawn held the runtime lock")
	}
	release()
	<-done
	if e.run.count("stop") != 1 {
		t.Error("Stop never ran")
	}
}

// A failed stop after a drain that closed a live agent leaves the sandbox
// RUNNING and unknown, and its probe makes an orphan record.
func TestFailedStopProbesTheDrainedAgent(t *testing.T) {
	e := newOrphanEnv(t)
	sb := e.launched(t, "p")
	drained := 0
	e.m.SetStopDrain(func(*pb.Sandbox, time.Duration) bool { drained++; return true })
	e.run.stopErr = errors.New("stop failed")
	e.tokens.issue(sb.GetId(), 3)
	e.run.setOut("p", aliveOut(3))
	if _, err := e.m.Stop(context.Background(), sb.GetId()); err == nil {
		t.Fatal("stop should fail")
	}
	e.waitState(t, sb.GetId(), pb.AgentCheck_STATE_DETACHED)
	if drained != 1 || e.get(t, sb.GetId()).GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Error("the sandbox stays RUNNING after a failed stop")
	}
	if e.notes.n() != 1 {
		t.Error("the orphan is surfaced")
	}
	// A successful stop then drops the record and retires the token.
	e.run.stopErr = nil
	if _, err := e.m.Stop(context.Background(), sb.GetId()); err != nil {
		t.Fatal(err)
	}
	if e.state(sb.GetId()) != pb.AgentCheck_STATE_CLEAR || e.get(t, sb.GetId()).GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED {
		t.Error("a confirmed stop drops the orphan with EXITED")
	}
	if _, err := e.m.Admit(sb.GetId()); err != nil {
		t.Errorf("Admit after the stop = %v", err)
	}
}

// A kit attach keeps the record through CREATING and probes once at RUNNING;
// a failed recreate (ERROR) keeps it.
func TestAddKitWithAnOrphan(t *testing.T) {
	e := newOrphanEnv(t)
	sb := e.launched(t, "p")
	id := sb.GetId()
	e.orphanOn(t, id, "p")
	e.run.kitErr = errors.New("recreate failed")
	if _, err := e.m.AddKit(context.Background(), id, "/kits/k", nil, nil, nil); err == nil {
		t.Fatal("kit attach should fail")
	}
	if e.state(id) != pb.AgentCheck_STATE_DETACHED {
		t.Error("ERROR dropped the orphan record")
	}
	// The recreate succeeds this time and ends the agent.
	e.run.setOut("p", goneOut)
	before := e.run.count("probe")
	e.run.kitErr = nil
	if _, err := e.m.AddKit(context.Background(), id, "/kits/k", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, id, pb.AgentCheck_STATE_CLEAR)
	if e.run.count("probe") != before+1 {
		t.Errorf("probes %d after the kit attach, want one", e.run.count("probe")-before)
	}
	if e.get(t, id).GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED {
		t.Error("the gone probe writes EXITED")
	}
}

// An external stop, seen by reconcile, drops the record through the
// published STOPPED.
func TestExternalStopDropsTheOrphan(t *testing.T) {
	e := newOrphanEnv(t)
	sb := e.launched(t, "p")
	e.orphanOn(t, sb.GetId(), "p")
	e.run.mu.Lock()
	e.run.running["p"] = false
	e.run.mu.Unlock()
	time.Sleep(5 * time.Millisecond) // reconcile settles only records older than the manager
	if _, err := e.m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.state(sb.GetId()) != pb.AgentCheck_STATE_CLEAR {
		t.Error("an external stop keeps the orphan record")
	}
}

// Rename takes no runtime lock, and keeps the record and the token.
func TestRenameIgnoresTheRuntimeLock(t *testing.T) {
	e := newOrphanEnv(t)
	sb := e.launched(t, "p")
	if _, err := e.m.Stop(context.Background(), sb.GetId()); err != nil {
		t.Fatal(err)
	}
	unlock, ok := e.m.TryLockRuntime(sb.GetId())
	if !ok {
		t.Fatal("lock")
	}
	defer unlock()
	if _, err := e.m.Rename(context.Background(), sb.GetId(), "q"); err != nil {
		t.Fatal(err)
	}
}

func TestListedRunningStrict(t *testing.T) {
	e := newOrphanEnv(t)
	sb := &pb.Sandbox{Id: "x", ContainerRef: "x"}
	if e.m.listedRunningStrict(context.Background(), sb) {
		t.Error("not listed reads stopped")
	}
	e.run.mu.Lock()
	e.run.listErr = errors.New("down")
	e.run.mu.Unlock()
	if !e.m.listedRunningStrict(context.Background(), sb) {
		t.Error("a listing error counts as still running")
	}
}

// --- the exec-client exit ---

// An exec client's exit always probes: a live agent becomes an orphan record
// (no EXITED); a dead one is EXITED once.
func TestClientExitProbes(t *testing.T) {
	e := newOrphanEnv(t)
	e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	e.tokens.issue("s", 2)
	hold := make(chan struct{})
	e.run.setHook(func(string, string) { <-hold })
	e.run.setOut("s", aliveOut(2))
	e.m.AgentClientExited("s", 2)
	if _, err := e.m.Admit("s"); !errors.Is(err, agent.ErrChecking) {
		t.Errorf("Admit right after the exit = %v", err)
	}
	close(hold)
	e.waitState(t, "s", pb.AgentCheck_STATE_DETACHED)
	if e.get(t, "s").GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_EXITED {
		t.Error("a live agent was taken for an exit")
	}
	// It exits: the next refusal's probe finds it gone.
	e.run.setOut("s", goneOut)
	orphanRefusalDebounce = 0
	if _, err := e.m.Admit("s"); !errors.Is(err, agent.ErrDetached) {
		t.Errorf("Admit = %v", err)
	}
	e.waitState(t, "s", pb.AgentCheck_STATE_CLEAR)
	if e.get(t, "s").GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED {
		t.Error("a gone agent is EXITED")
	}
	// A gone probe on an EXITED record changes nothing.
	e.m.AgentClientExited("s", 2)
	e.waitState(t, "s", pb.AgentCheck_STATE_CLEAR)
	if sb := e.get(t, "s"); sb.GetAgent().GetAgentCheck() != nil || sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED {
		t.Errorf("a second gone probe wrote %v", sb.GetAgent())
	}
	// A live session of the probed generation is the daemon's own agent.
	e.live.m["s"] = 2
	e.run.setOut("s", aliveOut(2))
	e.m.AgentClientExited("s", 2)
	e.waitState(t, "s", pb.AgentCheck_STATE_CLEAR)
}

// The backstop re-probes an orphan that holds a run.
func TestOrphanBackstop(t *testing.T) {
	e := newOrphanEnv(t)
	orphanBackstop = 10 * time.Millisecond
	e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, &pb.WorkerRun{Id: "r"})
	e.put(t, "free", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	e.run.setOut("s", aliveOut(1))
	e.run.setOut("free", aliveOut(1))
	e.m.AgentClientExited("s", 1)
	e.m.AgentClientExited("free", 1)
	e.waitState(t, "s", pb.AgentCheck_STATE_DETACHED)
	e.waitState(t, "free", pb.AgentCheck_STATE_DETACHED)
	ctx, cancel := context.WithCancel(context.Background())
	go e.m.RunOrphanBackstop(ctx)
	e.run.setOut("s", goneOut)
	e.waitState(t, "s", pb.AgentCheck_STATE_CLEAR)
	cancel()
	if e.state("free") != pb.AgentCheck_STATE_DETACHED {
		t.Error("the backstop probes only sandboxes that hold a run")
	}
	e.m.RecheckOrphan("nothing") // no state: nothing to probe
}

// --- the developer's stop ---

func TestReapOrphan(t *testing.T) {
	e := newOrphanEnv(t)
	ctx := context.Background()
	e.put(t, "plain", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	if err := e.m.ReapOrphan(ctx, "plain", ReapReplace); err != nil || e.run.count("reap") != 0 {
		t.Fatalf("no orphan: %v, %d reaps", err, e.run.count("reap"))
	}

	e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_WORKING, &pb.WorkerRun{Id: "r", StepRole: pb.StepRole_STEP_ROLE_ACTION})
	e.tokens.issue("s", 4)
	e.run.setOut("s", aliveOut(4))
	e.m.AgentClientExited("s", 4)
	e.waitState(t, "s", pb.AgentCheck_STATE_DETACHED)

	unlock, _ := e.m.TryLockRuntime("s")
	if err := e.m.ReapOrphan(ctx, "s", ReapReplace); !errors.Is(err, agent.ErrBusy) {
		t.Errorf("busy: %v", err)
	}
	unlock()
	e.run.reapFail = true
	if err := e.m.ReapOrphan(ctx, "s", ReapReplace); err == nil || e.state("s") != pb.AgentCheck_STATE_DETACHED {
		t.Errorf("a failed exec stops nothing: %v", err)
	}
	e.run.reapFail = false
	if err := e.m.ReapOrphan(ctx, "s", ReapReplace); err != nil {
		t.Fatal(err)
	}
	sb := e.get(t, "s")
	if e.state("s") != pb.AgentCheck_STATE_CLEAR || sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED ||
		sb.GetController().GetRun().GetStepNote() != replacedNote || len(e.tokens.Unretired("s")) != 0 {
		t.Errorf("after Replace: %v", sb)
	}

	// A restart writes no EXITED; a setup run keeps its note.
	e.put(t, "r", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, &pb.WorkerRun{Id: "r2", StepRole: pb.StepRole_STEP_ROLE_SETUP, StepNote: "n"})
	e.run.setOut("r", aliveOut(1))
	e.m.AgentClientExited("r", 1)
	e.waitState(t, "r", pb.AgentCheck_STATE_DETACHED)
	if err := e.m.ReapOrphan(ctx, "r", ReapRestart); err != nil {
		t.Fatal(err)
	}
	if sb := e.get(t, "r"); sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE || sb.GetController().GetRun().GetStepNote() != "n" {
		t.Errorf("after a restart reap: %v", sb)
	}

	// Not running: refused. Listed stopped: nothing to stop.
	e.m.orphans.setUnknown("plain")
	e.run.mu.Lock()
	e.run.running["plain"] = false
	e.run.mu.Unlock()
	if err := e.m.ReapOrphan(ctx, "plain", ReapReplace); err != nil || e.run.count("reap") != 3 {
		t.Errorf("listed stopped: %v, %d reaps", err, e.run.count("reap"))
	}
	e.put(t, "down", pb.SandboxState_SANDBOX_STATE_STOPPED, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	e.m.orphans.setUnknown("down")
	if err := e.m.ReapOrphan(ctx, "down", ReapReplace); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("stopped: %v", err)
	}
	if err := e.m.ReapOrphan(ctx, "missing", ReapReplace); err != nil {
		t.Errorf("no state: %v", err)
	}
	e.m.orphans.setUnknown("missing")
	if err := e.m.ReapOrphan(ctx, "missing", ReapReplace); err == nil {
		t.Error("an unknown sandbox id")
	}
}

// Only the developer's two paths may stop an orphan (T047 guard): AttachAgent
// with replace_orphan, and DeliverWorkerStep's restart.
func TestReapOrphanCallSites(t *testing.T) {
	allowed := map[string]bool{
		filepath.Join("grpc", "subscribe.go"): true,
		filepath.Join("worker", "step.go"):    true,
	}
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(string(b), ".ReapOrphan(") && !allowed[rel] {
			t.Errorf("%s calls ReapOrphan; only the developer's Replace and restart may", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join("..", "..", "cmd", "sxbd", "main.go"))
	if strings.Contains(string(b), ".ReapOrphan(") {
		t.Error("the daemon itself never stops an orphan")
	}
}

func TestMarkAgentUnknown(t *testing.T) {
	e := newOrphanEnv(t)
	e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	e.m.MarkAgentUnknown("s")
	if _, err := e.m.Admit("s"); !errors.Is(err, agent.ErrChecking) {
		t.Errorf("Admit = %v", err)
	}
	waitUntil(t, func() bool { return e.state("s") == pb.AgentCheck_STATE_UNKNOWN })
}

func TestStateWord(t *testing.T) {
	if w := stateWord(pb.SandboxState_SANDBOX_STATE_STOPPED); w != "stopped" {
		t.Errorf("stateWord = %q", w)
	}
}
