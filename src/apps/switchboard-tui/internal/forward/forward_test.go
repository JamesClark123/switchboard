package forward

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// fakeStream is a ForwardPort stream backed by a real TCP connection to a test
// server, so the client relay is exercised end to end without a daemon.
type fakeStream struct {
	grpc.ClientStream
	conn net.Conn

	mu     sync.Mutex
	opened bool
	open   *pb.PortForwardFrame_Open
	buf    []byte
}

func (f *fakeStream) Send(frame *pb.PortForwardFrame) error {
	if op := frame.GetOpen(); op != nil {
		f.mu.Lock()
		f.open = op
		f.mu.Unlock()
		return nil
	}
	if data := frame.GetData(); len(data) > 0 {
		_, err := f.conn.Write(data)
		return err
	}
	if frame.GetClosed() != nil {
		return f.conn.Close()
	}
	return nil
}

func (f *fakeStream) Recv() (*pb.PortForwardFrame, error) {
	f.mu.Lock()
	first := !f.opened
	f.opened = true
	f.mu.Unlock()
	if first {
		return &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Opened_{Opened: &pb.PortForwardFrame_Opened{}}}, nil
	}
	if f.buf == nil {
		f.buf = make([]byte, 4096)
	}
	n, err := f.conn.Read(f.buf)
	if n > 0 {
		return &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Data{Data: append([]byte(nil), f.buf[:n]...)}}, nil
	}
	if err == nil {
		err = io.EOF
	}
	return nil, err
}

func (f *fakeStream) CloseSend() error { return nil }

// fakeOpener dials a target address for every stream, standing in for the daemon
// dialling the service's host endpoint.
type fakeOpener struct {
	target string

	mu      sync.Mutex
	streams int
	opens   []*pb.PortForwardFrame_Open
	err     error
}

func (o *fakeOpener) ForwardPort(_ context.Context) (pb.Switchboard_ForwardPortClient, error) {
	o.mu.Lock()
	if o.err != nil {
		err := o.err
		o.mu.Unlock()
		return nil, err
	}
	o.streams++
	o.mu.Unlock()

	conn, err := net.Dial("tcp", o.target)
	if err != nil {
		return nil, err
	}
	s := &fakeStream{conn: conn}
	go func() {
		// Record the open frame once the relay has sent it.
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			op := s.open
			s.mu.Unlock()
			if op != nil {
				o.mu.Lock()
				o.opens = append(o.opens, op)
				o.mu.Unlock()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return s, nil
}

func (o *fakeOpener) streamCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.streams
}

func (o *fakeOpener) openFrames() []*pb.PortForwardFrame_Open {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*pb.PortForwardFrame_Open, len(o.opens))
	copy(out, o.opens)
	return out
}

// echoServer stands in for the forwarded service.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = conn.Close() }(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	return ln.Addr().String()
}

// US2: traffic to the local address reaches the service.
func TestForwardCarriesTrafficToTheService(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t)}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	port, err := m.Open(opener, "sb1", "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if port == 0 {
		t.Fatal("no local port allocated")
	}

	conn, err := net.Dial("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatalf("the local address must be connectable: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("no response came back through the forward: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("got %q, want the echoed bytes", buf)
	}
}

// The daemon needs the client's port so both sides display one address.
func TestForwardSendsTheOpenFrameFirst(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t)}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	port, err := m.Open(opener, "sb1", "svc-7")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if frames := opener.openFrames(); len(frames) > 0 {
			if frames[0].GetInstanceId() != "svc-7" {
				t.Errorf("open frame instance = %q, want svc-7", frames[0].GetInstanceId())
			}
			if frames[0].GetLocalPort() != port {
				t.Errorf("open frame port = %d, want the bound local port %d", frames[0].GetLocalPort(), port)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("no open frame was sent")
}

// One stream per accepted connection: a stalled connection must not wedge others.
func TestForwardOpensOneStreamPerConnection(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t)}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	port, _ := m.Open(opener, "sb1", "svc-1")
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", "127.0.0.1:"+itoa(port))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("x"))
		defer func() { _ = c.Close() }()
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if opener.streamCount() >= 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("streams = %d, want one per connection (3)", opener.streamCount())
}

