package agent

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// memSession is an in-memory echo Session: bytes written are made readable.
type memSession struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	resize [2]uint16
}

func newMemSession() *memSession {
	s := &memSession{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *memSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("closed")
	}
	s.buf = append(s.buf, p...)
	s.cond.Signal()
	return len(p), nil
}

func (s *memSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) == 0 && !s.closed {
		s.cond.Wait()
	}
	if len(s.buf) == 0 && s.closed {
		return 0, io.EOF
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *memSession) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resize = [2]uint16{cols, rows}
	return nil
}

func (s *memSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.cond.Broadcast()
	return nil
}

func TestRegistryPromptAndReuse(t *testing.T) {
	// Prompt sleeps promptSubmitDelay between the body and the Enter; zero it so
	// the test is fast and deterministic (both writes land before the Read below).
	defer func(d time.Duration) { promptSubmitDelay = d }(promptSubmitDelay)
	promptSubmitDelay = 0

	created := 0
	last := newMemSession()
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) {
		created++
		last = newMemSession()
		return last, nil
	})

	if err := reg.Prompt("sb1", &pb.AgentSpec{}, "hello"); err != nil {
		t.Fatal(err)
	}
	// The body is typed as one bracketed paste and submitted with a carriage
	// return (Enter), not a linefeed: a raw-mode TUI treats "\n" as a literal
	// newline and only "\r" as submit, and only the paste terminator keeps the
	// Enter a separate keystroke when both land in one read.
	buf := make([]byte, 64)
	n, _ := last.Read(buf)
	if got := string(buf[:n]); got != Typed("hello") {
		t.Errorf("prompt = %q, want %q", got, Typed("hello"))
	}

	// A second prompt reuses the same session (created once).
	if err := reg.Prompt("sb1", &pb.AgentSpec{}, "again"); err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Errorf("session created %d times, want 1 (reuse)", created)
	}

	// Resize flows through.
	sess, _ := reg.Session("sb1", &pb.AgentSpec{})
	if err := sess.Resize(80, 24); err != nil {
		t.Fatal(err)
	}

	reg.Close("sb1")
	// After close a new prompt re-creates the session.
	if err := reg.Prompt("sb1", &pb.AgentSpec{}, "x"); err != nil {
		t.Fatal(err)
	}
	if created != 2 {
		t.Errorf("session created %d times after close, want 2", created)
	}
}

func TestRegistryFactoryError(t *testing.T) {
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) {
		return nil, errors.New("no pty")
	})
	if err := reg.Prompt("sb1", &pb.AgentSpec{}, "x"); err == nil {
		t.Error("expected prompt error when the factory fails")
	}
}

func TestAgentCommandMapping(t *testing.T) {
	// The agent is launched from the sandbox's code directory via a bash -lc that
	// cd's then execs, so the terminal opens where `sbx run <agent>` would.
	c := agentCommand("sbx", Target{Ref: "sb-ref", Workdir: "/home/u/ws"},
		&pb.AgentSpec{Kind: "claude-code", Args: []string{"--model", "opus"}}, Spawn{})
	want := []string{"exec", "-it", "sb-ref", "bash", "-lc"}
	if len(c.Args) != len(want)+2 { // Args[0] is the binary; +1 for the launch script
		t.Fatalf("args = %v", c.Args)
	}
	for i, w := range want {
		if c.Args[i+1] != w {
			t.Errorf("arg[%d] = %q, want %q", i+1, c.Args[i+1], w)
		}
	}
	script := c.Args[len(c.Args)-1]
	// Claude runs with --dangerously-skip-permissions (non-interactive sandbox),
	// then any spec args, from the code directory.
	for _, sub := range []string{
		"cd '/home/u/ws'",
		"exec 'claude' '--dangerously-skip-permissions' '--model' 'opus'",
	} {
		if !strings.Contains(script, sub) {
			t.Errorf("launch script %q missing %q", script, sub)
		}
	}

	// An unset kind still targets the agent (claude) with the skip-permissions
	// flag, matching the sbx runner, rather than dropping to a bare shell.
	c2 := agentCommand("sbx", Target{Ref: "sb-ref"}, &pb.AgentSpec{}, Spawn{})
	if s := c2.Args[len(c2.Args)-1]; !strings.Contains(s, "exec 'claude' '--dangerously-skip-permissions'") {
		t.Errorf("default launch = %q, want it to exec claude with --dangerously-skip-permissions", s)
	}
	// With no workdir there is no cd prefix.
	if s := c2.Args[len(c2.Args)-1]; strings.Contains(s, "cd ") {
		t.Errorf("launch %q should not cd when workdir is empty", s)
	}
}

// written drains what was written into a memSession.
func (s *memSession) written() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := string(s.buf)
	s.buf = nil
	return out
}

func TestTypeStepAndRePress(t *testing.T) {
	sess := newMemSession()
	created := 0
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { created++; return sess, nil })
	at := time.Unix(1_700_000_000, 0)
	reg.now = func() time.Time { return at }
	// No live agent: nothing is spawned, nothing typed.
	if _, err := reg.TypeStep("sb", &pb.AgentSpec{}, "/model opus"); !errors.Is(err, ErrNoLiveAgent) || created != 0 {
		t.Fatalf("TypeStep with no agent = %v (created %d)", err, created)
	}
	if err := reg.RePress("sb", 0); !errors.Is(err, ErrNoLiveAgent) || created != 0 {
		t.Fatalf("RePress with no agent = %v (created %d)", err, created)
	}
	if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "shell"}); err != nil {
		t.Fatal(err)
	}
	sentAt, err := reg.TypeStep("sb", &pb.AgentSpec{}, "/model opus")
	if err != nil || !sentAt.Equal(at) {
		t.Fatalf("TypeStep = %v / %v", sentAt, err)
	}
	if got := sess.written(); got != Typed("/model opus") {
		t.Errorf("typed %q, want one paste then Enter", got)
	}
	// No blind re-press follows on its own.
	time.Sleep(20 * time.Millisecond)
	if got := sess.written(); got != "" {
		t.Errorf("TypeStep must not re-press by itself: %q", got)
	}
	for n, want := range []string{"\r", keypadEnter, keypadEnter} {
		if err := reg.RePress("sb", n); err != nil {
			t.Fatal(err)
		}
		if got := sess.written(); got != want {
			t.Errorf("re-press %d = %q, want %q", n, got, want)
		}
	}
	// A write failure is returned.
	_ = sess.Close()
	if _, err := reg.TypeStep("sb", nil, "x"); err == nil {
		t.Error("a failed paste must error")
	}
}

// failAfter is a Session whose writes fail after the first n.
type failAfter struct {
	memSession
	n int
}

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("pty gone")
	}
	f.n--
	return len(p), nil
}

func TestTypeStepEnterFailure(t *testing.T) {
	sess := &failAfter{n: 1}
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return sess, nil })
	if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "shell"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.TypeStep("sb", nil, "x"); err == nil {
		t.Error("a failed Enter must error")
	}
}
