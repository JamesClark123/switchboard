package ui

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

func declaredWeb() *pb.KitService {
	return &pb.KitService{
		Name: "web", Command: "pnpm dev --host 0.0.0.0", ListenPort: 3000,
		Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX, IsWebsite: true,
	}
}

func declaredDB() *pb.KitService {
	return &pb.KitService{
		Name: "db", Command: "postgres", ListenPort: 5432,
		Location: pb.ServiceLocation_SERVICE_LOCATION_ON_HOST,
	}
}

func runningInst(name string, localPort uint32) *pb.ServiceInstance {
	return &pb.ServiceInstance{
		Id: "svc-1", SandboxId: "sb-1", ServiceName: name,
		State: pb.ServiceState_SERVICE_STATE_RUNNING, LocalPort: localPort,
	}
}

// servicesOn opens the services screen for the first sandbox with the given rows.
func servicesOn(t *testing.T, rows []*pb.SandboxService) (Model, *fakeDaemon) {
	t.Helper()
	d := &fakeDaemon{services: rows}
	m := listModel(t, d)
	out, _ := update(m, press("p"))
	if out.screen != screenServices {
		t.Fatalf("screen = %v, want screenServices — `p` must open the service list", out.screen)
	}
	out, _ = update(out, out.listServicesCmd(d, out.servicesView.sandboxID)())
	return out, d
}

// FR-045 / SC-005: opening the list starts nothing.
func TestServicesScreenListsDeclaredServicesAndStartsNothing(t *testing.T) {
	rows := []*pb.SandboxService{{Declared: declaredWeb()}, {Declared: declaredDB()}}
	out, d := servicesOn(t, rows)

	v := out.View()
	for _, want := range []string{"web", "db", "stopped"} {
		if !strings.Contains(v, want) {
			t.Errorf("view must show %q:\n%s", want, v)
		}
	}
	if len(d.startedServices) != 0 {
		t.Errorf("opening the list must start nothing, started %v", d.startedServices)
	}
}

func TestServicesScreenShowsStateAndLocation(t *testing.T) {
	rows := []*pb.SandboxService{
		{Declared: declaredWeb(), Instance: runningInst("web", 49221)},
		{Declared: declaredDB()},
	}
	out, _ := servicesOn(t, rows)
	v := out.View()

	if !strings.Contains(v, "running") {
		t.Errorf("a running service must say so:\n%s", v)
	}
	if !strings.Contains(v, "127.0.0.1:49221") {
		t.Errorf("a running service must show its address on THIS machine:\n%s", v)
	}
	if !strings.Contains(v, "in sandbox") || !strings.Contains(v, "on host") {
		t.Errorf("each service must show where it runs:\n%s", v)
	}
}

// A stopped or failed service must never leave a dead address on screen (FR-050).
func TestLocalAddressOnlyShownWhileRunning(t *testing.T) {
	if got := localAddress(runningInst("web", 49221)); got != "127.0.0.1:49221" {
		t.Errorf("running address = %q", got)
	}
	stopped := runningInst("web", 49221)
	stopped.State = pb.ServiceState_SERVICE_STATE_STOPPED
	if got := localAddress(stopped); got != "" {
		t.Errorf("a stopped service must show no address, got %q", got)
	}
	failed := runningInst("web", 49221)
	failed.State = pb.ServiceState_SERVICE_STATE_FAILED
	if got := localAddress(failed); got != "" {
		t.Errorf("a failed service must show no address, got %q", got)
	}
	noPort := runningInst("web", 0)
	if got := localAddress(noPort); got != "" {
		t.Errorf("a service with no forward yet must show no address, got %q", got)
	}
}

func TestServicesStartAndStopToggle(t *testing.T) {
	out, d := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb()}})

	// s on a stopped service starts it.
	out, cmd := update(out, press("s"))
	if cmd != nil {
		update(out, runCmd(cmd))
	}
	if len(d.startedServices) != 1 || !strings.HasSuffix(d.startedServices[0], "/web") {
		t.Fatalf("started = %v, want one start of web", d.startedServices)
	}

	// s on a running service stops it.
	running, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb(), Instance: runningInst("web", 49221)}})
	d2 := running.daemon.(*fakeDaemon)
	running, cmd = update(running, press("s"))
	if cmd != nil {
		update(running, runCmd(cmd))
	}
	if len(d2.stoppedServices) != 1 {
		t.Errorf("stopped = %v, want one stop", d2.stoppedServices)
	}
}

