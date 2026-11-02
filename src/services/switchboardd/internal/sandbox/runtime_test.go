package sandbox

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
)

// --- the read/write split (SWI-26 B1) ---

// blockOn makes the runner's first call of kind block until the returned
// release is called, and returns once that call has started.
func (e *orphanEnv) blockOn(t *testing.T, kind string, start func()) (release func()) {
	t.Helper()
	in, out := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.run.setHook(func(k, _ string) {
		if k == kind {
			once.Do(func() { close(in); <-out })
		}
	})
	go start()
	select {
	case <-in:
	case <-time.After(3 * time.Second):
		t.Fatalf("no %s call", kind)
	}
	return sync.OnceFunc(func() { close(out) })
}

// A probe or a reap in flight holds the runtime lock only for reading: a
// developer's service start or stop (a read try-lock) goes ahead beside it,
// while a lifecycle operation (the write lock) still waits for it.
func TestReadersShareTheRuntimeLock(t *testing.T) {
	for _, kind := range []string{"probe", "reap"} {
		t.Run(kind, func(t *testing.T) {
			e := newOrphanEnv(t)
			e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
			e.tokens.issue("s", 2)
			e.run.setOut("s", aliveOut(2))
			e.m.orphans.setUnknown("s")
			done := make(chan struct{})
			release := e.blockOn(t, kind, func() {
				defer close(done)
				if kind == "probe" {
					e.m.orphans.probe("s")
				} else {
					_ = e.m.ReapOrphan(context.Background(), "s", ReapReplace)
				}
			})
			defer release()
			unlock, ok := e.m.TryRLockRuntime("s")
			if !ok {
				t.Fatalf("a service request is refused during a %s", kind)
			}
			unlock()
			if unlock, ok := e.m.TryLockRuntime("s"); ok {
				unlock()
				t.Errorf("the write lock was granted during a %s", kind)
			}
			release()
			<-done
		})
	}
}

