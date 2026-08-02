package ui

import (
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// serviceEvent wraps an instance update in the same eventMsg the subscription
// pump delivers, so these tests exercise the real routing.
func serviceEvent(inst *pb.ServiceInstance) eventMsg {
	return eventMsg{ev: &pb.Event{Event: &pb.Event_ServiceInstance{ServiceInstance: inst}}}
}

// instFor builds an instance whose id is unique per (sandbox, service), matching
// the daemon's svc-<seq> ids — two sandboxes running the same service name are two
// distinct instances.
func instFor(sandboxID, name string, st pb.ServiceState) *pb.ServiceInstance {
	return &pb.ServiceInstance{Id: "svc-" + sandboxID + "-" + name, SandboxId: sandboxID, ServiceName: name, State: st}
}

// FR-052: every transition updates the model; only a failure notifies.
func TestServiceEventsUpdateTheModelAndOnlyFailuresNotify(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	id := m.sandboxes[0].GetId()

	// A start the developer just performed: reflected, but silent.
	out, _ := update(m, serviceEvent(instFor(id, "web", pb.ServiceState_SERVICE_STATE_STARTING)))
	if len(out.inbox) != 0 {
		t.Errorf("a STARTING transition must not notify, inbox = %d", len(out.inbox))
	}
	if len(out.serviceInstances) != 1 {
		t.Errorf("the instance must be tracked, got %d", len(out.serviceInstances))
	}

	// A developer-initiated stop: also silent.
	out, _ = update(out, serviceEvent(instFor(id, "web", pb.ServiceState_SERVICE_STATE_STOPPED)))
	if len(out.inbox) != 0 {
		t.Errorf("a STOPPED transition must not notify, inbox = %d", len(out.inbox))
	}
}

// The notification itself comes from the daemon as a NotificationEvent; the client
// must give it a title that says what happened.
func TestServiceFailedNotificationTitle(t *testing.T) {
	if got := notifTitle(pb.NotificationKind_NOTIFICATION_KIND_SERVICE_FAILED); got != "service failed" {
		t.Errorf("notifTitle = %q, want %q", got, "service failed")
	}
}

func TestServiceFailureLandsInTheInbox(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	id := m.sandboxes[0].GetId()

	ev := eventMsg{ev: &pb.Event{Event: &pb.Event_Notification{Notification: &pb.NotificationEvent{
		Id: "n1", SandboxId: id, Kind: pb.NotificationKind_NOTIFICATION_KIND_SERVICE_FAILED,
		Message: `service "web" exited unexpectedly: exit status 1`,
	}}}}
	out, _ := update(m, ev)

	if len(out.inbox) != 1 {
		t.Fatalf("inbox = %d, want the failure", len(out.inbox))
	}
	if !strings.Contains(out.inbox[0].GetMessage(), "web") {
		t.Errorf("the inbox entry must name the service: %q", out.inbox[0].GetMessage())
	}
}

// FR-045: the sandbox list is the only cross-sandbox surface — a running count.
func TestSandboxListShowsARunningServicesIndicator(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	id := m.sandboxes[0].GetId()

	if badge := m.runningServicesBadge(id); badge != "" {
		t.Errorf("no services running, badge should be empty, got %q", badge)
	}

	out, _ := update(m, serviceEvent(instFor(id, "web", pb.ServiceState_SERVICE_STATE_RUNNING)))
	out, _ = update(out, serviceEvent(instFor(id, "api", pb.ServiceState_SERVICE_STATE_RUNNING)))

	badge := out.runningServicesBadge(id)
	if !strings.Contains(badge, "2") {
		t.Errorf("badge = %q, want a count of 2", badge)
	}
	if !strings.Contains(out.View(), "⇄") {
		t.Errorf("the sandbox row must carry the indicator:\n%s", out.View())
	}

	// Only RUNNING counts — a failed service is not "up".
	out, _ = update(out, serviceEvent(instFor(id, "api", pb.ServiceState_SERVICE_STATE_FAILED)))
	if badge := out.runningServicesBadge(id); !strings.Contains(badge, "1") {
		t.Errorf("badge = %q, want the count to drop to 1", badge)
	}

	// A different sandbox's services must not be counted here.
	out, _ = update(out, serviceEvent(instFor("other-sandbox", "web", pb.ServiceState_SERVICE_STATE_RUNNING)))
	if badge := out.runningServicesBadge(id); !strings.Contains(badge, "1") {
		t.Errorf("badge = %q, want another sandbox's service excluded", badge)
	}
}

// US5-2 / FR-050: when a service leaves RUNNING its forward is torn down, so no
// dead address survives on screen.
func TestLeavingRunningClosesTheForward(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	id := m.sandboxes[0].GetId()

	running := instFor(id, "web", pb.ServiceState_SERVICE_STATE_RUNNING)
	out, _ := update(m, serviceEvent(running))
	// A forward may or may not have opened (it needs a live opener), but the
	// bookkeeping must be consistent either way.
	tracked := out.serviceInstances[running.GetId()]
	if tracked == nil {
		t.Fatal("the running instance must be tracked")
	}

	failed := instFor(id, "web", pb.ServiceState_SERVICE_STATE_FAILED)
	out, _ = update(out, serviceEvent(failed))
	if out.forwards.Port(failed.GetId()) != 0 {
		t.Error("leaving RUNNING must close the forward and release the local port")
	}
	if out.serviceInstances[failed.GetId()].GetState() != pb.ServiceState_SERVICE_STATE_FAILED {
		t.Error("the tracked instance must reflect the new state")
	}
}

// US5-2: a lost host must show its services as unreachable, not as working.
func TestHostDisconnectMarksServicesUnreachable(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	sb := m.sandboxes[0]
	id := sb.GetId()

	out, _ := update(m, serviceEvent(instFor(id, "web", pb.ServiceState_SERVICE_STATE_RUNNING)))
	instID := "svc-" + id + "-web"
	if out.serviceInstances[instID].GetState() != pb.ServiceState_SERVICE_STATE_RUNNING {
		t.Fatal("precondition: the service must be running")
	}

	out = out.hostDisconnected(hostOf(sb, out.activeHost))

	got := out.serviceInstances[instID]
	if got.GetState() != pb.ServiceState_SERVICE_STATE_FAILED {
		t.Errorf("state = %v, want the service shown as unreachable", got.GetState())
	}
	if got.GetFailureReason() != pb.ServiceFailureReason_SERVICE_FAILURE_REASON_HOST_UNREACHABLE {
		t.Errorf("reason = %v, want HOST_UNREACHABLE", got.GetFailureReason())
	}
	if got.GetLocalPort() != 0 {
		t.Error("an unreachable service must not keep showing a local address")
	}
	// The service itself is untouched on the far side; the message must say so.
	if !strings.Contains(got.GetFailureDetail(), "still running") {
		t.Errorf("detail = %q, want it to say the service is still running on its host", got.GetFailureDetail())
	}
	if out.runningServicesBadge(id) != "" {
		t.Error("an unreachable service must not be counted as running")
	}
}

// A disconnect of a DIFFERENT host must leave this sandbox's services alone.
func TestHostDisconnectLeavesOtherHostsAlone(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	id := m.sandboxes[0].GetId()

	out, _ := update(m, serviceEvent(instFor(id, "web", pb.ServiceState_SERVICE_STATE_RUNNING)))
	out = out.hostDisconnected("some-other-host")

	if out.serviceInstances["svc-"+id+"-web"].GetState() != pb.ServiceState_SERVICE_STATE_RUNNING {
		t.Error("another host's disconnect must not touch this sandbox's services")
	}
}

func hostOf(sb *pb.Sandbox, fallback string) string {
	if sb.GetHostId() != "" {
		return sb.GetHostId()
	}
	return fallback
}
