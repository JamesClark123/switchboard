package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// leftover writes in-progress records as a crashed daemon would leave them.
func (e *saveEnv) leftover(recs ...*pb.McpSaveInProgress) {
	e.t.Helper()
	if _, err := e.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		if s.SavesInProgress == nil {
			s.SavesInProgress = map[string]*pb.McpSaveInProgress{}
		}
		for _, r := range recs {
			s.SavesInProgress[r.GetName()] = r
		}
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

func fastRecovery(t *testing.T) {
	b, bm := recoverBackoff, recoverBackoffMax
	recoverBackoff, recoverBackoffMax = 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { recoverBackoff, recoverBackoffMax = b, bm })
}

func TestStartupRecoveryResolvesEveryLeftover(t *testing.T) {
	fastRecovery(t)
	e := newSaveEnv(t)
	prev := remote("https://old/mcp")
	prevC := remote("https://old/mcp")
	prevC.ClientId = "c1"
	newC := remote("https://new/mcp")
	newC.ClientId = "c2"
	e.sec.vals["mcp:nw:a"] = "v"
	e.write("lr.srv", "remote http")
	e.write("ln.srv", "remote http")
	e.leftover(
		// not listed, new server: the planned created secret is removed.
		&pb.McpSaveInProgress{Name: "nw", Definition: remote("https://n/mcp", "A: ${a}"), PlannedCreatedSecrets: []string{"mcp:nw:a"}},
		// not listed, replace: a pending replace holding both definitions.
		&pb.McpSaveInProgress{Name: "nr", AttachByDefault: true, Definition: remote("https://new/mcp"), Previous: prev, PlannedOverwrittenSecrets: []string{"mcp:nr:k"}},
		// listed, replace with a client change: previous kept, re-bind pending.
		&pb.McpSaveInProgress{Name: "lr", Definition: newC, Previous: prevC},
		// listed, new: registered with its mark.
		&pb.McpSaveInProgress{Name: "ln", AttachByDefault: true, Definition: remote("https://ln/mcp")},
	)
	e.touch("ls.fail") // the first attempt cannot list
	gate := make(chan struct{})
	testHookRecover = func() { <-gate }
	t.Cleanup(func() { testHookRecover = nil })
	e.m = e.newManager()

	// While recovering: the toggle, Authorize and the definition read refuse.
	if _, err := e.m.SetDefault(context.Background(), "ln", false); !errors.Is(err, ErrRecovering) {
		t.Errorf("toggle = %v", err)
	}
	if err := e.m.Authorize(context.Background(), "ln", nil, false, nil); !errors.Is(err, ErrRecovering) {
		t.Errorf("authorize = %v", err)
	}
	if e.m.ReplaceState("nr") != BeingReplaced {
		t.Error("a recovering replace is not being replaced")
	}
	e.rm("ls.fail")
	close(gate)
	waitFor(t, "recovery", func() bool { return len(e.settings().GetSavesInProgress()) == 0 })
	waitFor(t, "recovering cleared", func() bool { return !e.m.stateSnapshot("ln").recovering })

	s := e.settings()
	if e.sec.has("mcp:nw:a") || s.GetDefinitions()["nw"] != nil {
		t.Errorf("new not listed = %v", s)
	}
	p := s.GetPendingReplaces()["nr"]
	if p == nil || p.GetRevision() != 1 || !p.GetAttachByDefault() || s.GetDefinitions()["nr"].GetUrl() != "https://old/mcp" {
		t.Errorf("replace not listed = %v", p)
	}
	if p.GetError() != "daemon stopped during the save; overwritten secrets cannot be restored: mcp:nr:k" {
		t.Errorf("error = %q", p.GetError())
	}
	if d := s.GetDefinitions()["lr"]; d.GetUrl() != "https://old/mcp" || !d.GetRebindPending() {
		t.Errorf("replace listed = %v", d)
	}
	if !has(e.endedHooks(), "lr:registered") {
		t.Errorf("hooks = %v", e.endedHooks())
	}
	if s.GetDefinitions()["ln"].GetUrl() != "https://ln/mcp" || !s.GetMarks()["ln"] {
		t.Errorf("new listed = %v", s)
	}
	if !e.m.stateSnapshot("nr").pending || e.m.ReplaceState("nr") != BeingReplaced {
		t.Error("the recovered pending replace is not in the state map")
	}
	if e.m.ReplaceState("lr") != EndedRegistered || e.m.ReplaceState("unknown") != EndedRegistered {
		t.Error("ReplaceState of an ended replace")
	}
}

