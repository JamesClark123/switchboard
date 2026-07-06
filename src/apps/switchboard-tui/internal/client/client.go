// Package client is the TUI's gRPC client to one or more daemons. A Conn wraps a
// single daemon connection (local Unix socket now; SSH dial-stdio is added in
// US3) together with the daemon's advertised DaemonInfo.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Conn is a live connection to one daemon.
type Conn struct {
	Info *pb.DaemonInfo

	hostID string
	cc     *grpc.ClientConn
	api    pb.SwitchboardClient
}

// API exposes the raw gRPC stub.
func (c *Conn) API() pb.SwitchboardClient { return c.api }

// HostID returns the connected daemon's host id (FR-006).
func (c *Conn) HostID() string { return c.hostID }

// DaemonVersion returns the version advertised by the connected daemon at
// handshake (empty if unknown).
func (c *Conn) DaemonVersion() string { return c.Info.GetDaemonVersion() }

// UpdateDaemon asks the connected daemon to self-update to target (empty =
// latest), forwarding each progress message to onProgress. It returns nil once
// the daemon reports success; the daemon restarts on the new binary immediately
// after, so this connection becomes unusable and the caller should reconnect.
func (c *Conn) UpdateDaemon(ctx context.Context, target string, onProgress func(stage, message string)) error {
	stream, err := c.api.UpdateDaemon(ctx, &pb.UpdateDaemonRequest{TargetVersion: target})
	if err != nil {
		return err
	}
	for {
		p, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if onProgress != nil {
			onProgress(p.GetStage(), p.GetMessage())
		}
		if e := p.GetError(); e != "" {
			return errors.New(e)
		}
		if p.GetDone() {
			return nil
		}
	}
}

// Close releases the underlying connection.
func (c *Conn) Close() error {
	if c.cc != nil {
		return c.cc.Close()
	}
	return nil
}

// DialLocal connects to a daemon over a local Unix domain socket (FR-003) and
// performs the GetDaemonInfo handshake (FR-006) to learn the host id.
func DialLocal(ctx context.Context, socketPath string) (*Conn, error) {
	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		return net.Dial("unix", socketPath)
	}
	cc, err := grpc.NewClient("passthrough:///unix",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	return handshake(ctx, cc)
}

// handshake runs GetDaemonInfo to confirm reachability and capture identity.
func handshake(ctx context.Context, cc *grpc.ClientConn) (*Conn, error) {
	api := pb.NewSwitchboardClient(cc)
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := api.GetDaemonInfo(hctx, &pb.GetDaemonInfoRequest{})
	if err != nil {
		_ = cc.Close()
		return nil, fmt.Errorf("daemon handshake failed (is switchboardd running?): %w", err)
	}
	return &Conn{hostID: info.GetHostId(), Info: info, cc: cc, api: api}, nil
}
