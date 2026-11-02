package agent

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// hookShells are the shells a hook command must work under: Claude Code runs
// hooks with dash in the sandbox image (SWI-27).
func hookShells(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, sh := range []string{"sh", "dash", "bash"} {
		if _, err := exec.LookPath(sh); err == nil {
			out = append(out, sh)
		}
	}
	if len(out) == 0 {
		t.Skip("no shell")
	}
	return out
}

// socketStdin returns a file for a hook's stdin that is a socket, as Claude Code
// gives it (a process cannot open one through /dev/fd), with body written to the
// other end, which is then closed.
func socketStdin(t *testing.T, body string) *os.File {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	in := os.NewFile(uintptr(fds[0]), "stdin")
	out := os.NewFile(uintptr(fds[1]), "writer")
	t.Cleanup(func() { _ = in.Close() })
	go func() {
		_, _ = io.WriteString(out, body)
		_ = out.Close()
	}()
	return in
}

// runHook runs an event's generated command the way Claude Code does.
func runHook(t *testing.T, shell, url, event, home, stdin string, env ...string) []byte {
	t.Helper()
	cmd := exec.Command(shell, "-c", BuildSettings("sb", url).Hooks[event][0].Hooks[0].Command)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "NO_PROXY=*"}, env...)
	cmd.Stdin = socketStdin(t, stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", shell, err, out)
	}
	return out
}

