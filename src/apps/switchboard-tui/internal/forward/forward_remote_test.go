package forward

import (
	"io"
	"net"
	"testing"
	"time"
)

// A remote-host sandbox is not a special case in this package: the forward rides
// whatever connection the client already holds, so the only thing that differs is
// that the "service" is on the far side of a bridged transport. These tests use a
// stdio-style pipe pair to stand in for that bridge.

// bridgedService is an echo service reached through an intermediate hop, standing
// in for a service on a remote daemon host.
func bridgedService(t *testing.T) (addr string, breakLink func()) {
	t.Helper()
	backend := echoServer(t)

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			in, err := front.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = in.Close() }()
				out, err := net.Dial("tcp", backend)
				if err != nil {
					return
				}
				defer func() { _ = out.Close() }()
				go func() { _, _ = io.Copy(out, in) }()
				copyDone := make(chan struct{})
				go func() { _, _ = io.Copy(in, out); close(copyDone) }()
				select {
				case <-copyDone:
				case <-done:
				}
			}()
		}
	}()
	return front.Addr().String(), func() { close(done); _ = front.Close() }
}

// US5 / SC-008: the address is on THIS machine and reaches the remote service,
// with no extra steps compared to a local sandbox.
func TestRemoteHostServiceIsReachableAtALocalAddress(t *testing.T) {
	addr, breakLink := bridgedService(t)
	defer breakLink()

	opener := &fakeOpener{target: addr}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	port, err := m.Open(opener, "remote-sb", "svc-1")
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatalf("the address must be on the client machine: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("remote")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("traffic must reach the service on the remote host: %v", err)
	}
	if string(buf) != "remote" {
		t.Errorf("got %q, want the echoed bytes", buf)
	}
}

// US5-2: losing the host tears the forward down rather than leaving a dead address.
func TestClosingAForwardAfterTheHostGoesAway(t *testing.T) {
	addr, breakLink := bridgedService(t)

	opener := &fakeOpener{target: addr}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	port, err := m.Open(opener, "remote-sb", "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	breakLink() // the remote path is gone

	// The client tears down the forward on disconnect.
	m.CloseSandbox("remote-sb")
	if m.Port("svc-1") != 0 {
		t.Error("a lost host must release the local port, not keep a dead address")
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(port), 300*time.Millisecond); err == nil {
		t.Error("the local listener must be gone once the host is unreachable")
	}
}

// Reconnecting re-establishes a working forward, possibly on a different port —
// the service itself was never restarted (R1's documented trade-off).
func TestReopeningAfterAReconnectWorks(t *testing.T) {
	addr, breakLink := bridgedService(t)
	defer breakLink()

	m := NewManager()
	t.Cleanup(m.CloseAll)

	first, err := m.Open(&fakeOpener{target: addr}, "remote-sb", "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	m.CloseAll()

	second, err := m.Open(&fakeOpener{target: addr}, "remote-sb", "svc-1")
	if err != nil {
		t.Fatalf("a reconnect must be able to re-establish the forward: %v", err)
	}
	if second == 0 {
		t.Fatal("no port allocated on reconnect")
	}
	_ = first // it may or may not be reused; only "it works again" is guaranteed

	conn, err := net.Dial("tcp", "127.0.0.1:"+itoa(second))
	if err != nil {
		t.Fatalf("the re-established address must work: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("no response through the re-established forward: %v", err)
	}
}