func TestStartupRecoveryOfARetry(t *testing.T) {
	fastRecovery(t)
	e := newSaveEnv(t)
	e.write("rl.srv", "remote http")
	if _, err := e.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		s.PendingReplaces = map[string]*pb.McpPendingReplace{
			"rn": {Name: "rn", Revision: 3, Previous: remote("https://p/mcp"), RemovalFailedSecrets: []string{"mcp:rn:x"}},
			"rl": {Name: "rl", Revision: 1, Previous: remote("https://p/mcp")},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.leftover(
		&pb.McpSaveInProgress{Name: "rn", Retry: true, Definition: remote("https://r/mcp"), Previous: remote("https://p/mcp"), PlannedCreatedSecrets: []string{"mcp:rn:x", "mcp:rn:y"}},
		&pb.McpSaveInProgress{Name: "rl", Retry: true, AttachByDefault: true, Definition: remote("https://r/mcp"), Previous: remote("https://p/mcp")},
	)
	e.m = e.newManager()
	waitFor(t, "recovery", func() bool { return len(e.settings().GetSavesInProgress()) == 0 })
	s := e.settings()
	p := s.GetPendingReplaces()["rn"]
	if p.GetRevision() != 4 || p.GetDefinition().GetUrl() != "https://r/mcp" || len(p.GetRemovalFailedSecrets()) != 2 || p.GetError() != "daemon stopped during the retry" {
		t.Errorf("retry not listed = %v", p)
	}
	if s.GetPendingReplaces()["rl"] != nil || !s.GetMarks()["rl"] || s.GetDefinitions()["rl"].GetUrl() != "https://r/mcp" {
		t.Errorf("retry listed = %v", s)
	}
	waitFor(t, "state", func() bool { return !e.m.stateSnapshot("rl").pending && e.m.stateSnapshot("rn").pending })
	if !has(e.endedHooks(), "rl:registered") {
		t.Errorf("hooks = %v", e.endedHooks())
	}
}

func TestRecoveryStopsOnShutdown(t *testing.T) {
	fastRecovery(t)
	e := newSaveEnv(t)
	e.touch("ls.fail")
	e.leftover(&pb.McpSaveInProgress{Name: "x", Definition: remote("https://x")})
	e.m = e.newManager()
	time.Sleep(50 * time.Millisecond)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	if len(e.settings().GetSavesInProgress()) != 1 || !e.m.stateSnapshot("x").recovering {
		t.Error("the leftover was resolved without a list")
	}
}

func TestPersistFailureRepliesUnavailableThenLands(t *testing.T) {
	d, b := persistRetryDelay, persistBackoff
	persistRetryDelay, persistBackoff = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { persistRetryDelay, persistBackoff = d, b })
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp", "A: ${a}"), false, nil)
	v := e.open("gh")

	// A switchboard-only save: recording, the queue refused, the stored secret named.
	e.store.setFail(1000)
	_, err := e.edit(v, v.GetDefinition(), true, map[string]string{"mcp:gh:a": "v"})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "stored secrets: mcp:gh:a") {
		t.Fatalf("err = %v", err)
	}
	if !e.m.stateSnapshot("gh").recording {
		t.Error("not recording")
	}
	if _, err := e.edit(v, v.GetDefinition(), true, nil); !errors.Is(err, ErrBeingReplaced) {
		t.Errorf("a new taker = %v", err)
	}
	if _, err := e.m.SetDefault(context.Background(), "gh", false); !errors.Is(err, ErrBeingReplaced) {
		t.Errorf("toggle = %v", err)
	}
	e.store.setFail(0)
	waitFor(t, "the retried persist", func() bool { return e.settings().GetMarks()["gh"] })
	waitFor(t, "the lock", func() bool { return e.m.stateSnapshot("gh").lockers == 0 && !e.m.stateSnapshot("gh").recording })

	// A registration change: the in-progress record lands, the persist fails.
	v = e.open("gh")
	fails := 0
	testHookStep = func(step string) {
		if step == "in-progress" && fails == 0 {
			fails++
			e.store.setFail(1000)
		}
	}
	t.Cleanup(func() { testHookStep = nil })
	_, err = e.edit(v, remote("https://y/mcp", "A: ${a}"), true, nil)
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "the host was changed") {
		t.Fatalf("replace err = %v", err)
	}
	if !e.m.stateSnapshot("gh").saving || e.m.ReplaceState("gh") != BeingReplaced {
		t.Error("not saving")
	}
	// Daemon shutdown ends the retry; the record stays for the next start.
	e.cancel()
	waitFor(t, "the lock", func() bool { return e.m.stateSnapshot("gh").lockers == 0 })
	e.store.setFail(0)
	if e.settings().GetSavesInProgress()["gh"] == nil {
		t.Error("the in-progress record was dropped")
	}
}

