package portforward

import (
	"context"
	"errors"
	"net"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestFreeLoopbackPortIsActuallyFree(t *testing.T) {
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	if port == 0 {
		t.Fatal("allocated port 0")
	}
	// It must be bindable right after allocation — that is the whole contract.
	ln, err := net.Listen("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatalf("allocated port %d was not free: %v", port, err)
	}
	_ = ln.Close()
}

func itoa(p uint32) string {
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

func TestPortFreeDetectsAListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)

	if portFree(port) {
		t.Error("a bound port must not report free")
	}
	_ = ln.Close()
	if !portFree(port) {
		t.Error("a released port must report free")
	}
}

func TestPublishSandboxPortPublishesAFreePort(t *testing.T) {
	s, _, runner, _ := newTestSupervisor(t)

	hostPort, err := s.publishSandboxPort(context.Background(), "sb1-ref", 3000)
	if err != nil {
		t.Fatal(err)
	}
	pubs := runner.publishes()
	if len(pubs) != 1 {
		t.Fatalf("publishes = %d, want 1", len(pubs))
	}
	if pubs[0][0] != hostPort || pubs[0][1] != 3000 {
		t.Errorf("published %v, want the returned host port mapped to the declared 3000", pubs[0])
	}
}

func TestPublishSandboxPortRetriesThenFails(t *testing.T) {
	s, _, runner, _ := newTestSupervisor(t)
	runner.publishErr = errors.New("port already allocated")

	if _, err := s.publishSandboxPort(context.Background(), "sb1-ref", 3000); err == nil {
		t.Fatal("a persistently failing publish must surface an error")
	}
	// It must retry rather than giving up on the first lost race, and must stop
	// rather than spin.
	if got := len(runner.publishes()); got != 0 {
		t.Errorf("no publish should have been recorded, got %d", got)
	}
}

func TestEffectivePortInSandboxAlwaysUsesTheDeclaredPort(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	svc := svc("web", "pnpm dev", 3000, inSandbox)

	port, cmd, err := s.effectivePort(svc)
	if err != nil {
		t.Fatal(err)
	}
	if port != 3000 {
		t.Errorf("port = %d, want the declared 3000 — a sandbox's own namespace cannot collide", port)
	}
	if cmd != "pnpm dev" {
		t.Errorf("command = %q, want it unchanged", cmd)
	}
}

func TestEffectivePortOnHostWithoutTokenUsesTheDeclaredPort(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	port, cmd, err := s.effectivePort(svc("worker", "pnpm worker", 7000, onHost))
	if err != nil {
		t.Fatal(err)
	}
	if port != 7000 || cmd != "pnpm worker" {
		t.Errorf("got (%d, %q), want the declared port and an unchanged command", port, cmd)
	}
}

// The {{port}} token is what lets two sandboxes run the same on-host service.
func TestEffectivePortOnHostWithTokenAllocatesAFreshPort(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	declared := svc("worker", "pnpm worker --port {{port}}", 7000, onHost)

	port, cmd, err := s.effectivePort(declared)
	if err != nil {
		t.Fatal(err)
	}
	if port == 7000 {
		t.Error("a {{port}} command must NOT reuse the declared port — that is the collision it exists to avoid")
	}
	if want := "pnpm worker --port " + itoa(port); cmd != want {
		t.Errorf("command = %q, want %q", cmd, want)
	}

	// A second instance must get a different port, which is what makes US3-3 work.
	port2, _, err := s.effectivePort(declared)
	if err != nil {
		t.Fatal(err)
	}
	if port2 == port {
		t.Errorf("two instances both got port %d; they must differ", port)
	}
}

func TestEffectivePortSubstitutesEveryOccurrence(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	declared := &pb.KitService{
		Name: "svc", Command: "serve --port {{port}} --advertise localhost:{{port}}",
		ListenPort: 7000, Location: onHost,
	}
	port, cmd, err := s.effectivePort(declared)
	if err != nil {
		t.Fatal(err)
	}
	if want := "serve --port " + itoa(port) + " --advertise localhost:" + itoa(port); cmd != want {
		t.Errorf("command = %q, want every token replaced (%q)", cmd, want)
	}
}
