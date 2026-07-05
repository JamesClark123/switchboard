package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// stdioConn adapts a child process's stdin/stdout to net.Conn so gRPC can speak
// over it. This is the Docker-CLI `dial-stdio` pattern (research R1): the child
// is `ssh <host> sxbd dial-stdio`, which bridges its stdio to the remote
// daemon's Unix socket.
type stdioConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	once   sync.Once
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *stdioConn) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_, _ = c.cmd.Process.Wait()
	})
	return nil
}

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }

func (c *stdioConn) LocalAddr() net.Addr              { return stdioAddr{} }
func (c *stdioConn) RemoteAddr() net.Addr             { return stdioAddr{} }
func (c *stdioConn) SetDeadline(time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(time.Time) error { return nil }

// newStdioConn starts cmd and wraps its stdio as a net.Conn. The child's stderr
// is forwarded to the client's stderr for diagnostics.
func newStdioConn(cmd *exec.Cmd) (*stdioConn, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	return &stdioConn{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// DialCommand dials a daemon over the stdio of cmd (which must bridge to the
// daemon's socket) and performs the GetDaemonInfo handshake. Closing the
// returned Conn terminates the child process.
func DialCommand(ctx context.Context, cmd *exec.Cmd) (*Conn, error) {
	sc, err := newStdioConn(cmd)
	if err != nil {
		return nil, err
	}

	var used bool
	var mu sync.Mutex
	dialer := func(context.Context, string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if used {
			// gRPC keeps a single HTTP/2 connection alive; a redial means the
			// pipe broke and cannot be re-established.
			return nil, errors.New("stdio connection already consumed (child exited?)")
		}
		used = true
		return sc, nil
	}

	cc, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		_ = sc.Close()
		return nil, err
	}
	conn, err := handshake(ctx, cc)
	if err != nil {
		_ = sc.Close()
		return nil, err
	}
	return conn, nil
}

// SSHCommand builds the `ssh [opts] <target> sxbd dial-stdio` command.
func SSHCommand(ctx context.Context, target string, opts []string) *exec.Cmd {
	args := make([]string, 0, len(opts)+3)
	args = append(args, opts...)
	args = append(args, target, "sxbd", "dial-stdio")
	return exec.CommandContext(ctx, "ssh", args...)
}

// DialSSH connects to a remote daemon over SSH using the dial-stdio bridge
// (FR-004). Auth/transport are the user's existing SSH (no new port, no separate
// auth system).
func DialSSH(ctx context.Context, target string, opts []string) (*Conn, error) {
	return DialCommand(ctx, SSHCommand(ctx, target, opts))
}