func homeWithRecord(t *testing.T, record string) string {
	t.Helper()
	home := t.TempDir()
	if record != "" {
		if err := os.MkdirAll(filepath.Join(home, ".sxb"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".sxb", "agent.pid"), []byte(record+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// The generated command authenticates through real curl, under each shell, with
// the hook input on a socket and the launch record and start time read.
func TestHookCommandSendsItsHeaders(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	type call struct {
		h    http.Header
		body string
	}
	calls := make(chan call, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls <- call{r.Header.Clone(), string(b)}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	self := os.Getpid() // the shell's $PPID
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", self))
	if err != nil {
		t.Skip("no /proc")
	}
	stat := string(raw)
	fields := strings.Fields(stat[strings.LastIndex(stat, ") ")+2:])
	wantStart, wantParent := fields[19], fields[1]

	for _, shell := range hookShells(t) {
		home := homeWithRecord(t, "3 319 442 /dev/pts/0")
		runHook(t, shell, srv.URL, "PreToolUse", home, `{"session_id":"s"}`, "SXB_HOOK_TOKEN=abc", "SXB_AGENT_GEN=3")
		c := <-calls
		for h, want := range map[string]string{
			headerToken: "abc", headerGen: "3", headerPID: strconv.Itoa(self),
			headerPIDStart: wantStart, headerParent: wantParent, headerRecord: "3 319 442",
		} {
			if got := c.h.Get(h); got != want {
				t.Errorf("%s: %s = %q, want %q", shell, h, got, want)
			}
		}
		if c.body != `{"session_id":"s"}` {
			t.Errorf("%s: body = %q", shell, c.body)
		}

		// No environment and no record: no empty header reaches the daemon,
		// which then counts it tokenless, as before.
		runHook(t, shell, srv.URL, "PreToolUse", t.TempDir(), `{}`)
		c = <-calls
		for _, h := range []string{headerToken, headerGen, headerRecord} {
			if _, sent := c.h[http.CanonicalHeaderKey(h)]; sent {
				t.Errorf("%s: %s sent with no value: %q", shell, h, c.h.Get(h))
			}
		}

		// A record forged to break out of the curl config is dropped, and the
		// hook still gets through.
		runHook(t, shell, srv.URL, "PreToolUse", homeWithRecord(t, `1" 2 3`), `{}`, "SXB_HOOK_TOKEN=abc", "SXB_AGENT_GEN=3")
		c = <-calls
		if c.h.Get(headerToken) != "abc" || c.h.Get(headerRecord) != "" {
			t.Errorf("%s: forged record = %v", shell, c.h)
		}
	}
}

// Acceptance (C2): no argv position ever holds the token, statically and in the
// argv curl actually receives.
func TestHookCommandKeepsTheTokenOutOfArgv(t *testing.T) {
	for event, ms := range BuildSettings("sb", "http://h/hook").Hooks {
		cmd := ms[0].Hooks[0].Command
		start := strings.Index(cmd, "3<<SXB")
		end := strings.LastIndex(cmd, "\nSXB\n")
		if start < 0 || end < start {
			t.Fatalf("%s: no here-document: %q", event, cmd)
		}
		outside := cmd[:start] + cmd[end:]
		if strings.Contains(outside, envHookToken) {
			t.Errorf("%s: the token is named outside the here-document: %q", event, outside)
		}
	}
	bin := t.TempDir()
	argv := filepath.Join(t.TempDir(), "argv")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argv + "\ncat /dev/fd/3 > " + argv + ".config\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shell := range hookShells(t) {
		cmd := exec.Command(shell, "-c", BuildSettings("sb", "http://h/hook").Hooks["Stop"][0].Hooks[0].Command)
		cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "SXB_HOOK_TOKEN=s3cret-token", "SXB_AGENT_GEN=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", shell, err, out)
		}
		got, err := os.ReadFile(argv)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(got), "s3cret-token") {
			t.Errorf("%s: token in curl's argv: %s", shell, got)
		}
		cfg, _ := os.ReadFile(argv + ".config")
		if !strings.Contains(string(cfg), `header = "X-Sxb-Token: s3cret-token"`) {
			t.Errorf("%s: token not in curl's config: %s", shell, cfg)
		}
	}
}

// countingBody counts what the server read of a request sent to it.
type countingBody struct {
	mu sync.Mutex
	n  int
	r  io.Reader
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.mu.Lock()
	c.n += n
	c.mu.Unlock()
	return n, err
}

// Acceptance (C3): a body over the cap is cut off without losing the fields, and
// the server closes the request rather than draining it.
func TestOversizedHookBodyIsCutOff(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s, sp := tokenServer(t, st)
	srv := httptest.NewServer(http.HandlerFunc(s.Handle))
	defer srv.Close()

	head := `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"`
	body := head + strings.Repeat("x", 8*maxHookRead) + `"}`
	cb := &countingBody{r: strings.NewReader(body)}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"?event=UserPromptSubmit&sandbox_id=sb", cb)
	req.ContentLength = int64(len(body))
	req.Header.Set(headerToken, sp.Token)
	req.Header.Set(headerGen, "1")
	req.Header.Set(headerPID, "100")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || !resp.Close {
		t.Errorf("status %d, close %v", resp.StatusCode, resp.Close)
	}
	cb.mu.Lock()
	n := cb.n
	cb.mu.Unlock()
	if n >= len(body) {
		t.Errorf("the whole %d-byte body was sent", n)
	}

	// The cap itself: a value past it hides what follows and nothing breaks.
	in := readHookInput(strings.NewReader(`{"session_id":"s","big":"` + strings.Repeat("y", 2*maxHookRead) + `","source":"late"}`))
	if in.SessionID != "s" || in.Source != "" || !in.Unread {
		t.Errorf("past the cap = %+v", in)
	}
	if in := readHookInput(strings.NewReader(`{"session_id":"s","hook_event_name":"PreToolUse","tool_input":{}}`)); !in.Unread {
		t.Error("stopping at the fields leaves the rest unread")
	}
	if in := readHookInput(strings.NewReader(`{"source":"clear"}`)); in.Unread {
		t.Error("a body read to its end has nothing unread")
	}
}

// A hook posting a big tool_input through real curl: the daemon answers without
// reading it all, and the hook still exits 0.
func TestHookCommandWithABigBody(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s, sp := tokenServer(t, st)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.Handle)}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_input":{"content":"` + strings.Repeat("x", 4*maxHookRead) + `"}}`
	runHook(t, "sh", "http://"+ln.Addr().String()+"/hook", "PreToolUse", t.TempDir(), body, "SXB_HOOK_TOKEN="+sp.Token, "SXB_AGENT_GEN=1")
	if got := st.agent("sb").GetStatus(); got != pb.AgentStatus_AGENT_STATUS_WORKING {
		t.Errorf("status = %v, want WORKING", got)
	}
}

