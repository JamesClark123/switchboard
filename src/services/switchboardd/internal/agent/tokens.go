package agent

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// Per-agent hook tokens (SWI-17, research R13 §2). Each agent PTY the daemon
// spawns for a sandbox is numbered (its generation, from a per-sandbox counter
// persisted in the daemon store, so it only grows, across daemon restarts too)
// and gets a random token. Both reach the agent through its launch environment
// (SXB_HOOK_TOKEN, SXB_AGENT_GEN; see PTYFactory), never argv or a file, and
// every hook and escape-hatch call sends them back as headers. A hook whose
// token the sandbox does not have is dropped; one with none (an agent launched
// before this feature, or a process the daemon did not start) counts for status
// display only.
//
// A generation's token stays accepted until its agent is known gone (a probe
// found it gone, its sandbox stopped or was destroyed, or the developer stopped
// it as an orphan; research R14). An exec-client exit alone never retires it:
// the agent may outlive its client, as it outlives a daemon restart.
//
// The generation's main agent process is bound by its first non-compact
// SessionStart (BindMain), or, when none is bound, by a probe of the agent.pid
// the launch command records (BindFromProbe, T047). An event is the main
// agent's when it carries the current generation's token and that PID: nested
// `claude -p` sessions and processes left by an earlier generation are not.

// hookTokenBytes is the size of a per-agent hook token (data-model.md).
const hookTokenBytes = 32

// TokenStore persists the hook tokens (registry.Registry). The hook path never
// reads it: HookTokens keeps everything in memory, loaded once at start.
type TokenStore interface {
	PutAgentToken(sandboxID string, t registry.AgentToken) error
	SetAgentMain(sandboxID string, gen int64, pid int, start string) error
	DeleteAgentToken(sandboxID string, gen int64) error
	DeleteAgentTokens(sandboxID string, purge bool) error
	ListAgentTokens() (map[string]*registry.AgentGens, error)
}

// Spawn is what an agent is launched with: its generation and hook token. The
// zero value (no token store) launches it without either.
type Spawn struct {
	Gen   int64
	Token string
}

// HookTokens is the in-memory index of every sandbox's generations, tokens and
// main agent processes, written through to the TokenStore. It never holds its
// lock across a store call: Generation is read inside the sandbox store's own
// write transactions (holdForDeveloper).
type HookTokens struct {
	store TokenStore
	logf  func(format string, args ...any)

	mu  sync.Mutex
	sbs map[string]*agentGens
}

type agentGens struct {
	counter int64
	gens    map[int64]*genEntry
}

type genEntry struct {
	token     []byte
	mainPID   int
	mainStart string
}

// LoadHookTokens builds the index from the store.
func LoadHookTokens(store TokenStore) (*HookTokens, error) {
	all, err := store.ListAgentTokens()
	if err != nil {
		return nil, fmt.Errorf("load agent tokens: %w", err)
	}
	t := &HookTokens{store: store, sbs: map[string]*agentGens{}}
	for id, g := range all {
		s := t.of(id)
		s.counter = g.Counter
		for _, tok := range g.Tokens {
			s.gens[tok.Gen] = &genEntry{token: []byte(tok.Token), mainPID: tok.MainPID, mainStart: tok.MainStart}
			s.counter = max(s.counter, tok.Gen)
		}
	}
	return t, nil
}

// SetLogger registers where refused binds and store failures are reported
// (nil = silent). No line ever carries a token.
func (t *HookTokens) SetLogger(logf func(format string, args ...any)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logf = logf
}

func (t *HookTokens) log(format string, args ...any) {
	t.mu.Lock()
	logf := t.logf
	t.mu.Unlock()
	if logf != nil {
		logf(format, args...)
	}
}

// of returns a sandbox's entry, creating it. Callers hold mu (or own t).
func (t *HookTokens) of(sandboxID string) *agentGens {
	s := t.sbs[sandboxID]
	if s == nil {
		s = &agentGens{gens: map[int64]*genEntry{}}
		t.sbs[sandboxID] = s
	}
	return s
}

// Issue numbers a new agent generation for the sandbox and draws its token,
// persisting both before returning, so the agent's first hook finds them. The
// generation is reserved in memory first: Generation never reads lower than an
// agent being spawned.
func (t *HookTokens) Issue(sandboxID string) (Spawn, error) {
	raw := make([]byte, hookTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Spawn{}, fmt.Errorf("draw agent token: %w", err)
	}
	tok := hex.EncodeToString(raw)
	t.mu.Lock()
	s := t.of(sandboxID)
	s.counter++
	gen := s.counter
	t.mu.Unlock()
	if err := t.store.PutAgentToken(sandboxID, registry.AgentToken{Gen: gen, Token: tok}); err != nil {
		return Spawn{}, fmt.Errorf("persist agent token: %w", err)
	}
	t.mu.Lock()
	t.of(sandboxID).gens[gen] = &genEntry{token: []byte(tok)}
	t.mu.Unlock()
	return Spawn{Gen: gen, Token: tok}, nil
}

