//go:build e2e

// Command fakeclaude is the e2e suite's stand-in for Claude Code inside a stub
// sandbox (SWI-17). The daemon types run steps into the agent's terminal and
// reads them back from its screen and hooks, so the stub agent draws what
// those checks read (the transcript's `❯` echoes and `⎿` results above an
// input box between two rules, as in agent/testdata/claudeui) and reports the
// hooks Claude Code would:
//
//   - /model opus|sonnet|haiku and /effort <level> are confirmed as Claude
//     Code confirms them; /model fable answers "Model 'fable' not found", so a
//     test can halt a run at a step that is not accepted;
//   - /clear empties the transcript and reports SessionStart (source clear)
//     with a new session id;
//   - any other text is a prompt: UserPromptSubmit, then Stop a moment later
//     (a framed action prompt's turn, `[switchboard orchestrator …`, lasts a
//     second: its run then reads running until review).
//
// Every submitted text is appended to the log file, so a test can assert
// what a delivered run looked like. The terminal is raw (the stub sbx runs
// `stty raw -echo` first); the input is bracketed paste plus Enter.
//
// Usage: fakeclaude <session id> <workspace> <log file>
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
	ruleWidth  = 60 // narrower than any terminal the daemon or a client sizes
	actionTurn = time.Second
)

type agent struct {
	mu         sync.Mutex
	session    string
	clears     int
	workspace  string
	log        *os.File
	transcript []string
	input      string
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: fakeclaude <session id> <workspace> <log file>")
		os.Exit(2)
	}
	log, err := os.OpenFile(os.Args[3], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	a := &agent{session: os.Args[1], workspace: os.Args[2], log: log}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			a.mu.Lock()
			a.draw()
			a.mu.Unlock()
		}
	}()
	a.mu.Lock()
	a.draw()
	a.mu.Unlock()
	a.read(bufio.NewReader(os.Stdin))
}

// read consumes keystrokes: bracketed pastes and printable runes go to the
// input box, Enter (CR or the keypad's) submits it, backspace edits it.
func (a *agent) read(in *bufio.Reader) {
	var pending bytes.Buffer
	pasting := false
	for {
		b, err := in.ReadByte()
		if err != nil {
			return
		}
		pending.WriteByte(b)
		s := pending.String()
		switch {
		case strings.HasSuffix(s, pasteStart):
			pending.Reset()
			pasting = true
			continue
		case pasting && strings.HasSuffix(s, pasteEnd):
			a.add(strings.TrimSuffix(s, pasteEnd))
			pending.Reset()
			pasting = false
			continue
		case pasting:
			continue
		case s == "\x1b" || s == "\x1b[" || s == "\x1bO" || strings.HasPrefix(s, "\x1b[2") && len(s) < len(pasteStart):
			continue // an escape sequence still arriving
		case s == "\r" || s == "\n" || s == "\x1bOM":
			a.submit()
		case s == "\x7f" || s == "\b":
			a.mu.Lock()
			if r := []rune(a.input); len(r) > 0 {
				a.input = string(r[:len(r)-1])
			}
			a.draw()
			a.mu.Unlock()
		case strings.HasPrefix(s, "\x1b"):
			// another key's sequence: ignored
		default:
			a.add(s)
		}
		pending.Reset()
	}
}

func (a *agent) add(text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.input += text
	a.draw()
}

var modelNames = map[string]string{"opus": "Opus 5.5", "sonnet": "Sonnet 5.5", "haiku": "Haiku 5.5"}

