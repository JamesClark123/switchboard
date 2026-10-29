package agent

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// openTokens opens a token index over a real store in dir.
func openTokens(t *testing.T, dir string) (*HookTokens, *registry.Registry) {
	t.Helper()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadHookTokens(reg)
	if err != nil {
		t.Fatal(err)
	}
	return tokens, reg
}

func TestHookTokensIssueVerifyAndRetire(t *testing.T) {
	dir := t.TempDir()
	tokens, reg := openTokens(t, dir)
	a, err := tokens.Issue("sb")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := tokens.Issue("sb")
	other, _ := tokens.Issue("sb2")
	if a.Gen != 1 || b.Gen != 2 || other.Gen != 1 || len(a.Token) != 2*hookTokenBytes || a.Token == b.Token {
		t.Fatalf("issued %+v %+v %+v", a, b, other)
	}
	if g, ok := tokens.Verify("sb", "2", b.Token); !ok || g != 2 {
		t.Errorf("Verify(own) = %d %v", g, ok)
	}
	for _, c := range []struct{ sb, gen, tok string }{
		{"sb", "2", "wrong"},       // a wrong token
		{"sb", "1", b.Token},       // another generation's
		{"sb", "x", b.Token},       // no generation
		{"sb", "2", ""},            // none
		{"sb2", "1", a.Token},      // another sandbox's
		{"nope", "1", other.Token}, // an unknown sandbox
	} {
		if _, ok := tokens.Verify(c.sb, c.gen, c.tok); ok {
			t.Errorf("Verify(%s, %s) accepted", c.sb, c.gen)
		}
	}
	if !tokens.BindMain("sb", 2, 77) || tokens.BindMain("sb", 2, 78) {
		t.Error("the main agent binds once")
	}
	if tokens.BindMain("sb", 1, 50) {
		t.Error("an earlier generation never binds")
	}
	_ = reg.Close()

	// A daemon restart: tokens, main PID and counter come back from the store.
	tokens, reg = openTokens(t, dir)
	defer func() { _ = reg.Close() }()
	if _, ok := tokens.Verify("sb", "2", b.Token); !ok || !tokens.IsMain("sb", 2, 77) {
		t.Error("a surviving agent's token and main PID must survive a restart")
	}
	if pid, _, ok := tokens.MainPID("sb", 2); !ok || pid != 77 {
		t.Errorf("MainPID = %d %v", pid, ok)
	}
	c, _ := tokens.Issue("sb")
	if c.Gen != 3 || tokens.Generation("sb") != 3 {
		t.Errorf("the generation counter only grows: %d", c.Gen)
	}
	tokens.Retire("sb", 3)
	if _, ok := tokens.Verify("sb", "3", c.Token); ok {
		t.Error("a retired token is refused")
	}
	if !tokens.IsMain("sb", 2, 77) {
		t.Error("with 3 retired, 2 is current again")
	}
	if got := tokens.Unretired("sb"); len(got) != 2 {
		t.Errorf("unretired = %v", got)
	}
	tokens.RetireAll("sb", false)
	if _, ok := tokens.Verify("sb", "2", b.Token); ok || tokens.Generation("sb") != 3 {
		t.Error("a stop retires every token and keeps the counter")
	}
	tokens.RetireAll("sb2", true)
	if tokens.Generation("sb2") != 0 {
		t.Error("a destroy purges the counter")
	}
	tokens.Retire("nope", 1)
	tokens.RetireAll("nope", true)
	if _, _, ok := tokens.MainPID("nope", 1); ok {
		t.Error("nothing bound for an unknown sandbox")
	}
}

// failingStore fails every write.
type failingStore struct{ *registry.Registry }

var errDisk = errors.New("disk full")

func (failingStore) PutAgentToken(string, registry.AgentToken) error { return errDisk }
func (failingStore) SetAgentMain(string, int64, int, string) error   { return errDisk }
func (failingStore) DeleteAgentToken(string, int64) error            { return errDisk }
func (failingStore) DeleteAgentTokens(string, bool) error            { return errDisk }
func (failingStore) ListAgentTokens() (map[string]*registry.AgentGens, error) {
	return map[string]*registry.AgentGens{"sb": {Counter: 4, Tokens: []registry.AgentToken{{Gen: 5, Token: "t"}}}}, nil
}

