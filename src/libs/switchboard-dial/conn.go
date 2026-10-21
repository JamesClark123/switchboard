package dial

import (
	"context"
	"fmt"
	"net"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// handshakeTimeout bounds the GetDaemonInfo handshake that confirms a dial.
const handshakeTimeout = 10 * time.Second

// Conn is a live, handshake-verified connection to one daemon.
type Conn struct {
	// Info is the DaemonInfo the daemon advertised at handshake.
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

// WorkspaceRoot returns the daemon's controlled workspace folder on its host
// (FR-006), advertised at handshake (empty if unknown).
func (c *Conn) WorkspaceRoot() string { return c.Info.GetWorkspaceRoot() }

// SbxVersion is the host sandbox CLI version the daemon reported at handshake
// (the parsed MAJOR.MINOR.PATCH when the daemon could read `sbx version`, else
// the raw line; empty if unknown).
func (c *Conn) SbxVersion() string { return c.Info.GetSbxVersion() }

// SbxMinVersion is the runtime baseline the daemon declares (feature 008,
// FR-096); RuntimeBaselineMet reports whether its host meets it.
func (c *Conn) SbxMinVersion() string    { return c.Info.GetSbxMinVersion() }
func (c *Conn) RuntimeBaselineMet() bool { return c.Info.GetRuntimeBaselineMet() }

// McpGateway reports whether the daemon's host can manage its MCP gateway, with
// the reason when it cannot (FR-079).
func (c *Conn) McpGateway() (available bool, reason string) {
	return c.Info.GetMcpGatewayAvailable(), c.Info.GetMcpGatewayReason()
}

// Close releases the underlying connection (and, for a stdio-bridged dial,
// terminates the child process).
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
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	info, err := api.GetDaemonInfo(hctx, &pb.GetDaemonInfoRequest{})
	if err != nil {
		_ = cc.Close()
		return nil, fmt.Errorf("daemon handshake failed (is switchboardd running?): %w", err)
	}
	return &Conn{hostID: info.GetHostId(), Info: info, cc: cc, api: api}, nil
}
