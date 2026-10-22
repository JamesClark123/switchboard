package grpc_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
)

// drainUpdate collects the progress stream into a slice.
func drainUpdate(t *testing.T, client pb.SwitchboardClient, target string) []*pb.UpdateProgress {
	t.Helper()
	stream, err := client.UpdateDaemon(context.Background(), &pb.UpdateDaemonRequest{TargetVersion: target})
	if err != nil {
		t.Fatal(err)
	}
	var out []*pb.UpdateProgress
	for {
		p, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func TestUpdateDaemonHappyPath(t *testing.T) {
	client, _ := startServer(t)

	var applied []byte
	var appliedTo string
	restarted := make(chan string, 1)
	restore := sbgrpc.SetUpdateHooks(sbgrpc.UpdateHooks{
		Fetch: func(_ context.Context, target string) (string, []byte, error) {
			return "v9.9.9", []byte("NEWSXBD"), nil
		},
		Apply:    func(b []byte, path string) error { applied, appliedTo = b, path; return nil },
		ExecPath: func() (string, error) { return "/home/user/.local/bin/sxbd", nil },
		IsBrew:   func(string) bool { return false },
		Restart:  func(pidFile, exe string) error { restarted <- exe; return nil },
	})
	defer restore()

	msgs := drainUpdate(t, client, "v9.9.9")
	last := msgs[len(msgs)-1]
	if !last.GetDone() || last.GetNewVersion() != "v9.9.9" {
		t.Fatalf("terminal message = %+v, want done with new_version v9.9.9", last)
	}
	if string(applied) != "NEWSXBD" {
		t.Errorf("apply received %q, want NEWSXBD", applied)
	}
	if appliedTo != "/home/user/.local/bin/sxbd" {
		t.Errorf("applied to %q, want the resolved executable", appliedTo)
	}
	// Restart runs asynchronously after the handler returns, onto the path
	// resolved before the swap.
	select {
	case exe := <-restarted:
		if exe != "/home/user/.local/bin/sxbd" {
			t.Errorf("restart exe = %q, want the pre-swap path", exe)
		}
	case <-time.After(2 * time.Second):
		t.Error("restart was not invoked")
	}
	// A "restarting" stage must precede done.
	var sawRestarting bool
	for _, m := range msgs {
		if m.GetStage() == "restarting" {
			sawRestarting = true
		}
	}
	if !sawRestarting {
		t.Error("expected a restarting stage before done")
	}
}

func TestUpdateDaemonBrewManagedDefers(t *testing.T) {
	client, _ := startServer(t)
	var applied, restarted bool
	restore := sbgrpc.SetUpdateHooks(sbgrpc.UpdateHooks{
		Fetch:    func(_ context.Context, _ string) (string, []byte, error) { return "v9.9.9", []byte("x"), nil },
		Apply:    func([]byte, string) error { applied = true; return nil },
		ExecPath: func() (string, error) { return "/opt/homebrew/Cellar/switchboard/1/bin/sxbd", nil },
		IsBrew:   func(string) bool { return true },
		Restart:  func(string, string) error { restarted = true; return nil },
	})
	defer restore()

	msgs := drainUpdate(t, client, "")
	if len(msgs) != 1 || msgs[0].GetError() == "" {
		t.Fatalf("expected a single error message, got %+v", msgs)
	}
	if applied || restarted {
		t.Error("brew-managed daemon must not apply or restart")
	}
}

func TestUpdateDaemonFetchError(t *testing.T) {
	client, _ := startServer(t)
	var applied bool
	restore := sbgrpc.SetUpdateHooks(sbgrpc.UpdateHooks{
		Fetch:    func(_ context.Context, _ string) (string, []byte, error) { return "", nil, errors.New("boom") },
		Apply:    func([]byte, string) error { applied = true; return nil },
		ExecPath: func() (string, error) { return "/home/user/.local/bin/sxbd", nil },
		IsBrew:   func(string) bool { return false },
		Restart:  func(string, string) error { return nil },
	})
	defer restore()

	msgs := drainUpdate(t, client, "v1")
	last := msgs[len(msgs)-1]
	if last.GetError() == "" {
		t.Fatalf("expected an error message, got %+v", last)
	}
	if applied {
		t.Error("apply must not run when fetch fails")
	}
}

func TestUpdateDaemonAlreadyCurrent(t *testing.T) {
	client, _ := startServer(t) // server DaemonVersion is "test"
	var applied bool
	restore := sbgrpc.SetUpdateHooks(sbgrpc.UpdateHooks{
		Fetch:    func(_ context.Context, _ string) (string, []byte, error) { return "test", []byte("x"), nil },
		Apply:    func([]byte, string) error { applied = true; return nil },
		ExecPath: func() (string, error) { return "/home/user/.local/bin/sxbd", nil },
		IsBrew:   func(string) bool { return false },
		Restart:  func(string, string) error { return nil },
	})
	defer restore()

	msgs := drainUpdate(t, client, "")
	last := msgs[len(msgs)-1]
	if !last.GetDone() || last.GetNewVersion() != "test" {
		t.Fatalf("expected done at current version, got %+v", last)
	}
	if applied {
		t.Error("apply must not run when already current")
	}
}

// TestUpdateDaemonFromSwappedBackupName: a daemon that already updated once
// but could not restart names the deleted ".sxbd.old" backup on Linux; the
// next update must apply to and restart on the installed binary, not try to
// back up the backup.
func TestUpdateDaemonFromSwappedBackupName(t *testing.T) {
	client, _ := startServer(t)
	var appliedTo string
	restarted := make(chan string, 1)
	restore := sbgrpc.SetUpdateHooks(sbgrpc.UpdateHooks{
		Fetch:    func(_ context.Context, _ string) (string, []byte, error) { return "v9.9.9", []byte("x"), nil },
		Apply:    func(_ []byte, path string) error { appliedTo = path; return nil },
		ExecPath: func() (string, error) { return "/home/user/.local/bin/.sxbd.old", nil },
		IsBrew:   func(string) bool { return false },
		Restart:  func(_, exe string) error { restarted <- exe; return nil },
	})
	defer restore()

	msgs := drainUpdate(t, client, "")
	if last := msgs[len(msgs)-1]; !last.GetDone() {
		t.Fatalf("expected done, got %+v", last)
	}
	if appliedTo != "/home/user/.local/bin/sxbd" {
		t.Errorf("applied to %q, want the installed binary", appliedTo)
	}
	select {
	case exe := <-restarted:
		if exe != "/home/user/.local/bin/sxbd" {
			t.Errorf("restart exe = %q, want the installed binary", exe)
		}
	case <-time.After(2 * time.Second):
		t.Error("restart was not invoked")
	}
}