// US2-4: a website service offers the browser action.
func TestServicesOpenLaunchesTheBrowserForAWebsite(t *testing.T) {
	var launched []string
	prev := browserRunner
	browserRunner = func(c *exec.Cmd) error { launched = append(launched, strings.Join(c.Args, " ")); return nil }
	t.Cleanup(func() { browserRunner = prev })

	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb(), Instance: runningInst("web", 49221)}})
	out, _ = update(out, press("o"))

	if len(launched) != 1 {
		t.Fatalf("browser launches = %v, want 1", launched)
	}
	if !strings.Contains(launched[0], "http://127.0.0.1:49221") {
		t.Errorf("launched %q, want the local address", launched[0])
	}
	if !strings.Contains(out.servicesView.status, "opened") {
		t.Errorf("status = %q, want confirmation", out.servicesView.status)
	}
}

// US2-5: a non-website service shows a copyable address instead.
func TestServicesOpenShowsACopyableAddressForNonWebsites(t *testing.T) {
	var launched int
	prev := browserRunner
	browserRunner = func(*exec.Cmd) error { launched++; return nil }
	t.Cleanup(func() { browserRunner = prev })

	inst := runningInst("db", 49222)
	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredDB(), Instance: inst}})
	out, _ = update(out, press("o"))

	if launched != 0 {
		t.Error("a non-website service must not open a browser")
	}
	if !strings.Contains(out.servicesView.status, "127.0.0.1:49222") {
		t.Errorf("status = %q, want a copyable address", out.servicesView.status)
	}
}

func TestServicesOpenOnAStoppedServiceSaysSo(t *testing.T) {
	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb()}})
	out, _ = update(out, press("o"))
	if !strings.Contains(out.servicesView.status, "not running") {
		t.Errorf("status = %q, want 'not running'", out.servicesView.status)
	}
}

// FR-051: the failure reason and captured output are reachable from the client.
func TestServicesDetailShowsFailureReasonAndOutput(t *testing.T) {
	inst := &pb.ServiceInstance{
		Id: "svc-1", SandboxId: "sb-1", ServiceName: "web",
		State:           pb.ServiceState_SERVICE_STATE_FAILED,
		FailureReason:   pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING_LOOPBACK,
		FailureDetail:   "listening on 127.0.0.1:3000 inside the sandbox — bind all interfaces instead",
		Output:          "vite v5 ready\nlocal: http://127.0.0.1:3000",
		OutputTruncated: true,
	}
	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb(), Instance: inst}})
	out, _ = update(out, press("enter"))

	detail := out.servicesView.detail
	if !strings.Contains(detail, "bind all interfaces") {
		t.Errorf("detail must name the remedy:\n%s", detail)
	}
	if !strings.Contains(detail, "vite v5 ready") {
		t.Errorf("detail must include the captured output:\n%s", detail)
	}
	if !strings.Contains(detail, "truncated") {
		t.Errorf("truncation must be made evident:\n%s", detail)
	}
	if !strings.Contains(out.View(), "failed") {
		t.Error("the list must show the failed state")
	}
}

func TestServicesEscReturnsToTheList(t *testing.T) {
	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb()}})
	out, _ = update(out, press("esc"))
	if out.screen != screenList {
		t.Errorf("screen = %v, want screenList", out.screen)
	}
}

func TestIsTerminalServiceState(t *testing.T) {
	cases := map[pb.ServiceState]bool{
		pb.ServiceState_SERVICE_STATE_UNSPECIFIED: true,
		pb.ServiceState_SERVICE_STATE_STOPPED:     true,
		pb.ServiceState_SERVICE_STATE_FAILED:      true,
		pb.ServiceState_SERVICE_STATE_STARTING:    false,
		pb.ServiceState_SERVICE_STATE_RUNNING:     false,
	}
	for st, want := range cases {
		if got := IsTerminalServiceState(st); got != want {
			t.Errorf("IsTerminalServiceState(%v) = %v, want %v", st, got, want)
		}
	}
}