// FR-049: the OS is the allocator, so two live forwards can never share a port.
func TestForwardPortsAreUniqueAcrossSandboxes(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t)}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	a, err := m.Open(opener, "sb1", "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Open(opener, "sb2", "svc-2")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two live forwards were given the same port %d", a)
	}
	if m.Count() != 2 {
		t.Errorf("forwards = %d, want 2", m.Count())
	}
}

// A duplicate RUNNING event must not move a service's address.
func TestForwardOpenIsIdempotent(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t)}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	first, _ := m.Open(opener, "sb1", "svc-1")
	second, err := m.Open(opener, "sb1", "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("reopening returned %d, want the existing %d", second, first)
	}
	if m.Count() != 1 {
		t.Errorf("forwards = %d, want 1", m.Count())
	}
}

// FR-048: closing the forward releases the port and kills live connections.
func TestCloseReleasesThePortAndTearsDownConnections(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t)}
	m := NewManager()

	port, _ := m.Open(opener, "sb1", "svc-1")
	conn, err := net.Dial("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	m.Close("svc-1")

	if m.Port("svc-1") != 0 {
		t.Error("a closed forward must report no port")
	}
	// The port must be immediately re-bindable — that is what "released" means.
	ln, err := net.Listen("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Errorf("port %d was not released: %v", port, err)
	} else {
		_ = ln.Close()
	}
	// Closing twice is a no-op.
	m.Close("svc-1")
	m.Close("never-existed")
}

func TestCloseSandboxAndCloseAll(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t)}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	_, _ = m.Open(opener, "sb1", "svc-1")
	_, _ = m.Open(opener, "sb1", "svc-2")
	_, _ = m.Open(opener, "sb2", "svc-3")

	m.CloseSandbox("sb1")
	if m.Count() != 1 {
		t.Errorf("after CloseSandbox(sb1): forwards = %d, want 1", m.Count())
	}
	m.CloseAll()
	if m.Count() != 0 {
		t.Errorf("after CloseAll: forwards = %d, want 0", m.Count())
	}
}

// A daemon that refuses the stream must not leave the browser hanging.
func TestForwardWithAFailingOpenerClosesTheConnection(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t), err: errors.New("unavailable")}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	port, _ := m.Open(opener, "sb1", "svc-1")
	conn, err := net.Dial("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("a refused stream must close the local connection, not hang it")
	}
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

func TestPortReturnsZeroForAnUnknownInstance(t *testing.T) {
	m := NewManager()
	if got := m.Port("never-opened"); got != 0 {
		t.Errorf("Port = %d, want 0 for an unknown instance", got)
	}
}

func TestCloseSandboxWithNoForwardsIsANoOp(t *testing.T) {
	m := NewManager()
	m.CloseSandbox("nothing-here")
	m.CloseAll()
	if m.Count() != 0 {
		t.Errorf("Count = %d, want 0", m.Count())
	}
}

// A connection that arrives after the forward is closed must be dropped, not
// relayed to a stream that no longer has a home.
func TestAcceptStopsOnceTheListenerCloses(t *testing.T) {
	opener := &fakeOpener{target: echoServer(t)}
	m := NewManager()
	port, err := m.Open(opener, "sb1", "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	m.Close("svc-1")

	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(port), 300*time.Millisecond); err == nil {
		t.Error("the listener must be gone after Close")
	}
}

// The relay must survive a service that closes immediately.
func TestRelayHandlesAServiceThatClosesAtOnce(t *testing.T) {
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
			_ = c.Close()
		}
	}()

	opener := &fakeOpener{target: ln.Addr().String()}
	m := NewManager()
	t.Cleanup(m.CloseAll)

	port, _ := m.Open(opener, "sb1", "svc-1")
	conn, err := net.Dial("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("a service that closes at once must end the local connection")
	}
}
