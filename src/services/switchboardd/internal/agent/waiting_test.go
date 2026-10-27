package agent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeTimer is a backstop timer fired by hand.
type fakeTimer struct {
	f       func()
	d       time.Duration
	stopped bool
}

func (t *fakeTimer) Stop() bool { was := !t.stopped; t.stopped = true; return was }

// waitingServer is a HookServer over a readable status side whose current
// status follows what the server sets, with a hand-fired backstop.
type waitingServer struct {
	*HookServer
	st     *trackingStatus
	timers []*fakeTimer
}

// trackingStatus is a readable status side that records the status it was last
// set to and reports it back, for a sandbox that is running unless stopped, and
// present unless gone.
type trackingStatus struct {
	readableStatus
	stopped, gone bool
}

func (f *trackingStatus) Get(id string) (*pb.Sandbox, error) {
	if f.gone {
		return nil, errors.New("not found")
	}
	sb, _ := f.readableStatus.Get(id)
	sb.State = pb.SandboxState_SANDBOX_STATE_RUNNING
	if f.stopped {
		sb.State = pb.SandboxState_SANDBOX_STATE_STOPPED
	}
	return sb, nil
}

func (f *trackingStatus) SetAgentStatus(id string, s pb.AgentStatus, now time.Time) (*pb.Sandbox, error) {
	f.current = s
	return f.fakeStatus.SetAgentStatus(id, s, now)
}

func newWaitingServer(controller *pb.SandboxController) *waitingServer {
	w := &waitingServer{st: &trackingStatus{readableStatus: readableStatus{fakeStatus: fakeStatus{controller: controller}}}}
	w.HookServer = NewHookServer(NewHub("h"), w.st)
	w.afterFunc = func(d time.Duration, f func()) stopper {
		t := &fakeTimer{f: f, d: d}
		w.timers = append(w.timers, t)
		return t
	}
	return w
}

func (w *waitingServer) kinds() []pb.NotificationKind {
	var out []pb.NotificationKind
	for _, n := range w.hub.Undelivered() {
		out = append(out, n.GetKind())
	}
	return out
}

func TestPendingBackground(t *testing.T) {
	tasks := []backgroundTask{
		{ID: "a", Type: "shell", Status: "running"},
		{ID: "b", Type: "subagent", Status: "pending"},
		{ID: "c", Type: "monitor", Status: ""},
		{ID: "d", Type: "shell", Status: "completed"},
		{ID: "e", Type: "shell", Status: "killed"},
	}
	if got := pendingBackground(tasks); got != 3 {
		t.Errorf("pendingBackground = %d, want 3 (only finished statuses release the agent)", got)
	}
	if got := pendingBackground(nil); got != 0 {
		t.Errorf("pendingBackground(nil) = %d, want 0", got)
	}
}

// A turn that ends while background work runs is a pause: WAITING, no
// task-complete notification; the wake-up (UserPromptSubmit) is WORKING and the
// final Stop with nothing pending is the real end of the task (SWI-12).
func TestStopWithBackgroundWorkWaits(t *testing.T) {
	w := newWaitingServer(nil)
	w.dispatch(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb1"})
	w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1", PendingBackground: 1})
	if w.st.current != pb.AgentStatus_AGENT_STATUS_WAITING {
		t.Fatalf("status after a Stop with background work = %v, want WAITING", w.st.current)
	}
	if k := w.kinds(); len(k) != 0 {
		t.Fatalf("a waiting Stop must not notify, got %v", k)
	}
	if len(w.timers) != 1 || w.timers[0].stopped {
		t.Fatalf("expected one armed backstop, got %+v", w.timers)
	}

	// Claude Code's idle nudge while waiting keeps the agent WAITING, silently.
	w.dispatch(hookPayload{Event: "Notification", SandboxID: "sb1", NotificationType: notifyIdle})
	// So does a legacy (untyped) notification, which can only be that nudge.
	w.dispatch(hookPayload{Event: "Notification", SandboxID: "sb1"})
	if w.st.current != pb.AgentStatus_AGENT_STATUS_WAITING || len(w.kinds()) != 0 || w.timers[0].stopped {
		t.Fatalf("idle nudge while waiting: status %v, notifications %v, backstop stopped %v", w.st.current, w.kinds(), w.timers[0].stopped)
	}

	// The background work finishes and wakes the agent.
	w.dispatch(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb1"})
	if w.st.current != pb.AgentStatus_AGENT_STATUS_WORKING || !w.timers[0].stopped {
		t.Fatalf("wake-up: status %v, backstop stopped %v; want WORKING, stopped", w.st.current, w.timers[0].stopped)
	}
	w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1"})
	if w.st.current != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Fatalf("final Stop: status %v, want IDLE", w.st.current)
	}
	if k := w.kinds(); len(k) != 1 || k[0] != pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE {
		t.Fatalf("final Stop should notify task complete once, got %v", k)
	}
}

// A wait that outlives backgroundWaitMax (a background server that never ends)
// is taken as the end of the task.
func TestBackstopEndsAStuckWait(t *testing.T) {
	w := newWaitingServer(nil)
	w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1", PendingBackground: 2})
	w.timers[0].f()
	if w.st.current != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Fatalf("backstop: status %v, want IDLE", w.st.current)
	}
	if k := w.kinds(); len(k) != 1 || k[0] != pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE {
		t.Fatalf("backstop should notify task complete, got %v", k)
	}
	// It says the wait was given up, not that the work finished.
	if msg := w.hub.Undelivered()[0].GetMessage(); !strings.Contains(msg, "stopped waiting on background work after 30m") || strings.Contains(msg, "task complete") {
		t.Errorf("backstop message = %q", msg)
	}
}