// Acceptance (C3): a burst of PreToolUse hooks samples the screen at most once a
// second per sandbox; status-changing events always sample.
func TestHookBurstSamplesTheScreenOnce(t *testing.T) {
	run := &pb.WorkerRun{Id: "r"}
	st := newEvidenceStatus(
		&pb.Sandbox{Id: "sb", Controller: &pb.SandboxController{Run: run}},
		&pb.Sandbox{Id: "sb2", Controller: &pb.SandboxController{Run: run}},
	)
	s := NewHookServer(NewHub("h"), st)
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	screens := map[string]int{}
	s.SetScreen(func(id string) ([]termview.Line, bool, bool) {
		screens[id]++
		return nil, false, false
	})
	tokens, reg := openTokens(t, t.TempDir())
	defer func() { _ = reg.Close() }()
	if _, err := tokens.Issue("sb"); err != nil {
		t.Fatal(err)
	}
	tokens.BindMain("sb", 1, mainPID)
	s.SetTokens(tokens)
	pre := func(id string) { s.handle(hookPayload{Event: "PreToolUse", SandboxID: id}) }
	for range 50 {
		pre("sb")
	}
	if screens["sb"] != 1 {
		t.Errorf("a burst read the screen %d times, want 1", screens["sb"])
	}
	pre("sb2")
	if screens["sb2"] != 1 {
		t.Error("the limit is per sandbox")
	}
	for _, ev := range []string{"Stop", "UserPromptSubmit", "SessionStart", "Notification"} {
		before := screens["sb"]
		s.handle(m(hookPayload{Event: ev, SandboxID: "sb"}))
		if screens["sb"] != before+1 {
			t.Errorf("%s did not sample", ev)
		}
	}
	// The exemption is the main agent's: a caller naming a status event is not.
	before0 := screens["sb"]
	for _, p := range []hookPayload{
		{Event: "Notification", SandboxID: "sb"},
		m(hookPayload{Event: "Stop", SandboxID: "sb", PID: 7}),
	} {
		s.handle(p)
	}
	if screens["sb"] != before0 {
		t.Errorf("an event not the main agent's sampled %d times", screens["sb"]-before0)
	}
	before := screens["sb"]
	pre("sb")
	if screens["sb"] != before {
		t.Error("a status event restarts the gap")
	}
	now = now.Add(idleSampleGap)
	pre("sb")
	if screens["sb"] != before+1 {
		t.Error("past the gap a hook samples again")
	}
	// An id a hook named is not kept unless the sandbox exists: the query is
	// unchecked.
	pre("no-such-sandbox")
	if _, kept := s.sampled["no-such-sandbox"]; kept {
		t.Error("an unknown sandbox id was kept")
	}
}

// A sandbox's last sample is dropped once it no longer limits anything, so a
// removed sandbox leaves nothing behind.
func TestSampleAllPrunesOldSamples(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb"})
	s := NewHookServer(NewHub("h"), st)
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	s.sampled["sb"] = now
	s.sampleAll()
	if _, kept := s.sampled["sb"]; !kept {
		t.Error("a sample inside the gap still limits hooks")
	}
	now = now.Add(idleSampleGap)
	s.sampleAll()
	if _, kept := s.sampled["sb"]; kept {
		t.Error("a sample past the gap is dropped")
	}
}

// sampleIdle samples one sandbox as a hook past the gap would.
func sampleIdle(s *HookServer, sandboxID string) {
	if g, ok := s.status.(sandboxGetter); ok {
		if sb, err := g.Get(sandboxID); err == nil {
			s.sampleIdleOf(sb)
		}
	}
}

// A value a run's step reads does not wait out the gap: a PreToolUse right after
// a Stop set it unsets it at once.
func TestHookSamplesWhileInputIdleIsSet(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Controller: &pb.SandboxController{Run: &pb.WorkerRun{Id: "r"}},
		Agent:      &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE}})
	s := NewHookServer(NewHub("h"), st)
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	s.SetScreen(screenOf(loadFixture(t, "empty-after-command"), false, true))
	s.sampleAfterHook("sb", true, now)
	if st.agent("sb").GetInputIdleSince() == nil {
		t.Fatal("an idle box sets input_idle_since")
	}
	s.handle(hookPayload{Event: "PreToolUse", SandboxID: "sb"})
	if st.agent("sb").GetInputIdleSince() != nil {
		t.Error("a PreToolUse inside the gap must still unset it")
	}
}

// A Stop cut off before its background_tasks is not taken as the end of the
// turn, even when its own hook_event_name was cut off too: the query names it.
func TestCutOffStopCountsAsPending(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s := NewHookServer(NewHub("h"), st)
	body := `{"last_assistant_message":"` + strings.Repeat("m", 2*maxHookRead) + `","hook_event_name":"Stop","background_tasks":[]}`
	if code := post(s, "Stop", "sb", body, nil); code != http.StatusNoContent {
		t.Fatalf("code %d", code)
	}
	if got := st.agent("sb").GetStatus(); got != pb.AgentStatus_AGENT_STATUS_WAITING {
		t.Errorf("status = %v, want WAITING", got)
	}
	s.disarmBackstop("sb")
}