func TestServiceStateTextAndIconCoverEveryState(t *testing.T) {
	states := []pb.ServiceState{
		pb.ServiceState_SERVICE_STATE_STOPPED,
		pb.ServiceState_SERVICE_STATE_STARTING,
		pb.ServiceState_SERVICE_STATE_RUNNING,
		pb.ServiceState_SERVICE_STATE_FAILED,
	}
	seen := map[string]bool{}
	for _, st := range states {
		text := serviceStateText(st)
		if text == "" || seen[text] {
			t.Errorf("state %v has a missing or duplicate label %q", st, text)
		}
		seen[text] = true
		if serviceStateIcon(st) == "" {
			t.Errorf("state %v has no icon", st)
		}
	}
}

// --- error and edge paths -----------------------------------------------------

func TestServicesLoadErrorSurfaces(t *testing.T) {
	d := &fakeDaemon{listServicesErr: errors.New("daemon down")}
	m := listModel(t, d)
	out, _ := update(m, press("p"))

	msg := out.listServicesCmd(d, out.servicesView.sandboxID)()
	if _, ok := msg.(errMsg); !ok {
		t.Errorf("msg = %T, want errMsg when the daemon refuses", msg)
	}
}

func TestApplyServicesIgnoresAStaleLoad(t *testing.T) {
	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb()}})
	before := len(out.servicesView.list.Items())

	// A load for a different sandbox must not overwrite what is on screen.
	after, _ := out.applyServices(servicesLoadedMsg{sandboxID: "someone-else", services: nil})
	if got := len(after.(Model).servicesView.list.Items()); got != before {
		t.Errorf("items = %d, want the stale load ignored (%d)", got, before)
	}
}

func TestServicesStartErrorSurfaces(t *testing.T) {
	d := &fakeDaemon{services: []*pb.SandboxService{{Declared: declaredWeb()}}, serviceErr: errors.New("nope")}
	m := listModel(t, d)
	out, _ := update(m, press("p"))
	out, _ = update(out, out.listServicesCmd(d, out.servicesView.sandboxID)())

	_, cmd := update(out, press("s"))
	if cmd == nil {
		t.Fatal("start must produce a command")
	}
	if _, ok := cmd().(errMsg); !ok {
		t.Error("a failed start must surface as an error")
	}
}

func TestServicesRefreshMsgReloads(t *testing.T) {
	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb()}})

	_, cmd := update(out, servicesRefreshMsg{sandboxID: out.servicesView.sandboxID})
	if cmd == nil {
		t.Error("a refresh for the open sandbox must reload the list")
	}
	// A refresh for a different sandbox is ignored.
	if _, cmd := update(out, servicesRefreshMsg{sandboxID: "other"}); cmd != nil {
		t.Error("a refresh for another sandbox must be ignored")
	}
}

func TestServicesNavigationKeysReachTheList(t *testing.T) {
	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb()}, {Declared: declaredDB()}})
	out, _ = update(out, press("j"))
	row, ok := out.selectedService()
	if !ok || row.GetDeclared().GetName() != "db" {
		t.Errorf("j must move the selection, selected %v", row.GetDeclared().GetName())
	}
}

func TestServicesActionsOnAnEmptyList(t *testing.T) {
	out, _ := servicesOn(t, nil)
	if _, ok := out.selectedService(); ok {
		t.Error("an empty list has no selection")
	}
	// None of these may panic.
	out, _ = update(out, press("s"))
	out, _ = update(out, press("o"))
	_, _ = update(out, press("enter"))
}

func TestServicesOpenReportsABrowserFailure(t *testing.T) {
	prev := browserRunner
	browserRunner = func(*exec.Cmd) error { return errors.New("no browser") }
	t.Cleanup(func() { browserRunner = prev })

	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb(), Instance: runningInst("web", 49221)}})
	out, _ = update(out, press("o"))
	if !strings.Contains(out.servicesView.status, "could not open") {
		t.Errorf("status = %q, want the failure and the address", out.servicesView.status)
	}
	if !strings.Contains(out.servicesView.status, "49221") {
		t.Errorf("status = %q, must still give the developer the address", out.servicesView.status)
	}
}

