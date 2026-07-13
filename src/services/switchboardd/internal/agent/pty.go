package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Session is a live agent terminal: raw bytes in (keystrokes/prompts) and out
// (screen). Backed by a PTY in production; a fake in tests.
type Session interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

// SessionFactory creates a Session for a sandbox's agent.
type SessionFactory func(sandboxID string, spec *pb.AgentSpec) (Session, error)

// Registry holds one Session per sandbox, created on demand (R8: one PTY per
// agent serves both programmatic prompting and interactive attach).
type Registry struct {
	mu       sync.Mutex
	sessions map[string]Session
	factory  SessionFactory
}

// NewRegistry constructs a Registry using the given session factory.
func NewRegistry(factory SessionFactory) *Registry {
	return &Registry{sessions: map[string]Session{}, factory: factory}
}

// Session returns the sandbox's session, creating it on first use.
func (r *Registry) Session(sandboxID string, spec *pb.AgentSpec) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[sandboxID]; ok {
		return s, nil
	}
	s, err := r.factory(sandboxID, spec)
	if err != nil {
		return nil, err
	}
	r.sessions[sandboxID] = s
	return s, nil
}

// Prompt writes a prompt line to the sandbox's agent (FR-022).
func (r *Registry) Prompt(sandboxID string, spec *pb.AgentSpec, text string) error {
	s, err := r.Session(sandboxID, spec)
	if err != nil {
		return err
	}
	_, err = s.Write([]byte(text + "\n"))
	return err
}

// Close terminates and forgets a sandbox's session.
func (r *Registry) Close(sandboxID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[sandboxID]; ok {
		_ = s.Close()
		delete(r.sessions, sandboxID)
	}
}

// --- Production PTY-backed factory ---

// ptySession wraps a PTY file and its child process.
type ptySession struct {
	f   *os.File
	cmd *exec.Cmd
}

func (p *ptySession) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptySession) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *ptySession) Resize(cols, rows uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
}
func (p *ptySession) Close() error {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.f.Close()
}

// agentCommand maps an AgentSpec to the in-sandbox command. The daemon execs into
// the sandbox via `sbx exec`. The target MUST be the sbx-addressable handle
// (the sandbox's container_ref / --name), NOT the daemon's uuid registry key:
// sbx addresses sandboxes by name, so `sbx exec -it <uuid> …` would fail
// immediately and the PTY would report EOF the moment a client attached.
//
// The command is launched UNWRAPPED (no setsid/nohup) on purpose. The T002 spike
// (research.md R4 "Verification result") verified against real sbx/Docker that a
// docker-exec child survives both a hard kill of the host exec client and a
// controlling-PTY hangup — the exact ptySession.Close() sequence — so host-side
// persistence already satisfies FR-002 (an in-flight AI prompt keeps running after
// the terminal closes / across a daemon restart's client-kill). A setsid wrap would
// add nothing to survival and would strip the controlling TTY, risking interactive
// job-control breakage; it is therefore deliberately not used.
func agentCommand(sbxBin, target string, spec *pb.AgentSpec) *exec.Cmd {
	inner := "bash"
	if spec.GetKind() == "claude-code" {
		inner = "claude"
	}
	args := []string{"exec", "-it", target, inner}
	args = append(args, spec.GetArgs()...)
	return exec.Command(sbxBin, args...)
}

// PTYFactory returns a SessionFactory that starts the sandbox's agent under a PTY.
//
// resolve maps the daemon's uuid registry key to the sbx-addressable handle
// (container_ref / --name) that `sbx exec` expects. It may return "" when the
// sandbox is unknown; the factory then falls back to the uuid so behavior is no
// worse than before resolution existed. resolve may be nil (uuid used directly),
// which is intended only for tests.
func PTYFactory(sbxBin string, resolve func(sandboxID string) string) SessionFactory {
	return func(sandboxID string, spec *pb.AgentSpec) (Session, error) {
		target := sandboxID
		if resolve != nil {
			if ref := resolve(sandboxID); ref != "" {
				target = ref
			}
		}
		cmd := agentCommand(sbxBin, target, spec)
		f, err := pty.Start(cmd)
		if err != nil {
			return nil, fmt.Errorf("start pty for %s: %w", sandboxID, err)
		}
		return &ptySession{f: f, cmd: cmd}, nil
	}
}