// A background subagent's own tool calls fire PreToolUse (and PostToolUse)
// with the sandbox's id while the main turn waits on it: the wait and its
// backstop go on. Outside a wait they still mean WORKING.
func TestToolHooksDoNotEndAWait(t *testing.T) {
	w := newWaitingServer(nil)
	w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1", PendingBackground: 1})
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		w.dispatch(hookPayload{Event: ev, SandboxID: "sb1"})
		if w.st.current != pb.AgentStatus_AGENT_STATUS_WAITING || w.timers[0].stopped {
			t.Fatalf("%s while waiting: status %v, backstop stopped %v; want WAITING, armed", ev, w.st.current, w.timers[0].stopped)
		}
	}
	w.dispatch(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb1"}) // the wake-up
	w.dispatch(hookPayload{Event: "PreToolUse", SandboxID: "sb1"})
	if w.st.current != pb.AgentStatus_AGENT_STATUS_WORKING || !w.timers[0].stopped {
		t.Fatalf("after the wake-up: status %v, backstop stopped %v; want WORKING, disarmed", w.st.current, w.timers[0].stopped)
	}
}

// A fired backstop does nothing once the agent moved on, or once a newer wait
// re-armed it.
func TestBackstopIgnoredWhenStale(t *testing.T) {
	w := newWaitingServer(nil)
	w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1", PendingBackground: 1})
	w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1", PendingBackground: 1}) // re-arms
	if len(w.timers) != 2 || !w.timers[0].stopped {
		t.Fatalf("a second waiting Stop should replace the backstop, got %+v", w.timers)
	}
	w.timers[0].f() // the replaced timer fires anyway (a race with Stop)
	if w.st.current != pb.AgentStatus_AGENT_STATUS_WAITING || len(w.kinds()) != 0 {
		t.Fatalf("stale backstop acted: status %v, notifications %v", w.st.current, w.kinds())
	}

	// The agent is parked on a permission prompt when the live timer fires:
	// the backstop only ever ends a wait.
	w.st.current = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
	w.timers[1].f()
	if w.st.current != pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT || len(w.kinds()) != 0 {
		t.Fatalf("backstop on a non-waiting agent acted: status %v, notifications %v", w.st.current, w.kinds())
	}
}

// Every way out of WAITING cancels the backstop. (The idle nudge and tool
// hooks are not ways out: TestStopWithBackgroundWorkWaits, TestToolHooksDoNotEndAWait.)
func TestBackstopDisarmedWhenTheAgentMovesOn(t *testing.T) {
	for _, p := range []hookPayload{
		{Event: "UserPromptSubmit"},
		{Event: "Stop"},
		{Event: "Notification", NotificationType: "permission_prompt"},
	} {
		w := newWaitingServer(nil)
		w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1", PendingBackground: 1})
		p.SandboxID = "sb1"
		w.dispatch(p)
		if !w.timers[0].stopped {
			t.Errorf("%s/%s should disarm the backstop", p.Event, p.NotificationType)
		}
	}
}

