package client

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestStdioAddrAndDeadlines(t *testing.T) {
	if (stdioAddr{}).Network() != "stdio" || (stdioAddr{}).String() != "stdio" {
		t.Error("stdioAddr should report the stdio network")
	}
	c := &stdioConn{}
	if c.LocalAddr().Network() != "stdio" || c.RemoteAddr().String() != "stdio" {
		t.Error("conn addrs should be stdio")
	}
	// Deadlines are no-ops (the underlying pipe has none).
	if c.SetDeadline(time.Now()) != nil || c.SetReadDeadline(time.Now()) != nil || c.SetWriteDeadline(time.Now()) != nil {
		t.Error("deadline setters should be no-ops returning nil")
	}
}

func TestNewStdioConnStartError(t *testing.T) {
	if _, err := newStdioConn(exec.Command("/no/such/binary/switchboard-test")); err == nil {
		t.Error("expected start error for a missing binary")
	}
}

func TestDefaultDialBothKinds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	// local: unreachable socket -> error.
	if _, err := defaultDial(ctx, HostEntry{Kind: "local", SocketPath: "/no/such.sock"}); err == nil {
		t.Error("expected local dial error for a missing socket")
	}
	// ssh: unresolvable target (or absent ssh binary) -> error, quickly.
	_, err := defaultDial(ctx, HostEntry{
		Kind:       "ssh",
		SSHTarget:  "switchboard.invalid",
		SSHOptions: []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=1"},
	})
	if err == nil {
		t.Error("expected ssh dial error for an unreachable target")
	}
}

func TestDialSSHError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if _, err := DialSSH(ctx, "switchboard.invalid", []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=1"}, ""); err == nil {
		t.Error("expected DialSSH error for an unreachable target")
	}
}