// `claude` on PATH may be a wrapper that forks, leaving the agent a direct child
// of the recorded process: it still binds on SessionStart, and counts as main.
// A process further below is logged, not bound.
func TestWrappedAgentBinds(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s, _ := tokenServer(t, st)
	var logged []string
	s.SetLogger(func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
	nested := recorded(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb"}, 200, "900")
	nested.PIDParent = 150
	s.handle(nested)
	if _, _, ok := s.tokens.MainPID("sb", 1); ok || len(logged) != 1 {
		t.Fatalf("not a child of the record: bound %v, logged %v", ok, logged)
	}
	ready := 0
	s.SetOnReady(func(string) { ready++ })
	child := recorded(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", SessionID: "s1"}, 101, "501")
	child.PIDParent = 100
	s.handle(child)
	if pid, _, _ := s.tokens.MainPID("sb", 1); pid != 101 || ready != 1 || st.agent("sb").GetSessionId() != "s1" {
		t.Errorf("wrapped main = %d, ready %d, %+v", pid, ready, st.agent("sb"))
	}
	turn := recorded(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb"}, 101, "501")
	turn.PIDParent = 100
	s.handle(turn)
	if st.agent("sb").GetLastSubmitAt() == nil {
		t.Error("the wrapped agent's events are the main agent's")
	}
	if _, start, _ := s.tokens.MainPID("sb", 1); start != "501" {
		t.Errorf("wrapped start = %q, want the hook's", start)
	}
	if s.tokens.IsMain("sb", 1, 101, "777") {
		t.Error("a reused PID is not the wrapped agent")
	}
}

// A shell can exec its last command, leaving a nested session a direct child of
// the agent. If it took the binding after a lost SessionStart, the agent's own
// next event takes it back, and that binding is final.
func TestRecordedHookCorrectsAWrappedBind(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s, _ := tokenServer(t, st)
	child := recorded(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb"}, 101, "501")
	child.PIDParent = 100
	s.handle(child)
	s.handle(recorded(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb"}, 100, "500"))
	if pid, start, _ := s.tokens.MainPID("sb", 1); pid != 100 || start != "500" {
		t.Fatalf("main = %d (%q), want the recorded process", pid, start)
	}
	if s.tokens.BindRecorded("sb", 1, 102, "600") {
		t.Error("a recorded binding is final")
	}
}

// recorded is a hook event as a generated command sends it: the launch record
// names main agent 100 of generation 1, started at 500.
func recorded(p hookPayload, pid int, start string) hookPayload {
	p.Tokened, p.Gen, p.PID, p.PIDStart = true, 1, pid, start
	p.Record = &launchRecord{gen: 1, pid: 100, start: "500"}
	return p
}

// Acceptance (C4): the main agent's SessionStart is lost and a nested `claude -p`
// sends one: it does not become main, and the main agent's next event binds it.
func TestLostSessionStartDoesNotBindANestedSession(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s, _ := tokenServer(t, st)
	ch, cancel := s.Tap("sb")
	defer cancel()
	ready := 0
	s.SetOnReady(func(string) { ready++ })

	s.handle(recorded(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", SessionID: "nested"}, 200, "900"))
	s.handle(recorded(hookPayload{Event: "Stop", SandboxID: "sb", SessionID: "nested"}, 200, "900"))
	if _, _, ok := s.tokens.MainPID("sb", 1); ok {
		t.Fatal("a nested session must not bind")
	}
	a := st.agent("sb")
	if a.GetSessionId() != "" || a.GetStatus() != pb.AgentStatus_AGENT_STATUS_UNSPECIFIED || len(ch) != 0 || ready != 0 {
		t.Errorf("a nested session counted: %+v (tap %d, ready %d)", a, len(ch), ready)
	}

	s.handle(recorded(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb", SessionID: "s1"}, 100, "500"))
	if pid, start, ok := s.tokens.MainPID("sb", 1); !ok || pid != 100 || start != "500" {
		t.Errorf("main = %d %q %v", pid, start, ok)
	}
	if st.agent("sb").GetLastSubmitAt() == nil || len(ch) != 1 {
		t.Error("the event that binds is already the main agent's")
	}
	s.handle(recorded(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", SessionID: "nested2"}, 200, "900"))
	if st.agent("sb").GetSessionId() == "nested2" {
		t.Error("a nested session stays nested after the bind")
	}
}

// A binding made with no record (an older hook command) that named a nested
// session is corrected, and a PID reused by another process is not main.
func TestRecordedBindCorrectsAndDetectsReuse(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s, _ := tokenServer(t, st)
	s.handle(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", Tokened: true, Gen: 1, PID: 200})
	if pid, _, _ := s.tokens.MainPID("sb", 1); pid != 200 {
		t.Fatalf("no record: the first SessionStart binds, got %d", pid)
	}
	s.handle(recorded(hookPayload{Event: "PreToolUse", SandboxID: "sb"}, 100, "500"))
	if pid, start, _ := s.tokens.MainPID("sb", 1); pid != 100 || start != "500" {
		t.Errorf("corrected = %d %q", pid, start)
	}
	if !s.tokens.IsMain("sb", 1, 100, "500") || !s.tokens.IsMain("sb", 1, 100, "") {
		t.Error("the main agent, its start time matching or unknown")
	}
	if s.tokens.IsMain("sb", 1, 100, "777") {
		t.Error("a reused PID is not the main agent")
	}
	// The reused PID's own hook: the record does not prove it, so it neither
	// rebinds nor counts, and changes no status.
	s.handle(hookPayload{Event: "UserPromptSubmit", SandboxID: "sb"}) // tokenless: WORKING
	s.handle(recorded(hookPayload{Event: "Stop", SandboxID: "sb"}, 100, "777"))
	if pid, start, _ := s.tokens.MainPID("sb", 1); pid != 100 || start != "500" {
		t.Errorf("after a reused PID's hook: %d %q", pid, start)
	}
	if got := st.agent("sb").GetStatus(); got != pb.AgentStatus_AGENT_STATUS_WORKING {
		t.Errorf("a reused PID drove the status: %v", got)
	}
	// A record of another generation proves nothing, and nor does one that names
	// the process in a generation no longer current.
	old := recorded(hookPayload{Event: "Stop", SandboxID: "sb"}, 300, "500")
	old.Record = &launchRecord{gen: 9, pid: 300, start: "500"}
	s.handle(old)
	if pid, _, _ := s.tokens.MainPID("sb", 1); pid != 100 {
		t.Error("a record of another generation binds nothing")
	}
	if _, err := s.tokens.Issue("sb"); err != nil {
		t.Fatal(err)
	}
	if s.tokens.BindRecorded("sb", 1, 300, "1") {
		t.Error("only the current generation binds")
	}
	if !s.tokens.BindRecorded("sb", 2, 400, "1") || s.tokens.BindRecorded("sb", 2, 401, "2") || s.tokens.BindRecorded("sb", 2, 400, "3") {
		t.Error("a binding with a start time is final")
	}
	if pid, start, _ := s.tokens.MainPID("sb", 2); pid != 400 || start != "1" {
		t.Errorf("final = %d %q", pid, start)
	}
	if _, err := s.tokens.Issue("sb"); err != nil {
		t.Fatal(err)
	}
	if s.tokens.BindRecorded("sb", 3, 0, "1") || s.tokens.BindRecorded("sb", 3, 5, "") {
		t.Error("only a real PID, with a start time, binds")
	}
}

func TestParseLaunchRecord(t *testing.T) {
	if r := parseLaunchRecord(" 3  319 442 "); r == nil || r.gen != 3 || r.pid != 319 || r.start != "442" {
		t.Errorf("record = %+v", r)
	}
	for _, v := range []string{"", "   ", "3 319", "x 319 442", "3 y 442", "3 319 442 extra", "3 319 z"} {
		if parseLaunchRecord(v) != nil {
			t.Errorf("%q parsed", v)
		}
	}
}

// A probe fills in the start time a hook with no record could not give, and
// never replaces the binding.
func TestBindFromProbeFillsTheStartTime(t *testing.T) {
	tokens, reg := openTokens(t, t.TempDir())
	defer func() { _ = reg.Close() }()
	if _, err := tokens.Issue("sb"); err != nil {
		t.Fatal(err)
	}
	tokens.BindMain("sb", 1, 300)
	if err := tokens.BindFromProbe("sb", probed()); err != nil {
		t.Fatal(err)
	}
	if pid, start, _ := tokens.MainPID("sb", 1); pid != 300 || start != "4711" {
		t.Errorf("main = %d %q", pid, start)
	}
	all, _ := reg.ListAgentTokens()
	if all["sb"].Tokens[0].MainStart != "4711" {
		t.Errorf("persisted = %+v", all["sb"].Tokens[0])
	}
}

// A probe's output, end to end through CheckProbed: the observed Docker Sandboxes shape still binds.
func TestProbeShapes(t *testing.T) {
	sandbox := probed() // PPID 0 and reference PPID 0
	if err := CheckProbed(sandbox); err != nil {
		t.Errorf("the Docker Sandboxes shape: %v", err)
	}
	r, err := ParseProbe("agent\t/opt/claude/bin/claude\nref\t1\t/usr/bin/tini\nfile\t1\t300\t4711\n" +
		"proc\t300\t4711\t1\t/opt/claude/bin/claude\t/usr/bin/tini\tclaude\x1f--dangerously-skip-permissions\x1f\n")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := r.Main()
	if !ok || p.RefPPID != 1 {
		t.Fatalf("main = %+v", p)
	}
	if err := CheckProbed(p); !errors.Is(err, errProbeRefused) {
		t.Errorf("a probe under PID 1 = %v, want refused", err)
	}
}

// A launch whose write failed leaves the previous generation's record: it says
// nothing about this agent, which still binds on its SessionStart and is not
// taken for a nested session.
func TestStaleRecordFallsBackToSessionStart(t *testing.T) {
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s, _ := tokenServer(t, st)
	if _, err := s.tokens.Issue("sb"); err != nil {
		t.Fatal(err)
	}
	ready := 0
	s.SetOnReady(func(string) { ready++ })
	stale := &launchRecord{gen: 1, pid: 50, start: "5"}
	s.handle(hookPayload{Event: "PreToolUse", SandboxID: "sb", Tokened: true, Gen: 2, PID: 100, Record: stale})
	if got := st.agent("sb").GetStatus(); got != pb.AgentStatus_AGENT_STATUS_WORKING {
		t.Errorf("status = %v: taken for a nested session", got)
	}
	s.handle(hookPayload{Event: "SessionStart", Source: "startup", SandboxID: "sb", SessionID: "s1", Tokened: true, Gen: 2, PID: 100, Record: stale})
	if pid, _, ok := s.tokens.MainPID("sb", 2); !ok || pid != 100 || ready != 1 {
		t.Errorf("main = %d %v, ready %d", pid, ok, ready)
	}
}

// The real generated command against the real server: the record binds the
// process that fired the hook (this test, the shell's parent) and no other.
func TestHookCommandBindsThroughTheServer(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", os.Getpid()))
	if err != nil {
		t.Skip("no /proc")
	}
	stat := string(raw)
	start := strings.Fields(stat[strings.LastIndex(stat, ") ")+2:])[19]
	st := newEvidenceStatus(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING})
	s, sp := tokenServer(t, st)
	srv := httptest.NewServer(http.HandlerFunc(s.Handle))
	defer srv.Close()
	env := []string{"SXB_HOOK_TOKEN=" + sp.Token, "SXB_AGENT_GEN=1"}
	body := `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`

	nested := homeWithRecord(t, fmt.Sprintf("1 %d %s /dev/pts/0", os.Getpid()+1, start))
	runHook(t, "sh", srv.URL, "SessionStart", nested, body, env...)
	if _, _, ok := s.tokens.MainPID("sb", 1); ok {
		t.Fatal("a process the record does not name bound")
	}
	main := homeWithRecord(t, fmt.Sprintf("1 %d %s /dev/pts/0", os.Getpid(), start))
	runHook(t, "sh", srv.URL, "SessionStart", main, body, env...)
	if pid, got, ok := s.tokens.MainPID("sb", 1); !ok || pid != os.Getpid() || got != start {
		t.Errorf("main = %d %q %v", pid, got, ok)
	}
	if st.agent("sb").GetSessionId() != "s1" {
		t.Error("the binding event is the main agent's")
	}

	// A FIFO at the record's path never blocks the hook.
	fifo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fifo, ".sxb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(fifo, ".sxb", "agent.pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHook(t, "sh", srv.URL, "PreToolUse", fifo, `{}`, env...)
}