// The forwarded Stop input carries background_tasks (captured from Claude Code
// 2.1.311); the handler turns it into the pending count dispatch acts on. An
// empty list, or the legacy body-less Stop, is the end of the task as before.
func TestHookHTTPStopBackgroundTasks(t *testing.T) {
	w := newWaitingServer(nil)
	srv := httptest.NewServer(http.HandlerFunc(w.Handle))
	defer srv.Close()
	post := func(body string) {
		t.Helper()
		resp, err := http.Post(srv.URL+"?event=Stop&sandbox_id=sb1", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
	post(`{"hook_event_name":"Stop","stop_hook_active":false,"background_tasks":[{"id":"btw765n4o","type":"shell","status":"running","description":"Sleep 25 seconds then print done","command":"sleep 25 && echo done"}],"session_crons":[]}`)
	if w.st.current != pb.AgentStatus_AGENT_STATUS_WAITING || len(w.kinds()) != 0 {
		t.Fatalf("Stop with a running task: status %v, notifications %v", w.st.current, w.kinds())
	}
	post(`{"hook_event_name":"Stop","stop_hook_active":false,"background_tasks":[],"session_crons":[]}`)
	if w.st.current != pb.AgentStatus_AGENT_STATUS_IDLE || len(w.kinds()) != 1 {
		t.Fatalf("Stop with nothing pending: status %v, notifications %v", w.st.current, w.kinds())
	}
	post("")
	if w.st.current != pb.AgentStatus_AGENT_STATUS_IDLE || len(w.kinds()) != 2 {
		t.Fatalf("legacy Stop: status %v, notifications %v", w.st.current, w.kinds())
	}
}

// A controlled worker's idle nudge after a real stop is still silent IDLE
// (unchanged), and the nudge after a wait does not reach the developer either.
func TestIdleNudgeOnWaitingWorker(t *testing.T) {
	w := newWaitingServer(&pb.SandboxController{OrchestratorId: "o"})
	w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1", PendingBackground: 1})
	w.dispatch(hookPayload{Event: "Notification", SandboxID: "sb1", NotificationType: notifyIdle})
	if w.st.current != pb.AgentStatus_AGENT_STATUS_WAITING || len(w.kinds()) != 0 {
		t.Fatalf("status %v, notifications %v", w.st.current, w.kinds())
	}
}

// The real timer source is wired by default and backs a working backstop.
func TestDefaultAfterFunc(t *testing.T) {
	hs := NewHookServer(NewHub("h"), &fakeStatus{})
	fired := make(chan struct{})
	tm := hs.afterFunc(time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("default afterFunc never fired")
	}
	tm.Stop()
}

// The backstop never touches a sandbox that is gone or stopped by the time it
// fires: no status write, no notification.
func TestBackstopSkipsGoneOrStoppedSandbox(t *testing.T) {
	for _, c := range []struct {
		name          string
		stopped, gone bool
	}{{"gone", false, true}, {"stopped", true, false}} {
		t.Run(c.name, func(t *testing.T) {
			w := newWaitingServer(nil)
			w.dispatch(hookPayload{Event: "Stop", SandboxID: "sb1", PendingBackground: 1})
			calls := w.st.calls
			w.st.stopped, w.st.gone = c.stopped, c.gone
			w.timers[0].f()
			if w.st.calls != calls || len(w.kinds()) != 0 {
				t.Errorf("backstop acted: calls %d→%d, notifications %v", calls, w.st.calls, w.kinds())
			}
		})
	}
}

// After a daemon restart the backstop is restored for agents recorded WAITING,
// with what is left of backgroundWaitMax since their last hook event (none
// left fires at once); other agents get none.
func TestRearmWaitingOnStart(t *testing.T) {
	w := newWaitingServer(nil)
	now := time.Unix(10_000, 0)
	w.now = func() time.Time { return now }
	at := func(ago time.Duration) *pb.AgentSession {
		return &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_WAITING, LastEventAt: timestamppb.New(now.Add(-ago))}
	}
	running := pb.SandboxState_SANDBOX_STATE_RUNNING
	w.RearmWaiting([]*pb.Sandbox{
		{Id: "recent", State: running, Agent: at(10 * time.Minute)},
		{Id: "overdue", State: running, Agent: at(2 * backgroundWaitMax)},
		{Id: "no-time", State: running, Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_WAITING}},
		{Id: "idle", State: running, Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE}},
		// Stopped while waiting by a daemon that did not yet reset the status:
		// no agent is left to wait, so it is set IDLE rather than armed.
		{Id: "stopped", State: pb.SandboxState_SANDBOX_STATE_STOPPED, Agent: at(time.Minute)},
	})
	if len(w.timers) != 3 {
		t.Fatalf("armed %d backstops, want 3", len(w.timers))
	}
	if w.st.current != pb.AgentStatus_AGENT_STATUS_IDLE || w.st.lastID != "stopped" {
		t.Fatalf("stopped waiting sandbox: status %v on %q, want IDLE on stopped", w.st.current, w.st.lastID)
	}
	for i, want := range []time.Duration{backgroundWaitMax - 10*time.Minute, 0, backgroundWaitMax} {
		if w.timers[i].d != want {
			t.Errorf("timer %d = %v, want %v", i, w.timers[i].d, want)
		}
	}
	w.st.current = pb.AgentStatus_AGENT_STATUS_WAITING
	w.timers[1].f()
	if w.st.current != pb.AgentStatus_AGENT_STATUS_IDLE || w.st.lastID != "overdue" {
		t.Errorf("overdue wait: status %v on %q, want IDLE on overdue", w.st.current, w.st.lastID)
	}
}