// Retire drops one generation's token: its agent is known gone (or was never
// started). A missing one is a no-op.
func (t *HookTokens) Retire(sandboxID string, gen int64) {
	t.mu.Lock()
	s, ok := t.sbs[sandboxID]
	if ok {
		delete(s.gens, gen)
	}
	t.mu.Unlock()
	if !ok {
		return
	}
	if err := t.store.DeleteAgentToken(sandboxID, gen); err != nil {
		t.log("[agent] %s: retire generation %d: %v", sandboxID, gen, err)
	}
}

// RetireAll drops every token of a sandbox: it stopped (no agent survives a
// stop) or, with purge, was destroyed, when its counter goes too.
func (t *HookTokens) RetireAll(sandboxID string, purge bool) {
	t.mu.Lock()
	s, ok := t.sbs[sandboxID]
	switch {
	case !ok:
	case purge:
		delete(t.sbs, sandboxID)
	default:
		s.gens = map[int64]*genEntry{}
	}
	t.mu.Unlock()
	if !ok {
		return
	}
	if err := t.store.DeleteAgentTokens(sandboxID, purge); err != nil {
		t.log("[agent] %s: retire agent tokens: %v", sandboxID, err)
	}
}

// Generation is the sandbox's agent generation counter: the generation of the
// last agent spawned for it (0 when none ever was). hold_gen records it.
func (t *HookTokens) Generation(sandboxID string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.sbs[sandboxID]; ok {
		return s.counter
	}
	return 0
}

// Unretired lists the sandbox's generations whose token is still accepted, in
// no particular order (T047: only such a sandbox may have a surviving agent).
func (t *HookTokens) Unretired(sandboxID string) []int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []int64
	if s, ok := t.sbs[sandboxID]; ok {
		for g := range s.gens {
			out = append(out, g)
		}
	}
	return out
}

// current is the sandbox's newest unretired generation (0 when none). Callers
// hold mu.
func (s *agentGens) current() int64 {
	var cur int64
	for g := range s.gens {
		cur = max(cur, g)
	}
	return cur
}

// Verify checks a call's X-Sxb-Gen / X-Sxb-Token pair against the sandbox's
// unretired tokens, comparing that generation's token in constant time. ok
// gives the call's generation.
func (t *HookTokens) Verify(sandboxID, genHeader, token string) (gen int64, ok bool) {
	gen, err := strconv.ParseInt(genHeader, 10, 64)
	if err != nil || token == "" {
		return 0, false
	}
	t.mu.Lock()
	var want []byte
	if s, found := t.sbs[sandboxID]; found {
		if e := s.gens[gen]; e != nil {
			want = e.token
		}
	}
	t.mu.Unlock()
	if want == nil || subtle.ConstantTimeCompare(want, []byte(token)) != 1 {
		return 0, false
	}
	return gen, true
}

// BindMain binds the main agent process of the sandbox's current generation
// from its first non-compact SessionStart (research R13). Only the current
// generation binds (a process left by an earlier one never does), only once,
// and only a real PID. It reports whether it bound.
func (t *HookTokens) BindMain(sandboxID string, gen int64, pid int) bool {
	t.mu.Lock()
	s, ok := t.sbs[sandboxID]
	var e *genEntry
	if ok && gen == s.current() {
		e = s.gens[gen]
	}
	if e == nil || e.mainPID != 0 || pid <= 0 {
		t.mu.Unlock()
		return false
	}
	e.mainPID = pid
	t.mu.Unlock()
	if err := t.store.SetAgentMain(sandboxID, gen, pid, ""); err != nil {
		t.log("[agent] %s: persist main agent of generation %d: %v", sandboxID, gen, err)
	}
	return true
}

// IsMain reports whether a verified call of generation gen from process pid is
// the sandbox's main agent: gen is the current generation and pid its bound
// main PID. Until the PID is bound nothing is.
func (t *HookTokens) IsMain(sandboxID string, gen int64, pid int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sbs[sandboxID]
	if !ok || pid <= 0 || gen != s.current() {
		return false
	}
	e := s.gens[gen]
	return e != nil && e.mainPID == pid
}

// MainPID returns a generation's bound main process and its start time (empty
// unless a probe bound it); ok is false when none is bound.
func (t *HookTokens) MainPID(sandboxID string, gen int64) (pid int, start string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, found := t.sbs[sandboxID]; found {
		if e := s.gens[gen]; e != nil && e.mainPID != 0 {
			return e.mainPID, e.mainStart, true
		}
	}
	return 0, "", false
}

