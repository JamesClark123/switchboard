package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// TestCaptureFixtures regenerates testdata/claudeui from a local `claude`, the
// way research R1 probed it (SWI-17, T006): a real PTY at 140×45 with
// TERM=xterm-256color, every command typed as one bracketed paste and an Enter
// (as the daemon types prompts), the screen rendered through termview and
// written as a fixture. It is skipped unless SXB_CAPTURE_CLAUDE=1 and never runs
// in CI:
//
//	SXB_CAPTURE_CLAUDE=1 go test -run TestCaptureFixtures -v -timeout 10m ./internal/agent/
//
// It also measures R1's open items and logs them (record them in research.md
// R1): a real skill turn's UserPromptSubmit latency, whether the Notification
// hook's input carries notification_type (waits for the 60 s idle nudge), and
// whether $PPID in a hook command is the claude process that fired it (R13).
//
// `/model` and `/effort` save defaults in ~/.claude/settings.json, so the file
// is backed up first and restored afterwards (also on failure).
func TestCaptureFixtures(t *testing.T) {
	if os.Getenv("SXB_CAPTURE_CLAUDE") != "1" {
		t.Skip("set SXB_CAPTURE_CLAUDE=1 to capture the claudeui fixtures from a local claude")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("no claude on PATH: %v", err)
	}
	restore := backupUserSettings(t)
	defer restore()

	c := startCapture(t, bin)
	defer c.stop()

	// Startup: a one-off dialog first (captured when it is the auto-mode one;
	// none appeared on the 2026-10-28 capture, so that fixture is hand-authored
	// and must be kept when the others are regenerated), then the input box with
	// its faint placeholder.
	c.waitScreen("the input box or a startup dialog", 60*time.Second, func(s string) bool {
		return hasBox(s) || strings.Contains(s, "Enter to confirm")
	})
	if s := c.text(); !hasBox(s) {
		if strings.Contains(strings.ToLower(s), "auto mode") {
			c.save("auto-mode-dialog")
		} else {
			t.Logf("startup dialog (not saved):\n%s", s)
		}
		c.keys("\r")
	}
	c.waitHook("SessionStart", 60*time.Second)
	c.waitScreen("the input box", 30*time.Second, hasBox)
	time.Sleep(2 * time.Second) // let start-up notices finish drawing
	c.save("startup-placeholder")

	// The setting built-ins and their read-back lines.
	for _, m := range []struct{ name, cmd, want string }{
		{"model-set-opus", "/model opus", "Set model to"},
		{"model-set-fable", "/model fable", "Set model to"},
		{"model-set-sonnet", "/model sonnet", "Set model to"},
		{"model-set-haiku", "/model haiku", "Set model to"},
		{"model-not-found", "/model bogus", "not found"},
		{"effort-set-low", "/effort low", "Set effort level to low"},
		{"effort-set-medium", "/effort medium", "Set effort level to medium"},
		{"effort-set-high", "/effort high", "Set effort level to high"},
		{"effort-set-xhigh", "/effort xhigh", "Set effort level to xhigh"},
		{"effort-max-session", "/effort max", "Set effort level to max"},
		{"effort-auto", "/effort auto", "Effort level set to auto"},
		{"effort-invalid", "/effort bogus", "Invalid argument"},
	} {
		c.submit(m.cmd)
		c.waitScreen(m.want, 10*time.Second, func(s string) bool { return strings.Contains(s, m.want) })
		c.settle()
		c.save(m.name)
		if m.name == "effort-set-medium" {
			c.save("empty-after-command")
		}
	}

	// The bare /model picker, then Esc.
	c.submit("/model")
	c.waitScreen("the model picker", 10*time.Second, func(s string) bool { return strings.Contains(s, "Select model") })
	c.settle()
	c.save("model-picker")
	c.keys("\x1b")
	c.waitScreen("the input box after Esc", 10*time.Second, hasBox)

	// Typing a slash opens the command autocomplete (not a modal).
	for _, k := range "/mod" {
		c.keys(string(k))
		time.Sleep(50 * time.Millisecond)
	}
	c.settle()
	c.save("typing-slash-autocomplete")
	c.clearBox()

	// Text left in the box, unsubmitted.
	c.paste(fixtureTextLeft)
	c.settle()
	c.save("text-left-in-box")
	c.clearBox()

	// The framed action prompt as delivered (a multi-line paste), unsubmitted.
	c.paste(fixtureFramedPrompt)
	c.settle()
	c.save("framed-paste-in-box")
	c.clearBox()

	// A long one-line custom prompt that wraps over several rows.
	c.paste(fixtureLongPrompt)
	c.settle()
	c.save("long-prompt-wrapped")
	c.clearBox()

	// /clear: the redrawn banner (SessionStart source=clear).
	before := c.hookCount("SessionStart")
	c.submit("/clear")
	c.waitHookN("SessionStart", before+1, 10*time.Second)
	c.waitScreen("the post-clear box", 10*time.Second, hasBox)
	time.Sleep(time.Second)
	c.save("clear-banner")

	// R1 open items.
	c.measureSkillTurn()
	c.checkPPID()
	c.waitIdleNotification()
}