func TestHookTokensStoreFailures(t *testing.T) {
	tokens, err := LoadHookTokens(failingStore{})
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	var mu sync.Mutex
	tokens.SetLogger(func(f string, a ...any) { mu.Lock(); logged = append(logged, f); mu.Unlock() })
	if tokens.Generation("sb") != 5 {
		t.Errorf("the counter covers every stored generation: %d", tokens.Generation("sb"))
	}
	if _, err := tokens.Issue("sb"); !errors.Is(err, errDisk) {
		t.Errorf("Issue = %v", err)
	}
	if tokens.Generation("sb") != 6 {
		t.Error("a failed issue still never reuses its generation")
	}
	tokens.BindMain("sb", 5, 9)
	tokens.Retire("sb", 5)
	tokens.RetireAll("sb", false)
	if len(logged) != 3 {
		t.Errorf("store failures are logged: %v", logged)
	}
	if _, err := LoadHookTokens(errStore{}); err == nil {
		t.Error("an unreadable store fails the load")
	}
}

type errStore struct{ failingStore }

func (errStore) ListAgentTokens() (map[string]*registry.AgentGens, error) { return nil, errDisk }

// probed is a process that passes the probe's checks: the agent binary, the
// fixed flags, started by the exec path (its parent is the probe shell's).
func probed() ProbedAgent {
	return ProbedAgent{
		Gen: 1, PID: 300, Start: "4711", Exe: "/opt/claude/bin/claude", AgentExe: "/opt/claude/bin/claude",
		Cmdline: []string{"claude", "--dangerously-skip-permissions"},
	}
}

func TestCheckProbed(t *testing.T) {
	ok := []func(*ProbedAgent){
		func(*ProbedAgent) {}, // both parents 0: a plain container
		func(p *ProbedAgent) { p.Exe = "/opt/claude/bin/2.1.311 (deleted)" }, // auto-updated since
		func(p *ProbedAgent) { // both the runtime's exec agent
			p.PPID, p.PPIDExe, p.RefPPID, p.RefPPIDExe = 12, "/usr/libexec/sbx-exec-agent", 13, "/usr/libexec/sbx-exec-agent"
		},
	}
	for i, f := range ok {
		p := probed()
		f(&p)
		if err := CheckProbed(p); err != nil {
			t.Errorf("case %d: %v", i, err)
		}
	}
	refused := map[string]func(*ProbedAgent){
		"no process":      func(p *ProbedAgent) { p.PID = 0 },
		"sleep":           func(p *ProbedAgent) { p.Exe, p.Cmdline = "/usr/bin/sleep", []string{"sleep", "1000"} },
		"other flags":     func(p *ProbedAgent) { p.Cmdline = []string{"claude", "-p", "x"} },
		"elsewhere":       func(p *ProbedAgent) { p.Exe = "/tmp/x/claude" },
		"short cmdline":   func(p *ProbedAgent) { p.Cmdline = []string{"claude"} },
		"parent mismatch": func(p *ProbedAgent) { p.PPID, p.PPIDExe = 40, "/usr/bin/script" },
		"ref parent only": func(p *ProbedAgent) { p.RefPPID, p.RefPPIDExe = 12, "/usr/libexec/sbx-exec-agent" },
		"different exec":  func(p *ProbedAgent) { p.PPID, p.PPIDExe, p.RefPPID, p.RefPPIDExe = 40, "/a", 12, "/b" },
		"script -qc claude": func(p *ProbedAgent) { // inside the sandbox, on a new pty
			p.PPID, p.PPIDExe, p.RefPPID, p.RefPPIDExe = 40, "/usr/bin/script", 12, "/usr/bin/script"
		},
		"a shell parent": func(p *ProbedAgent) { p.PPID, p.PPIDExe, p.RefPPID, p.RefPPIDExe = 40, "/bin/bash", 12, "/bin/bash" },
		"the agent's child": func(p *ProbedAgent) {
			p.PPID, p.PPIDExe, p.RefPPID, p.RefPPIDExe = 40, p.AgentExe, 12, p.AgentExe
		},
	}
	for name, f := range refused {
		p := probed()
		f(&p)
		if err := CheckProbed(p); !errors.Is(err, errProbeRefused) {
			t.Errorf("%s: %v, want refused", name, err)
		}
	}
}