func TestViewServicesRendersStatusAndDetail(t *testing.T) {
	out, _ := servicesOn(t, []*pb.SandboxService{{Declared: declaredWeb()}})
	out.servicesView.status = "a status line"
	out.servicesView.detail = "a detail block"
	v := out.viewServices()
	if !strings.Contains(v, "a status line") || !strings.Contains(v, "a detail block") {
		t.Errorf("view must render both status and detail:\n%s", v)
	}
}

func TestHandleServiceInstanceOpensAForwardWhenRunning(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	id := m.sandboxes[0].GetId()

	inst := &pb.ServiceInstance{Id: "svc-9", SandboxId: id, ServiceName: "web", State: pb.ServiceState_SERVICE_STATE_RUNNING}
	out, _ := m.handleServiceInstance(inst)
	mm := out.(Model)

	// fakeDaemon is not an Opener, so no listener is bound — the instance must still
	// be tracked rather than dropped.
	if mm.serviceInstances["svc-9"] == nil {
		t.Error("a running instance must be tracked even when no forward could open")
	}
}

func TestOpenerForFallsBackToTheActiveHost(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	// fakeDaemon does not implement forward.Opener, so this must be nil rather than
	// panicking on the type assertion.
	if got := m.openerFor(m.sandboxes[0].GetId()); got != nil {
		t.Errorf("openerFor = %v, want nil for a daemon that cannot forward", got)
	}
	if got := m.openerFor("unknown-sandbox"); got != nil {
		t.Errorf("openerFor(unknown) = %v, want nil", got)
	}
}

func TestInstanceOnHostMatching(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	sb := m.sandboxes[0]

	inst := &pb.ServiceInstance{Id: "svc-1", SandboxId: sb.GetId()}
	host := sb.GetHostId()
	if host == "" {
		host = m.activeHost
	}
	if !m.instanceOnHost(inst, host) {
		t.Error("an instance must match its own sandbox's host")
	}
	if m.instanceOnHost(inst, "elsewhere") {
		t.Error("an instance must not match a different host")
	}
	if m.instanceOnHost(&pb.ServiceInstance{SandboxId: "gone"}, host) {
		t.Error("an instance whose sandbox is unknown must not match")
	}
}

func TestReestablishForwardsSkipsNonRunning(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	rows := []*pb.SandboxService{
		{Declared: declaredWeb(), Instance: &pb.ServiceInstance{Id: "svc-1", State: pb.ServiceState_SERVICE_STATE_STOPPED}},
		{Declared: declaredDB()},
	}
	out := m.reestablishForwards(rows, m.sandboxes[0].GetId())
	if out.forwards.Count() != 0 {
		t.Errorf("forwards = %d, want none for stopped or unstarted services", out.forwards.Count())
	}
}

func TestServiceDetailWithoutAnInstance(t *testing.T) {
	detail := serviceDetail(&pb.SandboxService{Declared: declaredWeb()})
	if !strings.Contains(detail, "pnpm dev") || !strings.Contains(detail, "stopped") {
		t.Errorf("detail for an unstarted service must still describe it:\n%s", detail)
	}
}

func TestOpenBrowserPerPlatform(t *testing.T) {
	var got []string
	prev := browserRunner
	browserRunner = func(c *exec.Cmd) error { got = c.Args; return nil }
	t.Cleanup(func() { browserRunner = prev })

	if err := openBrowser("http://127.0.0.1:1234"); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !strings.Contains(strings.Join(got, " "), "127.0.0.1:1234") {
		t.Errorf("args = %v, want the URL handed to the platform opener", got)
	}
}

// --- forward integration (the client is the port allocator, research R1) -------

// forwardingDaemon is a fakeDaemon that can also open relay streams, so the model's
// forward bookkeeping can be exercised.
type forwardingDaemon struct {
	*fakeDaemon
	target string
}

