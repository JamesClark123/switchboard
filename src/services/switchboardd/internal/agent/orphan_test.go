package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// --- the probe and reap scripts, run for real against /proc (T047) ---

// agentEnv is a fake sandbox for the scripts: a bin directory whose `claude`
// is a copy of this test binary (so /proc/<pid>/exe names it), and a HOME.
type agentEnv struct {
	bin, home, claude string
}

func newAgentEnv(t *testing.T) agentEnv {
	t.Helper()
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := agentEnv{bin: filepath.Join(dir, "bin"), home: filepath.Join(dir, "home")}
	e.claude = filepath.Join(e.bin, "claude")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.home, ".sxb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.claude, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

// start launches a fake agent as `claude <args…>` (mode: SXB_FAKE_AGENT).
func (e agentEnv) start(t *testing.T, mode string, args ...string) *exec.Cmd {
	t.Helper()
	c := exec.Command(e.claude, args...)
	c.Args[0] = "claude"
	c.Env = append(os.Environ(), "SXB_FAKE_AGENT="+mode)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	t.Cleanup(func() { _ = c.Process.Kill(); <-done })
	// Until it has exec'd, /proc/<pid>/exe still names the forking test.
	waitFor(t, func() bool { l, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", c.Process.Pid)); return l == e.claude })
	return c
}

// startTime is a process's start time (stat field 22).
func startTime(t *testing.T, pid int) string {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	return strings.Fields(s[strings.LastIndex(s, ")")+2:])[19]
}

func (e agentEnv) writePID(t *testing.T, line string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.home, ".sxb", "agent.pid"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run runs a script as the daemon's `sbx exec <ref> bash -lc` would (without
// the host's profile), in the fake sandbox, and parses its output.
func (e agentEnv) run(t *testing.T, script string) ProbeResult {
	t.Helper()
	r, err := e.try(script)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// try is run that returns the script's or the parse's error.
func (e agentEnv) try(script string) (ProbeResult, error) {
	c := exec.Command("/bin/sh", "-c", script)
	c.Env = append(os.Environ(), "PATH="+e.bin+":"+os.Getenv("PATH"), "HOME="+e.home)
	out, err := c.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		return ProbeResult{}, fmt.Errorf("script: %w (%q, stderr %q)", err, out, stderr)
	}
	return ParseProbe(string(out))
}

func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(b)
	return !strings.HasPrefix(s[strings.LastIndex(s, ")")+2:], "Z")
}

// The launch command creates ~/.sxb and records gen, PID and start time, kept
// by exec (research R14).
func TestLaunchRecordsTheAgentPID(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	home := t.TempDir()
	c := exec.Command("bash", "-c", pidRecord(7)+"exec sleep 30")
	c.Env = append(os.Environ(), "HOME="+home)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	file := filepath.Join(home, ".sxb", "agent.pid")
	waitFor(t, func() bool { b, _ := os.ReadFile(file); return strings.Count(string(b), " ") >= 3 })
	b, _ := os.ReadFile(file)
	f := strings.Fields(string(b))
	if f[0] != "7" || f[1] != strconv.Itoa(c.Process.Pid) || f[2] != startTime(t, c.Process.Pid) {
		t.Errorf("agent.pid = %q, want gen 7, pid %d, start %s", b, c.Process.Pid, startTime(t, c.Process.Pid))
	}
	cmd := agentCommand("sbx", Target{Ref: "r"}, &pb.AgentSpec{}, Spawn{Gen: 9, Token: "t"})
	if s := cmd.Args[len(cmd.Args)-1]; !strings.HasPrefix(s, pidRecord(9)) || !strings.Contains(s, "exec 'claude'") {
		t.Errorf("launch %q does not record the PID before exec", s)
	}
}

// The probe reports alive only for the recorded process with its recorded
// start time; a reused PID never matches. Its answer is what a first bind
// checks.
func TestProbeScriptRecordedAgent(t *testing.T) {
	e := newAgentEnv(t)
	a := e.start(t, "plain", agentFlag)
	pid := a.Process.Pid
	e.writePID(t, fmt.Sprintf("5 %d %s /dev/pts/0", pid, startTime(t, pid)))
	r := e.run(t, ProbeScript())
	if !r.File || r.FileGen != 5 || r.FilePID != pid || len(r.Procs) != 1 || r.Procs[0].PID != pid {
		t.Fatalf("probe = %+v", r)
	}
	if r.AgentExe != e.claude || r.Procs[0].Exe != e.claude {
		t.Errorf("exe = %q / %q, want %q", r.AgentExe, r.Procs[0].Exe, e.claude)
	}
	p, ok := r.Main()
	if !ok || p.Gen != 5 || p.PID != pid || len(p.Cmdline) != 2 || p.Cmdline[1] != agentFlag {
		t.Fatalf("main = %+v %v", p, ok)
	}
	// Started by the same parent as the probe shell (both this test), not
	// from a shell inside the sandbox: bindable.
	if err := CheckProbed(p); err != nil {
		t.Errorf("CheckProbed: %v", err)
	}

	// The same PID with another start time is a reused PID: not the file's
	// agent, but the scan still finds the live one.
	e.writePID(t, fmt.Sprintf("5 %d 1 /dev/pts/0", pid))
	if r := e.run(t, ProbeScript()); r.File || len(r.Procs) != 1 || r.Procs[0].PID != pid {
		t.Errorf("a reused PID read as %+v", r)
	}
	if _, ok := (ProbeResult{File: true}).Main(); ok {
		t.Error("no process, no main")
	}
}

// With no (or an unparseable) agent.pid the probe falls back to the agent
// binary and its fixed flags: both of two pre-feature agents, one launched
// with since-changed spec args, and never a nested `claude -p`.
func TestProbeScriptFallback(t *testing.T) {
	e := newAgentEnv(t)
	a := e.start(t, "nested", agentFlag)
	b := e.start(t, "plain", agentFlag, "--model", "opus")
	e.start(t, "plain", "--other") // not the fixed flags
	for _, file := range []string{"", "garbage line", "5 x 7"} {
		if file != "" {
			e.writePID(t, file)
		}
		var r ProbeResult
		// The nested child takes a moment to start.
		waitFor(t, func() bool {
			r = e.run(t, ProbeScript())
			return !r.File && len(r.Procs) == 2
		})
		got := map[int]bool{}
		for _, p := range r.Procs {
			got[p.PID] = true
		}
		if !got[a.Process.Pid] || !got[b.Process.Pid] {
			t.Errorf("file %q: fallback matched %+v, want %d and %d", file, r.Procs, a.Process.Pid, b.Process.Pid)
		}
		if _, ok := r.Main(); ok {
			t.Error("a fallback match is never bound")
		}
	}
}

// The reap sends TERM to each live matched process, then KILL to any that
// ignored it; a reused PID is never signalled.
func TestReapScript(t *testing.T) {
	e := newAgentEnv(t)
	plain := e.start(t, "plain", agentFlag)
	deaf := e.start(t, "deaf", agentFlag)
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	time.Sleep(100 * time.Millisecond) // the deaf one ignores TERM once running

	// A reused PID (start time differs) is never signalled; the scan stops
	// both agents, the deaf one by KILL.
	e.writePID(t, fmt.Sprintf("3 %d 1 /dev/pts/0", sleeper.Process.Pid))
	r := e.run(t, ReapScript(time.Second))
	if r.Reaped != 2 {
		t.Errorf("reaped %d, want 2", r.Reaped)
	}
	waitFor(t, func() bool { return !alive(plain.Process.Pid) && !alive(deaf.Process.Pid) })
	if !alive(sleeper.Process.Pid) {
		t.Error("a reused PID was signalled")
	}
	if ReapScript(0) == "" || !strings.Contains(ReapScript(3*time.Second), "-lt 3 ") {
		t.Error("the wait is in whole seconds, at least one")
	}
}

// A forged agent.pid naming a live process that is not the agent (the file is
// writable inside the sandbox), or a live agent of another kind, is ignored:
// the probe falls back to the binary scan, so it never reads that process as
// alive, and a reap never signals it (its command line lacks the fixed flag).
// With nothing found, the probe fails (closed): never "gone"; the reap fails
// too, with the remedy, rather than report a stop that left it running (SWI-17
// re-review: Replace would spawn a second agent beside it).
func TestForgedAgentPIDIsIgnored(t *testing.T) {
	e := newAgentEnv(t)
	other := e.start(t, "plain", "--other") // the agent binary, not the fixed flags
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	for _, c := range []*exec.Cmd{other, sleeper} {
		pid := c.Process.Pid
		e.writePID(t, fmt.Sprintf("5 %d %s /dev/pts/0", pid, startTime(t, pid)))
		if r, err := e.try(ProbeScript()); err == nil {
			t.Errorf("pid %d: forged record read as %+v", pid, r)
		}
		// The developer's Replace signals nothing and fails, naming the remedy.
		if r, err := e.try(ReapScript(time.Second)); err == nil || !strings.Contains(err.Error(), "restart the sandbox") || !alive(pid) {
			t.Errorf("pid %d: reap of a forged record: %+v, %v", pid, r, err)
		}
	}
	// The scan still finds a real agent beside the forged record.
	a := e.start(t, "plain", agentFlag)
	if r := e.run(t, ProbeScript()); r.File || len(r.Procs) != 1 || r.Procs[0].PID != a.Process.Pid {
		t.Errorf("forged record hid the agent: %+v", r)
	}
}

// An auto-update repoints claude at a sibling version (or replaces the file)
// while a running agent keeps the old executable: the agent is still alive,
// still bindable, and still reaped.
func TestProbeAfterAgentUpdate(t *testing.T) {
	e := newAgentEnv(t)
	data, err := os.ReadFile(e.claude)
	if err != nil {
		t.Fatal(err)
	}
	old, next := filepath.Join(e.bin, "2.1.311"), filepath.Join(e.bin, "2.1.312")
	for _, f := range []string{old, next} {
		if err := os.WriteFile(f, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	running := e
	running.claude = old
	a := running.start(t, "plain", agentFlag)
	pid := a.Process.Pid
	if err := os.Remove(e.claude); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, e.claude); err != nil {
		t.Fatal(err)
	}
	e.writePID(t, fmt.Sprintf("5 %d %s /dev/pts/0", pid, startTime(t, pid)))
	r := e.run(t, ProbeScript())
	if !r.File || !r.Alive() || r.AgentExe != next {
		t.Fatalf("an updated agent read as %+v", r)
	}
	if p, ok := r.Main(); !ok || CheckProbed(p) != nil {
		t.Errorf("an updated agent is not bindable: %+v %v", p, CheckProbed(p))
	}
	// The old version deleted from disk: still the agent.
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	r = e.run(t, ProbeScript())
	if !r.File || !r.Alive() {
		t.Fatalf("an agent whose binary was deleted read as %+v", r)
	}
	if p, ok := r.Main(); !ok || CheckProbed(p) != nil {
		t.Errorf("an agent whose binary was deleted is not bindable: %+v %v", p, CheckProbed(p))
	}
	if r := e.run(t, ReapScript(time.Second)); r.Reaped != 1 {
		t.Errorf("reaped %d, want 1", r.Reaped)
	}
	waitFor(t, func() bool { return !alive(pid) })
}

// The probe fails closed (SWI-17 review): with no resolvable claude it errs
// rather than reading "gone" beside a live agent.
func TestProbeFailsWithoutTheAgentBinary(t *testing.T) {
	e := newAgentEnv(t)
	a := e.start(t, "plain", agentFlag)
	e.writePID(t, fmt.Sprintf("5 %d %s /dev/pts/0", a.Process.Pid, startTime(t, a.Process.Pid)))
	if err := os.Remove(e.claude); err != nil {
		t.Fatal(err)
	}
	// Only the tools the script uses: no claude anywhere on PATH.
	tools := t.TempDir()
	for _, name := range []string{"readlink", "sed", "cut", "tr"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skip(err)
		}
		if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command("/bin/sh", "-c", ProbeScript())
	c.Env = append(os.Environ(), "PATH="+tools, "HOME="+e.home)
	if out, err := c.Output(); err == nil {
		t.Errorf("probe without claude = %q, want a failure", out)
	}
}

// With no claude binary at all, the probe fails with its own message (not a
// bare error), and the developer's reap still runs to its end: it stops the
// agent.pid process carrying the fixed flag and clears (SWI-17 re-review).
func TestReapWithoutTheAgentBinary(t *testing.T) {
	e := newAgentEnv(t)
	a := e.start(t, "plain", agentFlag)
	e.writePID(t, fmt.Sprintf("5 %d %s /dev/pts/0", a.Process.Pid, startTime(t, a.Process.Pid)))
	if err := os.Remove(e.claude); err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	for _, name := range []string{"readlink", "sed", "cut", "tr", "sleep", "kill"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skip(err)
		}
		if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	sh := func(script string) ([]byte, error) {
		c := exec.Command("/bin/sh", "-c", script)
		c.Env = append(os.Environ(), "PATH="+tools, "HOME="+e.home)
		return c.Output()
	}
	_, err := sh(ProbeScript())
	var ee *exec.ExitError
	if !errors.As(err, &ee) || !strings.Contains(string(ee.Stderr), "no claude binary in the sandbox") {
		t.Errorf("probe without claude = %v, want the no-binary failure", err)
	}
	out, err := sh(ReapScript(time.Second))
	if err != nil {
		t.Fatalf("reap without claude = %v (%q)", err, out)
	}
	if r, err := ParseProbe(string(out)); err != nil || r.Reaped != 1 {
		t.Errorf("reap = %+v, %v", r, err)
	}
	waitFor(t, func() bool { return !alive(a.Process.Pid) })
}

// An agent the binary check can't identify (an npm install: exe and argv0 are
// node) fails the probe closed, but the developer's reap stops it through
// agent.pid, since its command line carries the fixed flag (SWI-17 re-review).
func TestReapStopsAnUnidentifiedAgent(t *testing.T) {
	e := newAgentEnv(t)
	node := filepath.Join(t.TempDir(), "node")
	data, err := os.ReadFile(e.claude)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, data, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(node, filepath.Join(e.bin, "cli.js"), agentFlag)
	c.Args[0] = "node"
	c.Env = append(os.Environ(), "SXB_FAKE_AGENT=plain")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	t.Cleanup(func() { _ = c.Process.Kill(); <-done })
	pid := c.Process.Pid
	waitFor(t, func() bool { l, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); return l == node })
	e.writePID(t, fmt.Sprintf("5 %d %s /dev/pts/0", pid, startTime(t, pid)))
	if r, err := e.try(ProbeScript()); err == nil {
		t.Errorf("probe of an unidentified agent = %+v, want a failure", r)
	}
	if r := e.run(t, ReapScript(time.Second)); r.Reaped != 1 {
		t.Errorf("reap = %+v, want 1 signalled", r)
	}
	<-done
}

// A stale agent.pid (a dead PID, e.g. a failed write by a later generation)
// does not hide a live agent: the scan still runs (SWI-17 review).
func TestStaleAgentPIDStillScans(t *testing.T) {
	e := newAgentEnv(t)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	e.writePID(t, fmt.Sprintf("5 %d 12345 /dev/pts/0", dead.Process.Pid))
	if r := e.run(t, ProbeScript()); r.Alive() {
		t.Fatalf("no agent: %+v", r)
	}
	a := e.start(t, "plain", agentFlag)
	if r := e.run(t, ProbeScript()); r.File || len(r.Procs) != 1 || r.Procs[0].PID != a.Process.Pid {
		t.Errorf("a stale agent.pid hid the agent: %+v", r)
	}
}

// Tabs and line breaks in a command line cannot inject probe lines.
func TestProbeCmdlineInjection(t *testing.T) {
	e := newAgentEnv(t)
	a := e.start(t, "plain", agentFlag, "\nfile\t9\t1\t1\nref\t1\t/x")
	r := e.run(t, ProbeScript())
	if r.File || r.FileGen != 0 || r.RefPPIDExe == "/x" || len(r.Procs) != 1 || r.Procs[0].PID != a.Process.Pid {
		t.Errorf("injected: %+v", r)
	}
}

func TestParseProbe(t *testing.T) {
	r, err := ParseProbe("agent\t/c\nref\t1\t/x\nfile\t4\t10\t99\nproc\t10\t99\t1\t/c\t/x\tclaude\x1f--dangerously-skip-permissions\x1f\nreaped\t1\nnoise\n")
	if err != nil || r.FileGen != 4 || r.RefPPID != 1 || r.RefPPIDExe != "/x" || len(r.Procs) != 1 || len(r.Procs[0].Cmdline) != 2 || r.Reaped != 1 {
		t.Fatalf("parse = %+v, %v", r, err)
	}
	for _, bad := range []string{"", "agent\t/c\n", "file\t1\n", "agent\t/c\nnofile\nproc\t1\n", "agent\t\nnofile\n", "nofile\n"} {
		if _, err := ParseProbe(bad); err == nil {
			t.Errorf("ParseProbe(%q) accepted", bad)
		}
	}
	// A proc with no PID or start time is no process.
	if r, err := ParseProbe("agent\t/c\nnofile\nproc\t0\t\t0\t\t\t\n"); err != nil || r.Alive() {
		t.Errorf("parse = %+v, %v", r, err)
	}
}

// --- the gate, the drain and the client-exit observer (T047/T048) ---

type fakeGate struct {
	mu       sync.Mutex
	err      error
	admits   int
	released int
}

func (g *fakeGate) Admit(string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.admits++
	if g.err != nil {
		return nil, g.err
	}
	return func() { g.mu.Lock(); g.released++; g.mu.Unlock() }, nil
}

func TestSessionAsksTheGateFirst(t *testing.T) {
	spawned := 0
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { spawned++; return newMemSession(), nil })
	g := &fakeGate{err: ErrDetached}
	reg.SetOrphanGate(g)
	for _, want := range []error{ErrDetached, ErrChecking, ErrBusy} {
		g.err = want
		if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "bash"}); !errors.Is(err, want) {
			t.Errorf("Session = %v, want %v", err, want)
		}
		if err := reg.Prompt("sb", &pb.AgentSpec{Kind: "bash"}, "x"); !errors.Is(err, want) {
			t.Errorf("Prompt = %v, want %v", err, want)
		}
	}
	if spawned != 0 {
		t.Fatalf("spawned %d beside a refusing gate", spawned)
	}
	if !errors.Is(ErrDetached, ErrOrphan) || !errors.Is(ErrChecking, ErrOrphan) || errors.Is(ErrBusy, ErrOrphan) {
		t.Error("the orphan refusals match ErrOrphan, busy doesn't")
	}
	// A pass holds the gate until the session is registered; a live session
	// is returned without asking again.
	g.err = nil
	if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "bash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "bash"}); err != nil {
		t.Fatal(err)
	}
	if spawned != 1 || g.released != 1 || g.admits != 7 {
		t.Errorf("spawned %d, released %d, admits %d", spawned, g.released, g.admits)
	}
	if gen, live := reg.LiveGen("sb"); !live || gen != 0 {
		t.Errorf("LiveGen = %d %v", gen, live)
	}
	if _, live := reg.LiveGen("other"); live {
		t.Error("no session, not live")
	}

	// A failing factory releases the gate too.
	bad := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return nil, io.ErrClosedPipe })
	g2 := &fakeGate{}
	bad.SetOrphanGate(g2)
	if _, err := bad.Session("sb", &pb.AgentSpec{}); err == nil || g2.released != 1 {
		t.Errorf("err %v, released %d", err, g2.released)
	}
}