func TestBindFromProbe(t *testing.T) {
	tokens, reg := openTokens(t, t.TempDir())
	defer func() { _ = reg.Close() }()
	var logged []string
	tokens.SetLogger(func(f string, a ...any) { logged = append(logged, f) })
	if _, err := tokens.Issue("sb"); err != nil {
		t.Fatal(err)
	}
	bad := probed()
	bad.Exe = "/usr/bin/sleep"
	if err := tokens.BindFromProbe("sb", bad); err == nil {
		t.Error("a wrong executable is not bound")
	}
	if _, _, ok := tokens.MainPID("sb", 1); ok || len(logged) != 1 {
		t.Errorf("refused and logged: %v", logged)
	}
	if err := tokens.BindFromProbe("sb", probed()); err != nil {
		t.Fatal(err)
	}
	if pid, start, ok := tokens.MainPID("sb", 1); !ok || pid != 300 || start != "4711" {
		t.Errorf("bound = %d %q %v", pid, start, ok)
	}
	// agent.pid rewritten to name another process: the binding stays, logged.
	moved := probed()
	moved.PID = 301
	if err := tokens.BindFromProbe("sb", moved); err != nil {
		t.Fatal(err)
	}
	if pid, _, _ := tokens.MainPID("sb", 1); pid != 300 || len(logged) != 2 {
		t.Errorf("a stored binding is never replaced: %d, %v", pid, logged)
	}
	gone := probed()
	gone.Gen = 9
	if err := tokens.BindFromProbe("sb", gone); err == nil {
		t.Error("a generation with no token is not bound")
	}
	all, _ := reg.ListAgentTokens()
	if all["sb"].Tokens[0].MainPID != 300 || all["sb"].Tokens[0].MainStart != "4711" {
		t.Errorf("persisted = %+v", all["sb"].Tokens[0])
	}
}

// The registry launches each agent as a new generation, the token reaching
// only that spawn; a failed spawn retires its token at once.
func TestRegistrySpawnsWithAToken(t *testing.T) {
	tokens, reg := openTokens(t, t.TempDir())
	defer func() { _ = reg.Close() }()
	var got []Spawn
	fail := false
	r := NewSpawnRegistry(func(_ string, _ *pb.AgentSpec, sp Spawn) (Session, error) {
		got = append(got, sp)
		if fail {
			return nil, errors.New("no pty")
		}
		return newMemSession(), nil
	})
	if r.Generation("sb") != 0 {
		t.Error("no index: generation 0")
	}
	r.SetHookTokens(tokens)
	if _, err := r.Session("sb", &pb.AgentSpec{Kind: "shell"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Gen != 1 || got[0].Token == "" || r.Generation("sb") != 1 {
		t.Fatalf("spawn = %+v", got)
	}
	if _, ok := tokens.Verify("sb", "1", got[0].Token); !ok {
		t.Error("the spawned agent's token is accepted")
	}
	fail = true
	if _, err := r.Session("sb2", &pb.AgentSpec{Kind: "shell"}); err == nil {
		t.Fatal("factory error")
	}
	if _, ok := tokens.Verify("sb2", "1", got[1].Token); ok {
		t.Error("a spawn that failed retires its token")
	}
	r.RetireTokens("sb", false)
	if _, ok := tokens.Verify("sb", "1", got[0].Token); ok {
		t.Error("RetireTokens retires")
	}
	// A token index that cannot persist refuses the spawn: an agent without
	// a token would never count for steps or holds.
	bad, _ := LoadHookTokens(failingStore{})
	r.SetHookTokens(bad)
	if _, err := r.Session("sb3", &pb.AgentSpec{Kind: "shell"}); !errors.Is(err, errDisk) || r.Live("sb3") {
		t.Errorf("unpersisted token = %v", err)
	}
	NewRegistry(nil).RetireTokens("x", true) // no index: no-op
}

// The token reaches the sbx exec client by name, from its own environment:
// never argv, never the daemon's environment (research R13).
func TestAgentCommandPassesTheTokenByName(t *testing.T) {
	sp := Spawn{Gen: 7, Token: "deadbeefcafe"}
	c := agentCommand("sbx", Target{Ref: "sb-ref", Workdir: "/ws"}, &pb.AgentSpec{}, sp)
	want := []string{"sbx", "exec", "-it", "-e", "SXB_HOOK_TOKEN", "-e", "SXB_AGENT_GEN", "sb-ref", "bash", "-lc"}
	if len(c.Args) != len(want)+1 {
		t.Fatalf("argv = %v", c.Args)
	}
	for i, w := range want {
		if c.Args[i] != w {
			t.Errorf("argv[%d] = %q, want %q", i, c.Args[i], w)
		}
	}
	if strings.Contains(strings.Join(c.Args, " "), sp.Token) {
		t.Error("the token value must never be in argv")
	}
	env := strings.Join(c.Env, "\n")
	if !strings.Contains(env, "SXB_HOOK_TOKEN=deadbeefcafe") || !strings.Contains(env, "SXB_AGENT_GEN=7") {
		t.Error("the values are set in the exec client's environment")
	}
	if os.Getenv("SXB_HOOK_TOKEN") != "" {
		t.Error("never in the daemon's own environment (os.Setenv)")
	}
	// Without a token, no flags and the inherited environment.
	if c := agentCommand("sbx", Target{Ref: "r"}, &pb.AgentSpec{}, Spawn{}); c.Env != nil || strings.Contains(strings.Join(c.Args, " "), "-e ") {
		t.Errorf("tokenless argv = %v", c.Args)
	}
}
