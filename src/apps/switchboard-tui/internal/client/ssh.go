package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Env vars that wire sxb's own binary in as ssh's SSH_ASKPASS helper. When they
// are set, the process prints the password to stdout and exits (see
// RunAskpassIfRequested). This lets the TUI collect an SSH password in a masked
// field and hand it to ssh non-interactively — instead of ssh prompting on the
// shared controlling terminal, which the Bubble Tea TUI already owns in raw
// mode (there a tty prompt would capture the user's password keystrokes as UI
// commands).
const (
	askpassSentinelEnv = "SWITCHBOARD_SSH_ASKPASS"
	askpassSecretEnv   = "SWITCHBOARD_SSH_PASSWORD"
)

// RunAskpassIfRequested reports whether this process was invoked by ssh as its
// SSH_ASKPASS helper. When it returns true the caller MUST exit immediately
// without doing anything else — main() calls this before any other startup.
func RunAskpassIfRequested() bool {
	if os.Getenv(askpassSentinelEnv) != "1" {
		return false
	}
	fmt.Println(os.Getenv(askpassSecretEnv))
	return true
}

// askpassProgram is the absolute path to this binary, used as ssh's SSH_ASKPASS
// helper. Falls back to argv[0] if the executable path cannot be resolved.
func askpassProgram() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return os.Args[0]
}

// stdioConn adapts a child process's stdin/stdout to net.Conn so gRPC can speak
// over it. This is the Docker-CLI `dial-stdio` pattern (research R1): the child
// is `ssh <host> sxbd dial-stdio`, which bridges its stdio to the remote
// daemon's Unix socket.
type stdioConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	once   sync.Once
	stderr tailBuffer
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
	// Stderr is forwarded for diagnostics AND kept (bounded) so a failed dial can
	// be classified from ssh's own words (feature 008, startup sign-in).
	sc := &stdioConn{cmd: cmd, stdin: stdin, stdout: stdout}
	cmd.Stderr = io.MultiWriter(os.Stderr, &sc.stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	return sc, nil
}

// DialErrorKind classifies why an ssh-backed dial failed, from ssh's stderr.
type DialErrorKind int

const (
	DialOther       DialErrorKind = iota
	DialAuthFailed                // ssh rejected the credentials: a password may help
	DialUnreachable               // the host could not be reached at all
	DialNoDaemon                  // reached, but sxbd is not installed there
)

func (k DialErrorKind) String() string {
	switch k {
	case DialAuthFailed:
		return "authentication failed"
	case DialUnreachable:
		return "host unreachable"
	case DialNoDaemon:
		return "sxbd not found on the host"
	}
	return "connection failed"
}

// DialError wraps a failed ssh-backed dial with its classification and the tail
// of ssh's stderr, so the TUI can decide whether a password prompt is worth
// showing (FR-088) and tell the developer why otherwise (FR-089).
type DialError struct {
	Kind   DialErrorKind
	Err    error
	Stderr string
}

func (e *DialError) Error() string {
	msg := e.Kind.String()
	if line := lastLine(e.Stderr); line != "" {
		msg += ": " + line
	}
	return msg
}

func (e *DialError) Unwrap() error { return e.Err }

// ClassifyDialStderr maps ssh's stderr to a DialErrorKind.
func ClassifyDialStderr(stderr string) DialErrorKind {
	l := strings.ToLower(stderr)
	switch {
	case strings.Contains(l, "permission denied") || strings.Contains(l, "publickey") || strings.Contains(l, "password") || strings.Contains(l, "authentication"):
		return DialAuthFailed
	case strings.Contains(l, "sxbd: command not found") || strings.Contains(l, "sxbd: not found") || strings.Contains(l, "command not found"):
		return DialNoDaemon
	case strings.Contains(l, "connection refused") || strings.Contains(l, "timed out") || strings.Contains(l, "could not resolve") ||
		strings.Contains(l, "no route to host") || strings.Contains(l, "network is unreachable") || strings.Contains(l, "connection reset"):
		return DialUnreachable
	}
	return DialOther
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// tailBuffer keeps the last few KiB written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
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
		// Give the child a moment to flush its last words before classifying.
		_ = sc.cmd.Wait()
		stderr := sc.stderr.String()
		return nil, &DialError{Kind: ClassifyDialStderr(stderr), Err: err, Stderr: stderr}
	}
	return conn, nil
}

// SSHCommand builds `ssh [opts] [hardening] <target> sxbd dial-stdio`.
//
// The child is always started in its own session (Setsid), detached from the
// controlling terminal, so ssh can never read the TUI's tty for a password or
// host-key prompt. When password is non-empty it is supplied non-interactively
// via SSH_ASKPASS (this binary, see RunAskpassIfRequested); when it is empty
// only key/agent auth is attempted (BatchMode) so a passwordless connect fails
// fast instead of blocking on a prompt it can never answer.
//
// The command is deliberately NOT bound to a context via exec.CommandContext:
// the ssh child is the transport for the whole *Conn, which outlives the dial.
// Its lifetime is owned by stdioConn.Close (invoked on every dial-failure path
// and by gRPC when the transport is torn down at Conn.Close). Binding it to the
// short-lived dial context instead would kill the child the instant that
// context is cancelled — leaving a live *Conn whose transport is already dead,
// so the next RPC fails with "stdio connection already consumed (child exited?)".
func SSHCommand(target string, opts []string, password string) *exec.Cmd {
	args := make([]string, 0, len(opts)+8)
	// User-supplied opts come first: ssh honors the first value of a repeated
	// -o, so the caller can override any of the hardening defaults below.
	args = append(args, opts...)
	// accept-new adds unknown host keys without an interactive prompt while
	// still refusing changed keys — required now that there is no tty to prompt on.
	args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	if password != "" {
		// Cap password retries so a wrong password fails fast instead of
		// re-invoking askpass. Crucially, do NOT set PreferredAuthentications:
		// the user's keys/agent (and their ~/.ssh/config) must still be tried
		// first — the askpass password is only a fallback for when ssh actually
		// prompts. Forcing password-only here breaks key-authenticated hosts.
		args = append(args, "-o", "NumberOfPasswordPrompts=1")
	} else {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args, target, "sxbd", "dial-stdio")

	cmd := exec.Command("ssh", args...)
	// New session => no controlling terminal, so ssh must use SSH_ASKPASS (when
	// set) rather than the shared tty for any prompt.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if password != "" {
		env := append(os.Environ(),
			"SSH_ASKPASS="+askpassProgram(),
			"SSH_ASKPASS_REQUIRE=force", // OpenSSH >=8.4: use askpass even with a tty
			askpassSentinelEnv+"=1",
			askpassSecretEnv+"="+password,
		)
		// Older OpenSSH only consults SSH_ASKPASS when DISPLAY is set; a
		// placeholder suffices since our helper ignores it.
		if os.Getenv("DISPLAY") == "" {
			env = append(env, "DISPLAY=switchboard:0")
		}
		cmd.Env = env
	}
	return cmd
}

// DialSSH connects to a remote daemon over SSH using the dial-stdio bridge
// (FR-004). Auth/transport are the user's existing SSH (no new port, no separate
// auth system). password is supplied non-interactively when non-empty; empty
// means key/agent auth only. See SSHCommand.
func DialSSH(ctx context.Context, target string, opts []string, password string) (*Conn, error) {
	return DialCommand(ctx, SSHCommand(target, opts, password))
}