// The factory is called only from Session (guard, T048).
func TestFactoryCalledOnlyFromSession(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if n := strings.Count(string(b), "r.factory("); n != 0 && (f != "pty.go" || n != 1) {
			t.Errorf("%s calls the factory %d times; only Registry.session may", f, n)
		}
	}
}

// termSession is a waitSession that records a TERM and can ignore it.
type termSession struct {
	*waitSession
	mu     sync.Mutex
	terms  int
	closes int
	deaf   bool
}

func (s *termSession) Terminate() error {
	s.mu.Lock()
	s.terms++
	deaf := s.deaf
	s.mu.Unlock()
	if !deaf {
		s.exit()
	}
	return nil
}

func (s *termSession) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	s.exit()
	return s.waitSession.Close()
}

func TestCloseForStop(t *testing.T) {
	var sess *termSession
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) {
		sess = &termSession{waitSession: newWaitSession()}
		return sess, nil
	})
	rec := &recorder{}
	reg.SetStatusUpdater(rec)
	lost := 0
	reg.SetOnClientExit(func(string, int64) { lost++ })
	if done, finish := reg.CloseForStop("none"); done != nil {
		t.Fatal("no session, no done")
	} else {
		finish()
	}
	if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "bash"}); err != nil {
		t.Fatal(err)
	}
	done, finish := reg.CloseForStop("sb")
	select {
	case <-done: // the watcher's onExit closed it, outside the locks
	case <-time.After(2 * time.Second):
		t.Fatal("done never closed after TERM")
	}
	finish()
	if sess.terms != 1 || sess.closes != 1 || reg.Live("sb") || lost != 0 {
		t.Errorf("terms %d closes %d live %v lost %d", sess.terms, sess.closes, reg.Live("sb"), lost)
	}
	for _, st := range rec.snapshot() {
		if st == pb.AgentStatus_AGENT_STATUS_EXITED {
			t.Error("a drained session is never reported EXITED")
		}
	}

	// A client that ignores TERM: done stays open until finish kills it.
	if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "bash"}); err != nil {
		t.Fatal(err)
	}
	sess.deaf = true
	done, finish = reg.CloseForStop("sb")
	select {
	case <-done:
		t.Fatal("a deaf client exited on TERM")
	case <-time.After(50 * time.Millisecond):
	}
	finish()
	<-done

	// A session that can't be waited on is done at once.
	plain := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { return newMemSession(), nil })
	if _, err := plain.Session("sb", &pb.AgentSpec{Kind: "bash"}); err != nil {
		t.Fatal(err)
	}
	done, finish = plain.CloseForStop("sb")
	<-done
	finish()
}

