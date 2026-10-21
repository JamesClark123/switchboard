// Package client is the TUI's gRPC client to one or more daemons. A Conn wraps a
// single daemon connection (local Unix socket, or SSH dial-stdio) together with
// the daemon's advertised DaemonInfo. The transport itself — the stdio bridge,
// the SSH hardening and the dial-failure classification — lives in the shared
// libs/switchboard-dial module (feature 009, research R2) so the daemon's
// orchestrator runtime dials worker machines exactly as the TUI does; this
// package keeps the typed RPC wrappers, the host Manager and the askpass helper.
package client

import (
	"context"
	"errors"
	"io"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Conn is a live connection to one daemon: the shared dial.Conn (handshake,
// cached DaemonInfo accessors, Close) plus this package's typed RPC wrappers.
type Conn struct {
	*dial.Conn
}

// wrap adopts a dialed connection, passing a dial error through untouched (a
// *dial.DialError keeps its classification for the sign-in flow).
func wrap(c *dial.Conn, err error) (*Conn, error) {
	if err != nil {
		return nil, err
	}
	return &Conn{Conn: c}, nil
}

// DialLocal connects to a daemon over a local Unix domain socket (FR-003) and
// performs the GetDaemonInfo handshake (FR-006) to learn the host id.
func DialLocal(ctx context.Context, socketPath string) (*Conn, error) {
	return wrap(dial.DialLocal(ctx, socketPath))
}

// UpdateDaemon asks the connected daemon to self-update to target (empty =
// latest), forwarding each progress message to onProgress. It returns nil once
// the daemon reports success; the daemon restarts on the new binary immediately
// after, so this connection becomes unusable and the caller should reconnect.
func (c *Conn) UpdateDaemon(ctx context.Context, target string, onProgress func(stage, message string)) error {
	stream, err := c.API().UpdateDaemon(ctx, &pb.UpdateDaemonRequest{TargetVersion: target})
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