func (f *forwardingDaemon) ForwardPort(ctx context.Context) (pb.Switchboard_ForwardPortClient, error) {
	conn, err := net.Dial("tcp", f.target)
	if err != nil {
		return nil, err
	}
	return &uiFakeStream{conn: conn}, nil
}

type uiFakeStream struct {
	grpc.ClientStream
	conn   net.Conn
	opened bool
	buf    []byte
}

func (s *uiFakeStream) Send(frame *pb.PortForwardFrame) error {
	if data := frame.GetData(); len(data) > 0 {
		_, err := s.conn.Write(data)
		return err
	}
	if frame.GetClosed() != nil {
		return s.conn.Close()
	}
	return nil
}

func (s *uiFakeStream) Recv() (*pb.PortForwardFrame, error) {
	if !s.opened {
		s.opened = true
		return &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Opened_{Opened: &pb.PortForwardFrame_Opened{}}}, nil
	}
	if s.buf == nil {
		s.buf = make([]byte, 4096)
	}
	n, err := s.conn.Read(s.buf)
	if n > 0 {
		return &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Data{Data: append([]byte(nil), s.buf[:n]...)}}, nil
	}
	if err == nil {
		err = io.EOF
	}
	return nil, err
}

func (s *uiFakeStream) CloseSend() error { return nil }

// uiEchoServer stands in for a forwarded service.
func uiEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

// US2 end to end through the model: a RUNNING event gets the service an address on
// this machine, and that address reaches the service.
func TestRunningEventOpensAWorkingLocalForward(t *testing.T) {
	d := &forwardingDaemon{fakeDaemon: &fakeDaemon{}, target: uiEchoServer(t)}
	m := listModel(t, d.fakeDaemon)
	m.daemon = d
	id := m.sandboxes[0].GetId()

	inst := &pb.ServiceInstance{Id: "svc-1", SandboxId: id, ServiceName: "web", State: pb.ServiceState_SERVICE_STATE_RUNNING}
	out, _ := m.handleServiceInstance(inst)
	mm := out.(Model)
	t.Cleanup(mm.forwards.CloseAll)

	port := mm.forwards.Port("svc-1")
	if port == 0 {
		t.Fatal("a running service must be given a port on this machine")
	}
	if got := mm.serviceInstances["svc-1"].GetLocalPort(); got != port {
		t.Errorf("tracked local port = %d, want the bound %d", got, port)
	}

	conn, err := net.Dial("tcp", "127.0.0.1:"+itoaU32(port))
	if err != nil {
		t.Fatalf("the address must be usable: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("hey")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("traffic must reach the service: %v", err)
	}
}

// US5: a reconnect re-establishes forwards for services the daemon still runs.
func TestReestablishForwardsAfterAReconnect(t *testing.T) {
	d := &forwardingDaemon{fakeDaemon: &fakeDaemon{}, target: uiEchoServer(t)}
	m := listModel(t, d.fakeDaemon)
	m.daemon = d
	id := m.sandboxes[0].GetId()

	rows := []*pb.SandboxService{{
		Declared: declaredWeb(),
		Instance: &pb.ServiceInstance{Id: "svc-5", SandboxId: id, ServiceName: "web", State: pb.ServiceState_SERVICE_STATE_RUNNING},
	}}
	out := m.reestablishForwards(rows, id)
	t.Cleanup(out.forwards.CloseAll)

	if out.forwards.Port("svc-5") == 0 {
		t.Error("a service the daemon still reports RUNNING must get a fresh listener")
	}
	// Re-running is idempotent — it must not stack listeners.
	before := out.forwards.Count()
	out = out.reestablishForwards(rows, id)
	if out.forwards.Count() != before {
		t.Errorf("forwards = %d, want no duplicates (%d)", out.forwards.Count(), before)
	}
}

func itoaU32(p uint32) string {
	const digits = "0123456789"
	if p == 0 {
		return "0"
	}
	var b []byte
	for p > 0 {
		b = append([]byte{digits[p%10]}, b...)
		p /= 10
	}
	return string(b)
}