func TestLockQueueRefusedWhenTheHolderStartsSaving(t *testing.T) {
	e := newSaveEnv(t)
	l, release, err := e.m.lockName(context.Background(), "gh", lockOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		_, r, err := e.m.lockName(context.Background(), "gh", lockOpts{})
		if r != nil {
			r()
		}
		got <- err
	}()
	waitFor(t, "the waiter", func() bool { return e.m.stateSnapshot("gh").lockers == 2 })
	e.m.markRefusing("gh", l, true)
	if err := <-got; !errors.Is(err, ErrBeingReplaced) {
		t.Errorf("waiter = %v", err)
	}
	// A cancelled waiter is dropped too.
	e.m.clearRefusing("gh", l, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _, err := e.m.lockName(ctx, "gh", lockOpts{})
		got <- err
	}()
	waitFor(t, "the waiter", func() bool { return e.m.stateSnapshot("gh").lockers == 2 })
	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}
	release()
	if s := e.m.stateSnapshot("gh"); s.lockers != 0 || s.generation != 1 {
		t.Errorf("state = %+v", s)
	}
	e.m.mu.Lock()
	n := len(e.m.busy)
	e.m.mu.Unlock()
	if n != 0 {
		t.Error("the lock entry leaked")
	}
	// An authSensitive taker during an authorization.
	e.m.mu.Lock()
	e.m.inflight["gh"] = 0
	e.m.mu.Unlock()
	if _, _, err := e.m.lockName(context.Background(), "gh", lockOpts{authSensitive: true}); refusalReason(err) != pb.McpSaveRefusal_AUTH_IN_PROGRESS {
		t.Errorf("auth = %v", err)
	}
}

func TestStaleFor(t *testing.T) {
	e := newSaveEnv(t)
	gens := e.m.generations()
	if e.m.staleFor("none", gens) {
		t.Error("an unknown name is stale")
	}
	_, release, _ := e.m.lockName(context.Background(), "gh", lockOpts{})
	e.m.stateMu.Lock()
	locked := e.m.staleFor("gh", gens)
	e.m.stateMu.Unlock()
	release()
	e.m.stateMu.Lock()
	moved := e.m.staleFor("gh", gens)
	fresh := e.m.staleFor("gh", e.m.generationsLocked())
	e.m.stateMu.Unlock()
	if !locked || !moved || fresh {
		t.Errorf("locked %v moved %v fresh %v", locked, moved, fresh)
	}
}

// generationsLocked is generations for a caller already holding stateMu.
func (m *Manager) generationsLocked() map[string]uint64 {
	out := map[string]uint64{}
	for n, s := range m.state {
		out[n] = s.generation
	}
	return out
}

func TestRefusalErrorAndIs(t *testing.T) {
	err := refuse(pb.McpSaveRefusal_GONE, "%s is gone", "gh")
	if err.Error() != "gh is gone" || !errors.Is(err, &Refusal{Reason: pb.McpSaveRefusal_GONE}) || errors.Is(err, ErrBeingReplaced) {
		t.Errorf("refusal = %v", err)
	}
	if !errors.Is(ErrRecovering, &Refusal{Reason: pb.McpSaveRefusal_UNVERIFIED}) {
		t.Error("ErrRecovering is UNVERIFIED")
	}
}