// hasBox reports whether a screen shows Claude Code's input box: a `❯` row
// right under a horizontal rule (an echoed command has no rule above it).
func hasBox(s string) bool {
	rows := strings.Split(s, "\n")
	for i := 1; i < len(rows); i++ {
		if strings.HasPrefix(rows[i], "❯") && strings.HasPrefix(rows[i-1], "─") {
			return true
		}
	}
	return false
}

// backupUserSettings copies ~/.claude/settings.json aside and returns the
// restore. A missing file is restored as missing.
func backupUserSettings(t *testing.T) func() {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	orig, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return func() {
		if existed {
			if err := os.WriteFile(path, orig, 0o600); err != nil {
				t.Errorf("restoring %s: %v", path, err)
			}
			return
		}
		_ = os.Remove(path)
	}
}

// capture drives one claude in a PTY.
type capture struct {
	t       *testing.T
	cmd     *exec.Cmd
	f       *os.File
	scr     *termview.Screen
	dir     string // the project dir (cwd) and hook logs
	hookLog string
	done    chan struct{}
	mu      sync.Mutex
	enterAt time.Time
}

const skillName = "sxb-ping"

func startCapture(t *testing.T, bin string) *capture {
	t.Helper()
	dir := t.TempDir()
	c := &capture{t: t, dir: dir, hookLog: filepath.Join(dir, "hooks.log"), done: make(chan struct{})}
	// A tiny project skill, for a real skill turn's UserPromptSubmit latency.
	skill := filepath.Join(dir, ".claude", "skills", skillName)
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: "+skillName+
		"\ndescription: Reply with the single word pong.\n---\n\nReply with the single word `pong` and nothing else. Do not use any tools.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Throwaway settings: every hook logs "<unix time> <event> <$PPID>" plus its
	// stdin JSON on the next line.
	hook := func(event string) []map[string]any {
		cmd := fmt.Sprintf(`{ printf '%%s %s %%s\n' "$(date +%%s.%%N)" "$PPID"; tr -d '\n'; echo; } >> %s`, event, c.hookLog)
		return []map[string]any{{"hooks": []map[string]any{{"type": "command", "command": cmd}}}}
	}
	hooks := map[string]any{}
	for _, ev := range []string{"UserPromptSubmit", "PreToolUse", "Notification", "Stop", "SessionStart", "SessionEnd"} {
		hooks[ev] = hook(ev)
	}
	settings, _ := json.Marshal(map[string]any{"hooks": hooks})
	settingsPath := filepath.Join(dir, "capture-settings.json")
	if err := os.WriteFile(settingsPath, settings, 0o644); err != nil {
		t.Fatal(err)
	}
	c.cmd = exec.Command(bin, "--dangerously-skip-permissions", "--settings", settingsPath)
	c.cmd.Dir = dir
	// A standalone agent, as a worker runs one: nothing inherited from a Claude
	// Code session running this test (CLAUDECODE, CLAUDE_EFFORT, …).
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE") && !strings.HasPrefix(kv, "TERM=") {
			c.cmd.Env = append(c.cmd.Env, kv)
		}
	}
	c.cmd.Env = append(c.cmd.Env, "TERM=xterm-256color")
	f, err := pty.StartWithSize(c.cmd, &pty.Winsize{Cols: 140, Rows: 45})
	if err != nil {
		t.Fatal(err)
	}
	c.f = f
	c.scr = termview.New(140, 45, termview.WithoutScrollback())
	go func() {
		defer close(c.done)
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				_, _ = c.scr.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *capture) stop() {
	_ = c.cmd.Process.Kill()
	_ = c.f.Close()
	<-c.done
	_ = c.cmd.Wait()
}

