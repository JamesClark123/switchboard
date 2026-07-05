package agent

import (
	"errors"
	"io"
	"sync"
	"testing"

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
	// The prompt (plus newline) is readable from the session.
	buf := make([]byte, 64)
	n, _ := last.Read(buf)
	if string(buf[:n]) != "hello\n" {
		t.Errorf("prompt = %q, want hello\\n", string(buf[:n]))
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
	c := agentCommand("sbx", "sb1", &pb.AgentSpec{Kind: "claude-code", Args: []string{"--model", "opus"}})
	got := append([]string{c.Path}, c.Args[1:]...)
	want := []string{"sbx", "exec", "-it", "sb1", "claude", "--model", "opus"}
	// c.Path may be resolved to an absolute path; compare the trailing args.
	if len(got) != len(want) {
		t.Fatalf("args = %v", c.Args)
	}
	for i := 1; i < len(want); i++ {
		if c.Args[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, c.Args[i], want[i])
		}
	}
	// Non-claude agents default to a shell.
	c2 := agentCommand("sbx", "sb1", &pb.AgentSpec{})
	if c2.Args[len(c2.Args)-1] != "bash" {
		t.Errorf("default inner cmd = %q, want bash", c2.Args[len(c2.Args)-1])
	}
}
