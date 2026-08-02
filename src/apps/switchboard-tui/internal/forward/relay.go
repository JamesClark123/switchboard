package forward

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// relayChunk is the read size for the local→daemon direction, matching the
// daemon-side relay.
const relayChunk = 32 * 1024

// accept loops on the local listener, giving each accepted TCP connection its own
// ForwardPort stream.
//
// One stream per connection (rather than one multiplexed stream per service) means
// a single stalled or half-closed browser connection cannot wedge the others, and
// the daemon side needs no framing beyond the frames the contract already defines.
func (f *forward) accept(ctx context.Context, opener Opener) {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return // listener closed: the forward is being torn down
		}
		if ctx.Err() != nil {
			_ = conn.Close()
			return
		}
		go f.relay(ctx, opener, conn)
	}
}

// relay carries one TCP connection to the service and back.
func (f *forward) relay(ctx context.Context, opener Opener, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	stream, err := opener.ForwardPort(ctx)
	if err != nil {
		return
	}
	if err := stream.Send(&pb.PortForwardFrame{
		Frame: &pb.PortForwardFrame_Open_{Open: &pb.PortForwardFrame_Open{
			InstanceId: f.instanceID,
			LocalPort:  f.localPort,
		}},
	}); err != nil {
		return
	}

	// The daemon answers `opened` once it has dialled the service; anything else
	// (a stopped service, an unknown instance) ends the connection here rather than
	// leaving a browser hanging on a socket that goes nowhere.
	first, err := stream.Recv()
	if err != nil || first.GetOpened() == nil {
		return
	}

	var wg sync.WaitGroup
	wg.Add(1)

	// daemon -> local
	go func() {
		defer wg.Done()
		defer func() { _ = conn.Close() }()
		for {
			frame, err := stream.Recv()
			if err != nil {
				return
			}
			if frame.GetClosed() != nil {
				return
			}
			if data := frame.GetData(); len(data) > 0 {
				if _, err := conn.Write(data); err != nil {
					return
				}
			}
		}
	}()

	// local -> daemon
	buf := make([]byte, relayChunk)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if sErr := stream.Send(&pb.PortForwardFrame{
				Frame: &pb.PortForwardFrame_Data{Data: append([]byte(nil), buf[:n]...)},
			}); sErr != nil {
				break
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				break
			}
			// Half-close: tell the daemon this direction is done so it can finish
			// writing the response instead of being cut off mid-body.
			_ = stream.Send(&pb.PortForwardFrame{
				Frame: &pb.PortForwardFrame_Closed_{Closed: &pb.PortForwardFrame_Closed{}},
			})
			break
		}
	}
	_ = stream.CloseSend()
	wg.Wait()
}
