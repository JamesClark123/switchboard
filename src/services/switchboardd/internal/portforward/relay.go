package portforward

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// relayChunk is the read size for both directions of the byte relay. It matches the
// existing stream reader sizing in this repo; the traffic is dev-server responses,
// not bulk transfer.
const relayChunk = 32 * 1024

// ErrRelayNotRunning is returned when a forward is requested for an instance that
// is not RUNNING. This is what stops a stale client from presenting a dead address
// as working (FR-050).
var ErrRelayNotRunning = errors.New("service is not running")

// ErrRelayFirstFrame is returned when a client's first frame is not `open`.
var ErrRelayFirstFrame = errors.New("first frame must be open")

// PortForwardStream is the transport-agnostic view of the bidirectional gRPC
// stream, so the relay can be tested without a server.
type PortForwardStream interface {
	Recv() (*pb.PortForwardFrame, error)
	Send(*pb.PortForwardFrame) error
}

// Relay services one ForwardPort stream: one accepted TCP connection on the
// developer's machine, carried over the connection the client already holds
// (research R1).
//
// The client is the sole allocator of ports on the developer's side; the daemon
// only records the port it was told about, so the address the developer sees and
// the address the daemon reports can never disagree.
func (s *Supervisor) Relay(stream PortForwardStream) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return ErrRelayFirstFrame
	}

	inst, ok := s.insts.Get(open.GetInstanceId())
	if !ok {
		return ErrRelayNotRunning
	}
	if inst.GetState() != pb.ServiceState_SERVICE_STATE_RUNNING {
		return ErrRelayNotRunning
	}
	endpoint := inst.GetHostEndpointPort()
	if endpoint == 0 {
		return ErrRelayNotRunning
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", endpoint), dialTimeout)
	if err != nil {
		return fmt.Errorf("dial service endpoint: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Record the developer-machine port so the client's service list can display it
	// and the daemon's view stays in step (FR-050).
	if port := open.GetLocalPort(); port != 0 && inst.GetLocalPort() != port {
		s.transition(inst.GetId(), func(i *pb.ServiceInstance) { i.LocalPort = port })
	}

	if err := stream.Send(&pb.PortForwardFrame{
		Frame: &pb.PortForwardFrame_Opened_{Opened: &pb.PortForwardFrame_Opened{}},
	}); err != nil {
		return err
	}

	return pipe(stream, conn)
}

// pipe copies bytes both ways until either side closes.
//
// Both directions must be able to end the relay: a browser closing a tab ends the
// client side, and a dev server restarting ends the service side. Whichever
// finishes first closes the socket, which unblocks the other.
func pipe(stream PortForwardStream, conn net.Conn) error {
	var wg sync.WaitGroup
	wg.Add(1)

	// tearingDown marks the deliberate close below, so the read error it provokes in
	// the other direction is not mistaken for a relay failure. Without it, every
	// normal end-of-connection would surface as "use of closed network connection".
	var tearingDown atomic.Bool

	// service -> client
	var sendErr error
	go func() {
		defer wg.Done()
		buf := make([]byte, relayChunk)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if sErr := stream.Send(&pb.PortForwardFrame{
					Frame: &pb.PortForwardFrame_Data{Data: append([]byte(nil), buf[:n]...)},
				}); sErr != nil {
					sendErr = sErr
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !tearingDown.Load() {
					sendErr = err
				}
				return
			}
		}
	}()

	// client -> service
	var recvErr error
	for {
		frame, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				recvErr = err
			}
			break
		}
		if closed := frame.GetClosed(); closed != nil {
			break
		}
		if data := frame.GetData(); len(data) > 0 {
			if _, wErr := conn.Write(data); wErr != nil {
				recvErr = wErr
				break
			}
		}
	}
	// Unblock the reader goroutine: without this it stays parked in conn.Read until
	// the service happens to send something, leaking a goroutine per dead connection.
	tearingDown.Store(true)
	_ = conn.Close()
	wg.Wait()

	if recvErr != nil {
		return recvErr
	}
	return sendErr
}