// submit handles the box's text as Claude Code would (see the package doc).
func (a *agent) submit() {
	a.mu.Lock()
	text := strings.TrimSpace(a.input)
	a.input = ""
	if text == "" {
		a.draw()
		a.mu.Unlock()
		return
	}
	_, _ = fmt.Fprintln(a.log, text)
	first, _, _ := strings.Cut(text, "\n")
	echo := "❯ " + clip(first)
	fields := strings.Fields(text)
	var hooks []map[string]any
	var turn time.Duration
	switch {
	case fields[0] == "/model" && len(fields) == 2:
		a.transcript = append(a.transcript, echo)
		if name, ok := modelNames[fields[1]]; ok {
			a.transcript = append(a.transcript, "  ⎿  Set model to "+name+" and saved as your default for new sessions")
		} else {
			a.transcript = append(a.transcript, "  ⎿  Model '"+fields[1]+"' not found")
		}
	case fields[0] == "/effort" && len(fields) == 2:
		a.transcript = append(a.transcript, echo)
		if fields[1] == "auto" {
			a.transcript = append(a.transcript, "  ⎿  Effort level set to auto")
		} else {
			a.transcript = append(a.transcript, "  ⎿  Set effort level to "+fields[1]+" (saved as your default for new sessions)")
		}
	case text == "/clear":
		a.clears++
		a.session = fmt.Sprintf("%s-clear%d", strings.SplitN(a.session, "-clear", 2)[0], a.clears)
		a.transcript = []string{echo}
		hooks = append(hooks, map[string]any{"hook_event_name": "SessionStart", "source": "clear", "session_id": a.session})
	default:
		a.transcript = append(a.transcript, echo)
		hooks = append(hooks, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": a.session, "prompt": text})
		turn = 300 * time.Millisecond
		if strings.HasPrefix(text, "[switchboard orchestrator") {
			turn = actionTurn
		}
	}
	a.draw()
	a.mu.Unlock()
	for _, h := range hooks {
		a.hook(h)
	}
	if turn > 0 {
		time.Sleep(turn)
		a.mu.Lock()
		a.transcript = append(a.transcript, "⏺ Done.")
		a.draw()
		session := a.session
		a.mu.Unlock()
		a.hook(map[string]any{"hook_event_name": "Stop", "session_id": session})
	}
}

// draw repaints the whole screen: the transcript's last rows, then the input
// box (caller holds mu).
func (a *agent) draw() {
	rows := a.transcript
	if len(rows) > 20 {
		rows = rows[len(rows)-20:]
	}
	rule := strings.Repeat("─", ruleWidth)
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	b.WriteString(" fake Claude Code (switchboard e2e)\r\n\r\n")
	for _, r := range rows {
		b.WriteString(r + "\r\n")
	}
	b.WriteString("\r\n" + rule + "\r\n❯ " + clip(strings.ReplaceAll(a.input, "\n", " ")) + "\r\n" + rule + "\r\n")
	_, _ = os.Stdout.WriteString(b.String())
}

func clip(s string) string {
	if r := []rune(s); len(r) > ruleWidth-4 {
		return string(r[:ruleWidth-4])
	}
	return s
}

var hookURL = regexp.MustCompile(`'(http://[^']*)'`)

// hook POSTs a hook event to the URL the daemon injected for it, with the
// headers its injected command sends (the agent's token, generation and
// PID; this process keeps the stub's PID, the stub having exec'd it).
func (a *agent) hook(payload map[string]any) {
	event, _ := payload["hook_event_name"].(string)
	raw, err := os.ReadFile(a.workspace + "/.claude/settings.local.json")
	if err != nil {
		return
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &settings) != nil || len(settings.Hooks[event]) == 0 || len(settings.Hooks[event][0].Hooks) == 0 {
		return
	}
	m := hookURL.FindStringSubmatch(settings.Hooks[event][0].Hooks[0].Command)
	if m == nil {
		return
	}
	url := strings.Replace(m[1], "//host.docker.internal:", "//127.0.0.1:", 1)
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sxb-Token", os.Getenv("SXB_HOOK_TOKEN"))
	req.Header.Set("X-Sxb-Gen", os.Getenv("SXB_AGENT_GEN"))
	req.Header.Set("X-Sxb-Pid", fmt.Sprint(os.Getpid()))
	c := &http.Client{Timeout: 2 * time.Second}
	if resp, err := c.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}