// ProbedAgent is what a probe of a sandbox (T047) read about the process its
// ~/.sxb/agent.pid names, and about the probe shell's own parent: the
// reference for what the runtime's exec path looks like (research R13).
type ProbedAgent struct {
	Gen   int64  // the generation agent.pid records
	PID   int    // the process it names
	Start string // its start time (/proc/<pid>/stat field 22), checked by the probe
	// Exe is readlink /proc/<pid>/exe; AgentExe the agent binary the probe
	// resolved (readlink -f "$(command -v claude)").
	Exe, AgentExe string
	Cmdline       []string // /proc/<pid>/cmdline
	// PPID / PPIDExe are the candidate's parent and its executable;
	// RefPPID / RefPPIDExe the probe shell's.
	PPID       int
	PPIDExe    string
	RefPPID    int
	RefPPIDExe string
}

// errProbeRefused is a probed process that cannot be the generation's agent.
var errProbeRefused = errors.New("not the daemon's agent")

// notExecParents are parents that mean a process was started from inside the
// sandbox, not by the runtime's exec path.
var notExecParents = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ash": true, "ksh": true, "fish": true,
	"busybox": true, "script": true, "tmux": true, "screen": true,
}

// CheckProbed decides whether a probed process may be bound as a generation's
// main agent (research R13): its executable is the agent binary, its command
// line starts with the fixed flags, and it was started by the runtime's exec
// path, not from inside the sandbox — its parent matches the probe shell's own
// (both 0, or both the same non-shell, non-agent executable). The agent.pid
// file is writable from inside the sandbox, so nothing else in it is trusted.
func CheckProbed(p ProbedAgent) error {
	want := agentArgv(nil) // claude --dangerously-skip-permissions
	switch {
	case p.PID <= 0 || p.Gen <= 0:
		return fmt.Errorf("%w: no process", errProbeRefused)
	case !agentBinary(p.Exe, p.AgentExe):
		return fmt.Errorf("%w: executable %q is not the agent binary", errProbeRefused, p.Exe)
	case len(p.Cmdline) < len(want) || filepath.Base(p.Cmdline[0]) != want[0] || p.Cmdline[1] != want[1]:
		return fmt.Errorf("%w: command line does not start with %v", errProbeRefused, want)
	case p.PPID == 0 && p.RefPPID == 0:
		return nil
	case p.PPID == 0 || p.RefPPID == 0:
		return fmt.Errorf("%w: parent %d does not match the exec path's %d", errProbeRefused, p.PPID, p.RefPPID)
	case p.PPIDExe == "" || p.PPIDExe != p.RefPPIDExe:
		return fmt.Errorf("%w: parent %q does not match the exec path's %q", errProbeRefused, p.PPIDExe, p.RefPPIDExe)
	case notExecParents[filepath.Base(p.PPIDExe)] || p.PPIDExe == p.AgentExe:
		return fmt.Errorf("%w: started from inside the sandbox (parent %q)", errProbeRefused, p.PPIDExe)
	}
	return nil
}

// agentBinary reports whether exe is the agent binary the probe resolved, as
// probeDetect's isagent decides it: in the same directory, since an auto-update
// repoints claude at a sibling version (possibly deleting the old one) while a
// running agent keeps its executable.
func agentBinary(exe, agent string) bool {
	exe = strings.TrimSuffix(exe, " (deleted)")
	return exe != "" && agent != "" && filepath.Dir(exe) == filepath.Dir(agent)
}

// BindFromProbe fills a generation's main agent from a probe of agent.pid
// (T047), only when none is bound and CheckProbed passes; a refusal is logged
// and returned. A stored binding is never replaced: agent.pid can be rewritten
// from inside the sandbox, so a mismatch is logged and the stored PID stays.
func (t *HookTokens) BindFromProbe(sandboxID string, p ProbedAgent) error {
	if err := CheckProbed(p); err != nil {
		t.log("[agent] %s: main agent of generation %d not bound from agent.pid: %v", sandboxID, p.Gen, err)
		return err
	}
	t.mu.Lock()
	var e *genEntry
	if s, ok := t.sbs[sandboxID]; ok {
		e = s.gens[p.Gen]
	}
	switch {
	case e == nil:
		t.mu.Unlock()
		return fmt.Errorf("%w: generation %d has no token", errProbeRefused, p.Gen)
	case e.mainPID != 0:
		bound := e.mainPID
		t.mu.Unlock()
		if bound != p.PID {
			t.log("[agent] %s: agent.pid names process %d, but generation %d's main agent is %d; keeping it", sandboxID, p.PID, p.Gen, bound)
		}
		return nil
	}
	e.mainPID, e.mainStart = p.PID, p.Start
	t.mu.Unlock()
	if err := t.store.SetAgentMain(sandboxID, p.Gen, p.PID, p.Start); err != nil {
		t.log("[agent] %s: persist main agent of generation %d: %v", sandboxID, p.Gen, err)
	}
	return nil
}
