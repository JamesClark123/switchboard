package orchestrator

import (
	"errors"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func withHost(t *testing.T, env *testEnv, orchID, hostID, target string) *runtime {
	t.Helper()
	if _, err := env.svc.SetHosts(orchID, []*pb.OrchestratorHost{{HostId: hostID, DisplayName: hostID, SshTarget: target}}); err != nil {
		t.Fatal(err)
	}
	rt, _ := env.svc.runtimeFor(orchID)
	return rt
}

// A failing dial leaves the host unreachable with the dial's error and is
// retried after the backoff under the fake clock.
func TestLinkRetriesWithBackoff(t *testing.T) {
	env := newEnv(t)
	env.dials.err = errors.New("ssh: Could not resolve hostname box")
	o, _ := env.svc.Create("bot")
	rt := withHost(t, env, o.GetId(), "box", "me@box")
	waitFor(t, func() bool { return env.dials.count() == 1 })
	waitFor(t, func() bool {
		ok, reason := rt.reachability("box")
		return !ok && strings.Contains(reason, "Could not resolve")
	})
	// No retry before the backoff elapses…
	env.clock.Advance(linkRetryMin - 1)
	if env.dials.count() != 1 {
		t.Fatalf("retried early: %d dials", env.dials.count())
	}
	// …then one retry at linkRetryMin, the next after double.
	waitFor(t, func() bool { return env.clock.pending() >= 1 })
	env.clock.Advance(1)
	waitFor(t, func() bool { return env.dials.count() == 2 })
	waitFor(t, func() bool { return env.clock.pending() >= 1 })
	env.clock.Advance(2 * linkRetryMin)
	waitFor(t, func() bool { return env.dials.count() == 3 })
	// Backoff caps at linkRetryMax.
	l := rt.link("box")
	l.mu.Lock()
	l.backoff = linkRetryMax
	l.mu.Unlock()
	if d := l.nextBackoff(); d != linkRetryMax {
		t.Errorf("capped backoff = %v", d)
	}
}

// A daemon that answers the worker-side RPCs with UNIMPLEMENTED is reported as
// too old (the `u` update fan-out is the remedy).
func TestLinkTooOldDaemon(t *testing.T) {
	env := newEnv(t)
	w := newFakeWorker("box")
	w.tooOld = true
	env.dials.socks["me@box"] = w.serve(t)
	o, _ := env.svc.Create("bot")
	rt := withHost(t, env, o.GetId(), "box", "me@box")
	waitFor(t, func() bool {
		ok, reason := rt.reachability("box")
		return !ok && reason == "daemon on box is too old — update with u"
	})
	if _, err := rt.apiFor("box"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("apiFor on a down link = %v", err)
	}
	if _, err := rt.apiFor("nowhere"); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "no host entry for nowhere") {
		t.Errorf("apiFor on a missing entry = %v", err)
	}
}

// Over a healthy link the first frame is hello and pings follow at the
// configured cadence under the fake clock; the host becomes reachable.
func TestLinkHelloThenPings(t *testing.T) {
	env := newEnv(t)
	w := newFakeWorker("box")
	env.dials.socks["me@box"] = w.serve(t)
	o, _ := env.svc.Create("bot")
	rt := withHost(t, env, o.GetId(), "box", "me@box")
	waitFor(t, func() bool { ok, _ := rt.reachability("box"); return ok })
	waitFor(t, func() bool { return len(w.frameKinds()) >= 1 })
	if kinds := w.frameKinds(); kinds[0] != "hello" {
		t.Fatalf("first frame = %v, want hello", kinds)
	}
	w.mu.Lock()
	hello := w.frames[0].GetHello()
	w.mu.Unlock()
	if hello.GetOrchestratorId() != o.GetId() || hello.GetName() != "bot" {
		t.Errorf("hello = %+v", hello)
	}
	waitFor(t, func() bool { return env.clock.pending() >= 1 })
	env.clock.Advance(linkPingInterval)
	waitFor(t, func() bool { return len(w.frameKinds()) == 2 && w.frameKinds()[1] == "ping" })
	waitFor(t, func() bool { return env.clock.pending() >= 1 })
	env.clock.Advance(linkPingInterval)
	waitFor(t, func() bool { return len(w.frameKinds()) == 3 })
	// A host that stays unknown to the orchestrator still answers reachability.
	det, _ := env.svc.Get(o.GetId())
	if !det.GetHosts()[0].GetReachable() || det.GetHosts()[0].GetError() != "" {
		t.Errorf("detail host = %+v", det.GetHosts()[0])
	}
	// Removing the entry tears the link down; the fake's link count stays 1.
	if _, err := env.svc.SetHosts(o.GetId(), nil); err != nil {
		t.Fatal(err)
	}
	if rt.link("box") != nil {
		t.Error("link should be gone")
	}
}

func TestDescribeAndTooOld(t *testing.T) {
	if describe(nil, "h") != "" {
		t.Error("nil describes as empty")
	}
	if got := describe(errors.New("first\nlast line"), "h"); got != "last line" {
		t.Errorf("describe multi-line = %q", got)
	}
	if !isTooOld(statusUnimplemented()) || isTooOld(errors.New("x")) {
		t.Error("isTooOld")
	}
	if got := describe(statusUnimplemented(), "box"); got != errTooOld("box") {
		t.Errorf("describe too old = %q", got)
	}
	ue := &UnreachableError{Host: "box"}
	if ue.Error() != "host box is unreachable" || !errors.Is(ue, ErrUnreachable) {
		t.Errorf("UnreachableError = %q", ue.Error())
	}
	ac := &AlreadyControlledError{Name: "a"}
	if ac.Error() != "sandbox is controlled by a" || !errors.Is(ac, ErrAlreadyControlled) {
		t.Errorf("AlreadyControlledError = %q", ac.Error())
	}
}
