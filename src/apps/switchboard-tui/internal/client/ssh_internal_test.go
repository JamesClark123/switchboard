package client

import (
	"context"
	"testing"
	"time"
)

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
