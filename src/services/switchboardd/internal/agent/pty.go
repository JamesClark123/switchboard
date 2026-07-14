package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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

// Target describes how the daemon reaches a sandbox's agent through `sbx`.
type Target struct {
	// Ref is the sbx-addressable handle (container_ref / --name). It MUST NOT be
	// the daemon's uuid registry key: sbx addresses sandboxes by name, so
	// `sbx exec -it <uuid> …` fails immediately and the PTY reports EOF the moment
	// a client attaches.
	Ref string
	// Workdir is the in-container working directory to open the agent in — the
	// sandbox's code directory. sbx mounts the seeded workspace at the same path
	// it has on the host (which is why `sxb` run inside the sandbox finds the
	// workspace marker by walking up, FR-017), so this is the sandbox's
	// WorkspacePath. Empty means "leave sbx's default directory".
	Workdir string
}

// agentArgv builds the in-sandbox command + arguments to launch for an agent spec.
// The sbx runner currently always creates sandboxes with the `claude` agent
// (sandbox/runner.go), so an unset kind maps to claude rather than a bare shell —
// opening the terminal should drop the user into their agent, matching what
// `sbx run claude` does. A future multi-runner world can carry the concrete kind
// through here.
//
// Claude is launched with --dangerously-skip-permissions so it runs
// non-interactively inside the throwaway sandbox: a detached session has no
// terminal attached to answer a permission prompt, so it must never block on one.
// (The injected settings.local.json also sets defaultMode=bypassPermissions; the
// flag is the CLI-level belt-and-braces for the same intent.) Spec args follow so
// a caller can still override/extend the invocation.
func agentArgv(spec *pb.AgentSpec) []string {
	var argv []string
	switch spec.GetKind() {
	case "", "claude", "claude-code":
		argv = []string{"claude", "--dangerously-skip-permissions"}
	default:
		argv = []string{spec.GetKind()}
	}
	return append(argv, spec.GetArgs()...)
}

// agentCommand maps an AgentSpec to the in-sandbox command. The daemon execs into
// the sandbox via `sbx exec` and launches the agent from the sandbox's code
// directory (tgt.Workdir) so the terminal opens exactly where `sbx run <agent>`
// would — not at the container root. The `cd` is best-effort (`; exec` rather than
// `&& exec`) so a missing/renamed workspace still yields a working agent shell
// instead of an immediate EOF.
//
// The command is launched UNWRAPPED w.r.t. session/job-control (no setsid/nohup)
// on purpose. The T002 spike (research.md R4 "Verification result") verified
// against real sbx/Docker that a docker-exec child survives both a hard kill of
// the host exec client and a controlling-PTY hangup — the exact ptySession.Close()
// sequence — so host-side persistence already satisfies FR-002 (an in-flight AI
// prompt keeps running after the terminal closes / across a daemon restart's
// client-kill). `exec` replaces the wrapping shell with the agent so the PTY's
// controlling process IS the agent, keeping interactive job control intact.
func agentCommand(sbxBin string, tgt Target, spec *pb.AgentSpec) *exec.Cmd {
	launch := "exec"
	for _, a := range agentArgv(spec) {
		launch += " " + shellQuote(a)
	}
	if tgt.Workdir != "" {
		launch = "cd " + shellQuote(tgt.Workdir) + " 2>/dev/null; " + launch
	}
	args := []string{"exec", "-it", tgt.Ref, "bash", "-lc", launch}
	return exec.Command(sbxBin, args...)
}

// shellQuote wraps s in single quotes for safe use inside a `bash -lc` string,
// escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// PTYFactory returns a SessionFactory that starts the sandbox's agent under a PTY.
//
// resolve maps the daemon's uuid registry key to the sbx Target (handle + code
// directory). It may return a zero Target when the sandbox is unknown; the factory
// then falls back to targeting the uuid so behavior is no worse than before
// resolution existed. resolve may be nil (uuid used directly, no workdir), which is
// intended only for tests.
func PTYFactory(sbxBin string, resolve func(sandboxID string) Target) SessionFactory {
	return func(sandboxID string, spec *pb.AgentSpec) (Session, error) {
		tgt := Target{Ref: sandboxID}
		if resolve != nil {
			if r := resolve(sandboxID); r.Ref != "" {
				tgt = r
			}
		}
		cmd := agentCommand(sbxBin, tgt, spec)
		f, err := pty.Start(cmd)
		if err != nil {
			return nil, fmt.Errorf("start pty for %s: %w", sandboxID, err)
		}
		return &ptySession{f: f, cmd: cmd}, nil
	}
}
