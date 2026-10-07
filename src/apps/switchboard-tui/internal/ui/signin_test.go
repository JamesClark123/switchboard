package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// signInModel wires a manager with a local daemon plus the given ssh hosts and a
// scripted dial: outcomes[id] is consulted with the password the attempt carried.
func signInModel(t *testing.T, outcomes map[string]func(password string) error, hosts ...client.HostEntry) Model {
	t.Helper()
	d := &fakeDaemon{}
	d.sandboxes = []*pb.Sandbox{{Id: "s1", DisplayName: "one", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}
	m := withSandboxes(sized(New(d, "/work")), d.sandboxes)
	mgr := client.NewManager()
	mgr.Upsert(client.HostEntry{ID: "local", DisplayName: "laptop", Kind: "local"})
	mgr.Adopt("local", nil)
	for _, h := range hosts {
		mgr.Upsert(h)
	}
	mgr.SetDialFunc(func(_ context.Context, e client.HostEntry) (*client.Conn, error) {
		if f, ok := outcomes[e.ID]; ok {
			return nil, f(e.SSHPassword)
		}
		return nil, nil
	})
	return m.WithHosts(mgr, nil, "local")
}

// runSignIn feeds the start message and runs attempt commands until the sequence
// pauses (prompt / card) or ends.
func runSignIn(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	for i := 0; i < 20 && msg != nil; i++ {
		var cmd tea.Cmd
		m, cmd = update(m, msg)
		if m.signInModalActive() || !m.signIn.active {
			return m
		}
		msg = runCmd(cmd)
	}
	return m
}

var authFailed = &client.DialError{Kind: client.DialAuthFailed, Err: errors.New("exit 255"), Stderr: "Permission denied (publickey,password)."}

func TestSignInKeyHostConnectsSilentlyPasswordHostPrompts(t *testing.T) {
	var pwSeen string
	m := signInModel(t, map[string]func(string) error{
		"key": func(string) error { return nil },
		"pw": func(p string) error {
			pwSeen = p
			if p == "" {
				return authFailed
			}
			return nil
		},
	}, client.HostEntry{ID: "key", DisplayName: "keyhost", Kind: "ssh", SSHTarget: "me@key"},
		client.HostEntry{ID: "pw", DisplayName: "pwhost", Kind: "ssh", SSHTarget: "me@pw"})

	m = runSignIn(t, m, signInStartMsg{})
	if !m.signIn.active || !m.hosts.connecting || m.hosts.pwMode != pwSignIn {
		t.Fatalf("expected the password prompt for pwhost; active %v connecting %v mode %v", m.signIn.active, m.hosts.connecting, m.hosts.pwMode)
	}
	if hc, _ := m.manager.Get("key"); hc.State != client.HostConnected {
		t.Error("the key host should have connected without a prompt")
	}
	v := m.View()
	if !strings.Contains(v, "Sign in to me@pw") || !strings.Contains(v, "refused") {
		t.Errorf("prompt should name the host and the reason; got:\n%s", v)
	}
	// Type the password; enter retries with it.
	for _, r := range "secret" {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = runSignIn(t, m, runCmd(cmd))
	if pwSeen != "secret" {
		t.Errorf("password passed to the dial = %q", pwSeen)
	}
	if m.signIn.active || !strings.Contains(m.status, "signed in to 2 of 2") {
		t.Errorf("sequence should end with both connected; active %v status %q", m.signIn.active, m.status)
	}
	if hc, _ := m.manager.Get("pw"); hc.State != client.HostConnected || hc.Entry.SSHPassword != "" {
		t.Errorf("pw host should be connected and the password never retained; %+v", hc.Entry)
	}
}

func TestSignInFailureCardRetryAndSkip(t *testing.T) {
	calls := 0
	m := signInModel(t, map[string]func(string) error{
		"down": func(string) error {
			calls++
			return &client.DialError{Kind: client.DialUnreachable, Err: errors.New("exit 255"), Stderr: "ssh: connect to host down port 22: Connection refused"}
		},
		"ok": func(string) error { return nil },
	}, client.HostEntry{ID: "down", DisplayName: "downhost", Kind: "ssh", SSHTarget: "me@down"},
		client.HostEntry{ID: "ok", DisplayName: "okhost", Kind: "ssh", SSHTarget: "me@ok"})

	m = runSignIn(t, m, signInStartMsg{})
	if m.signIn.phase != signInFailed || m.hosts.connecting {
		t.Fatalf("an unreachable host should show the failure card, not a prompt; phase %v", m.signIn.phase)
	}
	v := m.View()
	if !strings.Contains(v, "Could not sign in to downhost") || !strings.Contains(v, "Connection refused") || !strings.Contains(v, "host 1 of 2") {
		t.Errorf("card should name the host and reason; got:\n%s", v)
	}
	// r retries (fails again), esc skips; the next host still connects.
	m, cmd := update(m, press("r"))
	m = runSignIn(t, m, runCmd(cmd))
	if calls != 2 || m.signIn.phase != signInFailed {
		t.Errorf("retry should re-attempt; calls %d phase %v", calls, m.signIn.phase)
	}
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = runSignIn(t, m, runCmd(cmd))
	if m.signIn.active || !strings.Contains(m.status, "signed in to 1 of 2") || !strings.Contains(m.status, "1 failed") {
		t.Errorf("status = %q (active %v)", m.status, m.signIn.active)
	}
	if hc, _ := m.manager.Get("ok"); hc.State != client.HostConnected {
		t.Error("a failed host must not prevent the next from connecting")
	}
}

func TestSignInPromptEscSkipsAndListStaysUsable(t *testing.T) {
	m := signInModel(t, map[string]func(string) error{"pw": func(string) error { return authFailed }},
		client.HostEntry{ID: "pw", DisplayName: "pwhost", Kind: "ssh", SSHTarget: "me@pw"})
	m = runSignIn(t, m, signInStartMsg{})
	if !m.hosts.connecting {
		t.Fatal("expected the prompt")
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = runSignIn(t, m, runCmd(cmd))
	if m.signIn.active || m.hosts.connecting || !strings.Contains(m.status, "1 skipped") {
		t.Errorf("esc should skip and end; status %q", m.status)
	}
	// The TUI is usable afterwards (the launch wizard opens) and the skipped host
	// is still there to connect later.
	m, _ = update(m, press("n"))
	if m.screen != screenLaunch {
		t.Errorf("the launch wizard should open normally after the sequence, got %v", m.screen)
	}
	if hc, ok := m.manager.Get("pw"); !ok || hc.State == client.HostConnected {
		t.Error("the skipped host should remain known and disconnected")
	}
}

func TestSignInRespectsSettingAndSkipsConnectedAndLocal(t *testing.T) {
	attempts := 0
	m := signInModel(t, map[string]func(string) error{"a": func(string) error { attempts++; return nil }},
		client.HostEntry{ID: "a", DisplayName: "a", Kind: "ssh", SSHTarget: "me@a"})
	m.prefs.AutoConnectHosts = false
	m = runSignIn(t, m, signInStartMsg{})
	if attempts != 0 || m.signIn.active {
		t.Error("the sequence must not run when the setting is off")
	}
	m.prefs.AutoConnectHosts = true
	m.manager.Adopt("a", nil) // already connected: skipped
	m = runSignIn(t, m, signInStartMsg{})
	if attempts != 0 || m.signIn.active {
		t.Error("already-connected hosts (and the local daemon) are never attempted")
	}
}

func TestSignInIgnoresStaleResultsAndUnknownKeys(t *testing.T) {
	m := signInModel(t, map[string]func(string) error{"down": func(string) error { return errBoom{} }},
		client.HostEntry{ID: "down", DisplayName: "downhost", Kind: "ssh", SSHTarget: "me@down"})
	m = runSignIn(t, m, signInStartMsg{})
	if m.signIn.phase != signInFailed {
		t.Fatalf("phase = %v", m.signIn.phase)
	}
	m, _ = update(m, signInResultMsg{id: "someone-else", err: nil}) // stale: ignored
	if m.signIn.phase != signInFailed {
		t.Error("a result for another host must not advance the sequence")
	}
	m, _ = update(m, press("z")) // unknown key on the card: no-op
	if m.signIn.phase != signInFailed {
		t.Error("unknown keys on the card are ignored")
	}
	if !strings.Contains(m.View(), "Could not sign in") {
		t.Error("the card should stay up")
	}
}