// A reap and a spawn never overlap (SWI-26 B1): both hold the orphan gate, so
// whichever comes second is refused busy, and the reap's script never runs
// beside an admitted spawn.
func TestReapAndAdmitExclude(t *testing.T) {
	t.Run("reap first", func(t *testing.T) {
		e := newOrphanEnv(t)
		e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
		e.m.orphans.setUnknown("s")
		done := make(chan error, 1)
		release := e.blockOn(t, "reap", func() { done <- e.m.ReapOrphan(context.Background(), "s", ReapReplace) })
		defer release()
		// The check clears while the reap runs (a probe found it gone): the
		// gate's own state no longer refuses, the orphan gate does.
		e.m.orphans.drop("s")
		if rel, err := e.m.Admit("s"); !errors.Is(err, agent.ErrBusy) {
			if rel != nil {
				rel()
			}
			t.Errorf("Admit during a reap = %v", err)
		}
		release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		rel, err := e.m.Admit("s")
		if err != nil {
			t.Fatalf("Admit after the reap = %v", err)
		}
		rel()
	})
	t.Run("admit first", func(t *testing.T) {
		e := newOrphanEnv(t)
		e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
		rel, err := e.m.Admit("s")
		if err != nil {
			t.Fatal(err)
		}
		e.m.orphans.setUnknown("s")
		if err := e.m.ReapOrphan(context.Background(), "s", ReapReplace); !errors.Is(err, agent.ErrBusy) || e.run.count("reap") != 0 {
			t.Errorf("Replace during a spawn = %v, %d reaps", err, e.run.count("reap"))
		}
		rel()
		if err := e.m.ReapOrphan(context.Background(), "s", ReapReplace); err != nil || e.run.count("reap") != 1 {
			t.Errorf("Replace after the spawn = %v, %d reaps", err, e.run.count("reap"))
		}
	})
	// Started together, over and over: never both inside at once.
	t.Run("together", func(t *testing.T) {
		e := newOrphanEnv(t)
		e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
		var mu sync.Mutex
		spawning, reaping := false, false
		e.run.setHook(func(k, _ string) {
			if k != "reap" {
				return
			}
			mu.Lock()
			reaping = true
			if spawning {
				t.Error("the reap's script ran beside an admitted spawn")
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			reaping = false
			mu.Unlock()
		})
		for range 50 {
			e.m.orphans.setUnknown("s")
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_ = e.m.ReapOrphan(context.Background(), "s", ReapReplace)
			}()
			go func() {
				defer wg.Done()
				// Past the state check, as when a probe clears the sandbox
				// between a reap's own check and its script.
				rel, ok := e.m.orphanGate("s")
				if !ok {
					return
				}
				mu.Lock()
				spawning = true
				if reaping {
					t.Error("a spawn was admitted during the reap's script")
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				spawning = false
				mu.Unlock()
				rel()
			}()
			wg.Wait()
		}
	})
}

// The gateway's and the MCP overlay's `sbx mcp load`/`unload` take the read
// lock: refused while a lifecycle operation holds the sandbox, and free beside
// a probe (SWI-26 B4).
func TestMcpLoadTakesTheRuntimeReadLock(t *testing.T) {
	e := newOrphanEnv(t)
	sb := e.launched(t, "p")
	id := sb.GetId()
	unlock, ok := e.m.TryLockRuntime(id)
	if !ok {
		t.Fatal("lock")
	}
	if _, err := e.m.AttachMcp(context.Background(), id, "srv", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("AttachMcp under a lifecycle operation = %v", err)
	}
	unlock()
	if _, err := e.m.AttachMcp(context.Background(), id, "srv", nil); err != nil {
		t.Fatal(err)
	}
	unlock, _ = e.m.TryLockRuntime(id)
	if _, err := e.m.DetachMcp(context.Background(), id, "srv", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("DetachMcp under a lifecycle operation = %v", err)
	}
	unlock()
	e.run.mu.Lock()
	loads, unloads := len(e.run.mcpLoads), len(e.run.mcpUnloads)
	e.run.mu.Unlock()
	if loads != 1 || unloads != 0 {
		t.Errorf("%d loads, %d unloads; want 1, 0", loads, unloads)
	}
	// Beside a probe in flight, the detach runs.
	e.tokens.issue(id, 2)
	e.run.setOut("p", aliveOut(2))
	e.m.orphans.setUnknown(id)
	done := make(chan struct{})
	release := e.blockOn(t, "probe", func() { defer close(done); e.m.orphans.probe(id) })
	defer release()
	if _, err := e.m.DetachMcp(context.Background(), id, "srv", nil); err != nil {
		t.Errorf("DetachMcp beside a probe = %v", err)
	}
	release()
	<-done
}

// --- the shared listing (SWI-26 B3) ---

// N probes at once list the runtime once.
func TestConcurrentProbesShareOneListing(t *testing.T) {
	e := newOrphanEnv(t)
	gate := make(chan struct{})
	e.run.pmu.Lock()
	e.run.listHook = func() { <-gate }
	e.run.pmu.Unlock()
	ids := []string{"a", "b", "c", "d", "e", "f"}
	for i, id := range ids {
		e.put(t, id, pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
		e.tokens.issue(id, int64(i+1))
		e.run.setOut(id, goneOut)
		e.m.orphans.setUnknown(id)
		go e.m.orphans.probe(id)
	}
	// Let every probe reach the listing before it answers.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	for _, id := range ids {
		e.waitState(t, id, pb.AgentCheck_STATE_CLEAR)
	}
	e.run.mu.Lock()
	lists := e.run.lists
	e.run.mu.Unlock()
	if lists != 1 {
		t.Errorf("%d listings for %d probes, want 1", lists, len(ids))
	}
}

// A caller that gives up never fails the shared call for the others, a
// lifecycle operation drops the cached listing, and a failed listing is not
// reused.
func TestSharedListing(t *testing.T) {
	e := newOrphanEnv(t)
	e.put(t, "s", pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
	lists := func() int {
		e.run.mu.Lock()
		defer e.run.mu.Unlock()
		return e.run.lists
	}
	gate := make(chan struct{})
	e.run.pmu.Lock()
	e.run.listHook = func() { <-gate }
	e.run.pmu.Unlock()
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.m.listShared(cctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller = %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.m.listShared(context.Background()); done <- err }()
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("a waiter failed with the cancelled caller: %v", err)
	}
	if n := lists(); n != 1 {
		t.Fatalf("%d listings, want 1", n)
	}
	// Cached: no second call.
	if _, err := e.m.listShared(context.Background()); err != nil || lists() != 1 {
		t.Errorf("cached read = %v, %d listings", err, lists())
	}
	// A lifecycle operation drops it.
	e.m.runtimeOp("s")()
	if _, err := e.m.listShared(context.Background()); err != nil || lists() != 2 {
		t.Errorf("after an operation = %v, %d listings", err, lists())
	}
	// A failure is shared with its waiters but never cached.
	e.m.lists.invalidate()
	e.run.mu.Lock()
	e.run.listErr = errors.New("boom")
	e.run.mu.Unlock()
	if _, err := e.m.listShared(context.Background()); err == nil {
		t.Error("the failure is reported")
	}
	e.run.mu.Lock()
	e.run.listErr = nil
	e.run.mu.Unlock()
	if _, err := e.m.listShared(context.Background()); err != nil || lists() != 4 {
		t.Errorf("after a failure = %v, %d listings, want a fresh one", err, lists())
	}
}

// --- the start-up check off the serving path (SWI-26 B2) ---

// A generation minted between the start-up check's snapshot and its listing
// (an agent spawned since) is neither retired nor marked EXITED from the
// older listing; a sandbox with none is settled as before.
func TestStartupCheckKeepsAGenerationMintedSinceTheSnapshot(t *testing.T) {
	e := newOrphanEnv(t)
	for _, id := range []string{"minted", "plain"} {
		e.put(t, id, pb.SandboxState_SANDBOX_STATE_RUNNING, pb.AgentStatus_AGENT_STATUS_IDLE, nil)
		e.tokens.issue(id, 2)
		e.run.mu.Lock()
		e.run.running[id] = false // listed stopped
		e.run.mu.Unlock()
	}
	e.run.pmu.Lock()
	e.run.listHook = func() { e.tokens.issue("minted", 3) }
	e.run.pmu.Unlock()
	e.m.CheckOrphansAtStart(context.Background())
	if got := e.tokens.Unretired("minted"); !slices.Contains(got, 3) {
		t.Errorf("the new generation was retired: unretired %v", got)
	}
	if st := e.get(t, "minted").GetAgent().GetStatus(); st == pb.AgentStatus_AGENT_STATUS_EXITED {
		t.Error("EXITED written over an agent spawned since the listing")
	}
	if len(e.tokens.Unretired("plain")) != 0 || e.get(t, "plain").GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED {
		t.Error("a listed-stopped sandbox is settled: tokens retired, EXITED")
	}
}
