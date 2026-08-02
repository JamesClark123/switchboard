package portforward

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// fakeStream is an in-memory PortForwardStream: the test writes frames the daemon
// will Recv, and reads back the frames the daemon Sends. No gRPC server required.
type fakeStream struct {
	in  chan *pb.PortForwardFrame
	out chan *pb.PortForwardFrame

	mu     sync.Mutex
	closed bool
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		in:  make(chan *pb.PortForwardFrame, 64),
		out: make(chan *pb.PortForwardFrame, 64),
	}
}

func (f *fakeStream) Recv() (*pb.PortForwardFrame, error) {
	frame, ok := <-f.in
	if !ok {
		return nil, io.EOF
	}
	return frame, nil
}

func (f *fakeStream) Send(frame *pb.PortForwardFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("stream closed")
	}
	f.out <- frame
	return nil
}

func (f *fakeStream) closeIn() { close(f.in) }

// echoServer accepts one connection and echoes bytes back.
func echoServer(t *testing.T) (net.Listener, uint32) {
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
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln, uint32(ln.Addr().(*net.TCPAddr).Port)
}

// runningInstance registers a RUNNING instance pointing at endpoint.
func runningInstance(s *Supervisor, sandboxID, name string, endpoint uint32) *pb.ServiceInstance {
	inst, _ := s.insts.Create(sandboxID, name)
	updated, _ := s.insts.Update(inst.GetId(), func(i *pb.ServiceInstance) {
		i.State = pb.ServiceState_SERVICE_STATE_RUNNING
		i.HostEndpointPort = endpoint
	})
	return updated
}

func TestRelayCarriesBytesBothWays(t *testing.T) {
	_, endpoint := echoServer(t)
	s, _, _, _ := newTestSupervisor(t, runningSandbox("sb1"))
	inst := runningInstance(s, "sb1", "web", endpoint)

	stream := newFakeStream()
	done := make(chan error, 1)
	go func() { done <- s.Relay(stream) }()

	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: inst.GetId(), LocalPort: 49221},
	}}

	// The daemon confirms it dialled the service before any data flows.
	select {
	case frame := <-stream.out:
		if frame.GetOpened() == nil {
			t.Fatalf("first daemon frame = %v, want opened", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no opened frame")
	}

	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Data{Data: []byte("GET / HTTP/1.1\r\n")}}
	select {
	case frame := <-stream.out:
		if got := string(frame.GetData()); got != "GET / HTTP/1.1\r\n" {
			t.Errorf("echoed %q, want the bytes to reach the service and come back", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no data came back from the service")
	}

	stream.closeIn()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Relay returned %v, want a clean close", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Relay did not return after the client closed")
	}
}

// The daemon records the port the CLIENT bound, so both sides show one address.
func TestRelayRecordsTheClientsLocalPort(t *testing.T) {
	_, endpoint := echoServer(t)
	s, _, _, _ := newTestSupervisor(t, runningSandbox("sb1"))
	inst := runningInstance(s, "sb1", "web", endpoint)

	stream := newFakeStream()
	go func() { _ = s.Relay(stream) }()
	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: inst.GetId(), LocalPort: 49221},
	}}
	<-stream.out // opened

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cur, _ := s.insts.Get(inst.GetId()); cur.GetLocalPort() == 49221 {
			stream.closeIn()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the daemon must record the developer-machine port from the open frame")
}

func TestRelayRejectsANonOpenFirstFrame(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t, runningSandbox("sb1"))
	stream := newFakeStream()
	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Data{Data: []byte("hi")}}

	if err := s.Relay(stream); !errors.Is(err, ErrRelayFirstFrame) {
		t.Errorf("err = %v, want ErrRelayFirstFrame", err)
	}
}

// FR-050: a stale client must not be able to make a dead address look alive.
func TestRelayRefusesAnInstanceThatIsNotRunning(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t, runningSandbox("sb1"))
	inst, _ := s.insts.Create("sb1", "web") // STARTING, not RUNNING

	stream := newFakeStream()
	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: inst.GetId()},
	}}
	if err := s.Relay(stream); !errors.Is(err, ErrRelayNotRunning) {
		t.Errorf("err = %v, want ErrRelayNotRunning", err)
	}

	// A stopped service is refused for the same reason.
	s.insts.Update(inst.GetId(), func(i *pb.ServiceInstance) {
		i.State = pb.ServiceState_SERVICE_STATE_STOPPED
		i.HostEndpointPort = 1
	})
	stream2 := newFakeStream()
	stream2.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: inst.GetId()},
	}}
	if err := s.Relay(stream2); !errors.Is(err, ErrRelayNotRunning) {
		t.Errorf("stopped service: err = %v, want ErrRelayNotRunning", err)
	}
}

func TestRelayRefusesAnUnknownInstance(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t, runningSandbox("sb1"))
	stream := newFakeStream()
	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: "svc-999"},
	}}
	if err := s.Relay(stream); !errors.Is(err, ErrRelayNotRunning) {
		t.Errorf("err = %v, want ErrRelayNotRunning", err)
	}
}

func TestRelayFailsWhenTheServiceEndpointIsDead(t *testing.T) {
	ln, endpoint := listenLoopback(t)
	_ = ln.Close() // nothing to dial

	s, _, _, _ := newTestSupervisor(t, runningSandbox("sb1"))
	inst := runningInstance(s, "sb1", "web", endpoint)

	stream := newFakeStream()
	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: inst.GetId()},
	}}
	if err := s.Relay(stream); err == nil {
		t.Error("a dial failure must surface, not hang the client on a socket to nowhere")
	}
}

// A closed frame from the client ends the relay without an error.
func TestRelayHonoursTheClientHalfClose(t *testing.T) {
	_, endpoint := echoServer(t)
	s, _, _, _ := newTestSupervisor(t, runningSandbox("sb1"))
	inst := runningInstance(s, "sb1", "web", endpoint)

	stream := newFakeStream()
	done := make(chan error, 1)
	go func() { done <- s.Relay(stream) }()

	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Open_{
		Open: &pb.PortForwardFrame_Open{InstanceId: inst.GetId()},
	}}
	<-stream.out
	stream.in <- &pb.PortForwardFrame{Frame: &pb.PortForwardFrame_Closed_{Closed: &pb.PortForwardFrame_Closed{}}}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a half-close must end the relay cleanly, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Relay did not return after the closed frame")
	}
}