// With a client-exit observer, an exec client exiting on its own is reported
// to it (the agent may have outlived it) instead of as EXITED.
func TestClientExitGoesToTheObserver(t *testing.T) {
	var sess *waitSession
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { sess = newWaitSession(); return sess, nil })
	rec := &recorder{}
	reg.SetStatusUpdater(rec)
	var mu sync.Mutex
	var got []string
	reg.SetOnClientExit(func(id string, gen int64) { mu.Lock(); got = append(got, fmt.Sprint(id, gen)); mu.Unlock() })
	if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "bash"}); err != nil {
		t.Fatal(err)
	}
	sess.exit()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	if got[0] != "sb0" || len(rec.snapshot()) != 0 {
		t.Errorf("observer got %v, statuses %v", got, rec.snapshot())
	}
}

// The observer is told before the session leaves the table, under the
// registry lock: a spawn asking the gate in between would find neither a
// session nor the observer's unknown mark and start a second agent.
func TestClientExitObserverRunsBeforeTheSessionGoes(t *testing.T) {
	var sess *waitSession
	reg := NewRegistry(func(string, *pb.AgentSpec) (Session, error) { sess = newWaitSession(); return sess, nil })
	reg.SetStatusUpdater(&recorder{})
	called := make(chan [2]bool, 1)
	reg.SetOnClientExit(func(id string, _ int64) {
		locked := !reg.mu.TryLock()
		if !locked {
			reg.mu.Unlock()
		}
		_, listed := reg.sessions[id]
		called <- [2]bool{locked, listed}
	})
	if _, err := reg.Session("sb", &pb.AgentSpec{Kind: "bash"}); err != nil {
		t.Fatal(err)
	}
	sess.exit()
	select {
	case got := <-called:
		if !got[0] || !got[1] {
			t.Errorf("observer ran with lock held %v, session listed %v; want both", got[0], got[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer never called")
	}
	waitFor(t, func() bool { return !reg.Live("sb") })
}

func TestPTYSessionTerminate(t *testing.T) {
	p := &ptySession{cmd: exec.Command("sleep", "30")}
	if err := p.Terminate(); err != nil {
		t.Errorf("no process: %v", err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := p.Terminate(); err != nil {
		t.Fatal(err)
	}
	err := p.cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Errorf("wait = %v, want SIGTERM", err)
	}
}

// A main-agent SessionEnd tells the orphan tracker (a detached agent that
// exits normally is re-probed at once); other events don't. It changes no
// status: an exiting agent is not WORKING.
func TestSessionEndObserver(t *testing.T) {
	st := &fakeStatus{}
	hs := NewHookServer(NewHub("h"), st)
	tokens, _ := openTokens(t, t.TempDir())
	hs.SetTokens(tokens)
	sp, err := tokens.Issue("sb1")
	if err != nil {
		t.Fatal(err)
	}
	tokens.BindMain("sb1", sp.Gen, 77)
	var ended []string
	hs.SetOnSessionEnd(func(id string) { ended = append(ended, id) })
	hs.handle(hookPayload{Event: "Stop", SandboxID: "sb1", Tokened: true, Gen: sp.Gen, PID: 77})
	calls := st.calls
	hs.handle(hookPayload{Event: "SessionEnd", SandboxID: "sb1", Tokened: true, Gen: sp.Gen, PID: 78}) // a nested session
	hs.handle(hookPayload{Event: "SessionEnd", SandboxID: "sb1", Tokened: true, Gen: sp.Gen, PID: 77})
	if len(ended) != 1 || ended[0] != "sb1" {
		t.Errorf("ended = %v", ended)
	}
	if st.calls != calls || st.last != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Errorf("SessionEnd wrote a status: %d writes, last %v", st.calls-calls, st.last)
	}
}
