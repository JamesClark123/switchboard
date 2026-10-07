package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// authStub writes an sbx whose `mcp auth <name>` prints the documented URL line,
// records its own pid to pidfile, then runs `then` (e.g. sleep, exit 2).
func authStub(t *testing.T, then string) (m *Manager, pidfile string, events *int) {
	t.Helper()
	dir := t.TempDir()
	pidfile = filepath.Join(dir, "pid")
	script := "#!/usr/bin/env bash\ncase \"$*\" in\n  \"mcp auth x\") echo $$ > \"" + pidfile + "\"; echo 'Resolving MCP server \"x\"...'; echo 'Open this URL to authorize MCP server \"x\": https://example.test/auth?s=1'; " + then + " ;;\n  *) exit 1 ;;\nesac\n"
	bin := filepath.Join(dir, "sbx")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	n := 0
	events = &n
	m = NewManager(&CLI{Bin: bin}, reg, "h", func(*pb.Event) { n++ })
	return m, pidfile, events
}

func waitGone(t *testing.T, pidfile string) {
	t.Helper()
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("pidfile: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // ESRCH: gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("stub pid %d still alive after the wait ended", pid)
}

type frames struct {
	url  chan string
	all  []*pb.McpAuthProgress
	done *pb.McpAuthState
	errs []string
	msgs []string
}

func newFrames() *frames { return &frames{url: make(chan string, 1)} }

func (f *frames) on(p *pb.McpAuthProgress) {
	f.all = append(f.all, p)
	switch e := p.Event.(type) {
	case *pb.McpAuthProgress_Url:
		f.url <- e.Url
	case *pb.McpAuthProgress_Message:
		f.msgs = append(f.msgs, e.Message)
	case *pb.McpAuthProgress_Done:
		d := e.Done
		f.done = &d
	case *pb.McpAuthProgress_Error:
		f.errs = append(f.errs, e.Error)
	}
}

func TestAuthorizeStreamsURLThenCancelKillsTheGroup(t *testing.T) {
	m, pidfile, events := authStub(t, "sleep 30")
	fr := newFrames()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Authorize(ctx, "x", fr.on) }()
	select {
	case u := <-fr.url:
		if u != "https://example.test/auth?s=1" {
			t.Errorf("url = %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no URL frame within 5s")
	}
	// A second wait for the same name is refused while this one runs.
	if err := m.Authorize(ctx, "x", nil); !errors.Is(err, ErrAuthInProgress) {
		t.Errorf("concurrent authorize err = %v, want ErrAuthInProgress", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Authorize returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Authorize did not return after cancel")
	}
	if fr.done == nil || *fr.done != pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED {
		t.Errorf("terminal frame = %v, want UNAUTHORIZED", fr.done)
	}
	for _, p := range fr.all {
		if p.GetDeadlineUnix() <= time.Now().Unix() {
			t.Errorf("frame deadline %d should be in the future", p.GetDeadlineUnix())
		}
	}
	if len(fr.msgs) != 1 || !strings.Contains(fr.msgs[0], "Resolving") {
		t.Errorf("messages = %q, want the pre-URL progress line only", fr.msgs)
	}
	if *events != 0 {
		t.Error("a cancelled wait must not emit")
	}
	waitGone(t, pidfile)
	// The name is free again (an already-cancelled context keeps this instant).
	gone, cancelGone := context.WithCancel(context.Background())
	cancelGone()
	if err := m.Authorize(gone, "x", nil); errors.Is(err, ErrAuthInProgress) {
		t.Error("in-flight guard should release after the wait ends")
	}
}

func TestAuthorizeSuccessAndFailure(t *testing.T) {
	m, _, events := authStub(t, "echo 'MCP server \"x\" authorized'; exit 0")
	fr := newFrames()
	if err := m.Authorize(context.Background(), "x", fr.on); err != nil {
		t.Fatal(err)
	}
	if fr.done == nil || *fr.done != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED || len(fr.errs) != 0 {
		t.Errorf("success frames: done=%v errs=%q", fr.done, fr.errs)
	}
	if *events != 1 {
		t.Errorf("events = %d, want 1", *events)
	}

	m2, _, events2 := authStub(t, "echo 'authorization denied by provider' >&2; exit 2")
	fr2 := newFrames()
	if err := m2.Authorize(context.Background(), "x", fr2.on); err != nil {
		t.Fatalf("a non-zero exit is reported in frames, not returned: %v", err)
	}
	if fr2.done == nil || *fr2.done != pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED {
		t.Errorf("failure terminal frame = %v", fr2.done)
	}
	if len(fr2.errs) != 1 || !strings.Contains(fr2.errs[0], "denied by provider") || !strings.Contains(fr2.errs[0], "exit status 2") {
		t.Errorf("error frames = %q", fr2.errs)
	}
	if last := fr2.all[len(fr2.all)-1]; last.GetDone() != pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED {
		t.Error("the Done frame must be last")
	}
	if *events2 != 0 {
		t.Error("a failed authorization must not emit")
	}
}

func TestAuthorizeTimeoutKillsAndReportsUnauthorized(t *testing.T) {
	prev := authTimeout
	authTimeout = 400 * time.Millisecond
	t.Cleanup(func() { authTimeout = prev })
	m, pidfile, _ := authStub(t, "sleep 30")
	fr := newFrames()
	start := time.Now()
	if err := m.Authorize(context.Background(), "x", fr.on); err != nil {
		t.Fatalf("timeout should not be returned as an error: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("wait took %v, should end at the bound", time.Since(start))
	}
	if fr.done == nil || *fr.done != pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED {
		t.Errorf("terminal frame = %v, want UNAUTHORIZED", fr.done)
	}
	waitGone(t, pidfile)
}

func TestAuthorizeStartFailureIsReturned(t *testing.T) {
	reg, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reg.Close() }()
	m := NewManager(&CLI{Bin: filepath.Join(t.TempDir(), "missing")}, reg, "h", nil)
	if err := m.Authorize(context.Background(), "x", nil); err == nil {
		t.Error("a CLI that cannot start must be reported as an error")
	}
}

func TestAuthTimeoutConstant(t *testing.T) {
	if AuthTimeout != 10*time.Minute {
		t.Errorf("AuthTimeout = %v, want 10m (clarified bound)", AuthTimeout)
	}
}