func (c *capture) text() string {
	var sb strings.Builder
	for _, l := range c.scr.Lines() {
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (c *capture) write(s string) {
	if _, err := c.f.Write([]byte(s)); err != nil {
		c.t.Fatal(err)
	}
}

func (c *capture) keys(s string) { c.write(s) }

func (c *capture) paste(s string) { c.write(pasteStart + s + pasteEnd) }

// submit pastes text and presses Enter after promptSubmitDelay, as Prompt does.
func (c *capture) submit(s string) {
	c.paste(s)
	time.Sleep(promptSubmitDelay)
	c.mu.Lock()
	c.enterAt = time.Now()
	c.mu.Unlock()
	c.write(submitKey)
}

// clearBox empties the input box with backspaces (never Ctrl+C: a second one
// on an empty box exits the agent), then Esc for any autocomplete left open.
func (c *capture) clearBox() {
	c.keys(strings.Repeat("\x7f", 600))
	time.Sleep(300 * time.Millisecond)
	c.keys("\x1b")
	time.Sleep(300 * time.Millisecond)
}

// settle lets a redraw finish before a capture.
func (c *capture) settle() { time.Sleep(400 * time.Millisecond) }

func (c *capture) save(name string) {
	saveFixture(c.t, name, c.scr.Lines())
	c.t.Logf("saved %s", name)
}

func (c *capture) waitScreen(what string, limit time.Duration, ok func(string) bool) {
	c.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok(c.text()) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	c.t.Fatalf("timed out waiting for %s; screen:\n%s\nhooks: %+v", what, c.text(), c.hooks())
}

// hookEvent is one logged hook: when it fired, its $PPID and its input.
type hookEvent struct {
	at    time.Time
	event string
	ppid  int
	input map[string]any
}

func (c *capture) hooks() []hookEvent {
	f, err := os.Open(c.hookLog)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []hookEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		head := strings.Fields(sc.Text())
		if len(head) != 3 {
			continue
		}
		secs, _ := strconv.ParseFloat(head[0], 64)
		ppid, _ := strconv.Atoi(head[2])
		ev := hookEvent{at: time.Unix(0, int64(secs*1e9)), event: head[1], ppid: ppid}
		if sc.Scan() {
			_ = json.Unmarshal([]byte(sc.Text()), &ev.input)
		}
		out = append(out, ev)
	}
	return out
}

func (c *capture) hookCount(event string) int {
	n := 0
	for _, h := range c.hooks() {
		if h.event == event {
			n++
		}
	}
	return n
}

func (c *capture) waitHook(event string, limit time.Duration) { c.waitHookN(event, 1, limit) }

func (c *capture) waitHookN(event string, n int, limit time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if c.hookCount(event) >= n {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	c.t.Fatalf("timed out waiting for %s hook #%d; screen:\n%s", event, n, c.text())
}

// measureSkillTurn runs the project skill as a real model turn and logs how
// long UserPromptSubmit took to fire after the Enter.
func (c *capture) measureSkillTurn() {
	before := c.hookCount("UserPromptSubmit")
	stops := c.hookCount("Stop")
	c.submit("/" + skillName)
	c.waitHookN("UserPromptSubmit", before+1, 30*time.Second)
	c.mu.Lock()
	enter := c.enterAt
	c.mu.Unlock()
	var at time.Time
	for _, h := range c.hooks() {
		if h.event == "UserPromptSubmit" {
			at = h.at
		}
	}
	c.t.Logf("R1: skill turn UserPromptSubmit latency = %v after Enter (submitWindow 2.5s)", at.Sub(enter).Round(time.Millisecond))
	c.waitHookN("Stop", stops+1, 120*time.Second)
}

// checkPPID logs whether a hook's $PPID is the claude process (R13), and if not
// which ancestor it is.
func (c *capture) checkPPID() {
	pid := c.cmd.Process.Pid
	for _, h := range c.hooks() {
		chain := ancestors(h.ppid)
		idx := -1
		for i, p := range chain {
			if p == pid {
				idx = i
				break
			}
		}
		c.t.Logf("R13: %s hook $PPID=%d claude pid=%d (claude is ancestor #%d of $PPID; 0 = $PPID itself) chain=%v",
			h.event, h.ppid, pid, idx, chain)
	}
}

// ancestors returns pid and its parents up to init.
func ancestors(pid int) []int {
	var out []int
	for pid > 1 && len(out) < 16 {
		out = append(out, pid)
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			break
		}
		// pid (comm) state ppid …; comm may contain spaces, so split after ")".
		rest := string(stat)[strings.LastIndexByte(string(stat), ')')+2:]
		f := strings.Fields(rest)
		if len(f) < 2 {
			break
		}
		pid, _ = strconv.Atoi(f[1])
	}
	return out
}

// waitIdleNotification waits for Claude Code's idle nudge (about 60 s after a
// turn) and logs whether the Notification input carries notification_type.
func (c *capture) waitIdleNotification() {
	deadline := time.Now().Add(100 * time.Second)
	for time.Now().Before(deadline) {
		for _, h := range c.hooks() {
			if h.event == "Notification" {
				nt, ok := h.input["notification_type"]
				c.t.Logf("R1: Notification input notification_type present=%v value=%v message=%v", ok, nt, h.input["message"])
				return
			}
		}
		time.Sleep(time.Second)
	}
	c.t.Logf("R1: no Notification hook within 100 s of the last turn")
}
