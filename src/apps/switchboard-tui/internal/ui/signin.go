package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
)

// Startup sign-in (feature 008, US5, FR-087..FR-090; research R7): when the
// automatic host sign-in setting is on, the TUI works through the saved remote
// hosts one at a time as it starts. Each host is first tried without a password
// (key/agent auth); only an authentication failure raises the centered masked
// prompt; any other failure shows a card with retry/skip. Nothing is persisted,
// a failed host never blocks another, and the local daemon is never in the queue.

// signInTimeout bounds one connection attempt (SC-007). A package constant.
const signInTimeout = 30 * time.Second

type signInPhase int

const (
	signInIdle signInPhase = iota
	signInAttempting
	signInPrompting
	signInFailed
)

type signInState struct {
	active  bool
	queue   []client.HostEntry
	current int
	phase   signInPhase
	reason  string // failure reason shown on the card
	// tallies for the end-of-sequence status line
	connected, skipped, failed int
}

// signInStartMsg kicks the sequence off after Init; signInResultMsg is one
// attempt's outcome.
type signInStartMsg struct{}

type signInResultMsg struct {
	id       string
	password bool // the attempt carried a password
	err      error
}

// signInKickoffCmd is batched into Init when the setting is on.
func signInKickoffCmd() tea.Cmd { return func() tea.Msg { return signInStartMsg{} } }

// startSignIn builds the queue — saved ssh hosts not already connected — and
// attempts the first.
func (m Model) startSignIn() (tea.Model, tea.Cmd) {
	if m.manager == nil || !m.prefs.AutoConnectHosts {
		return m, nil
	}
	if m.hostStore != nil {
		if known, err := m.hostStore.List(); err == nil {
			for _, h := range known {
				m.manager.Upsert(toEntry(h))
			}
		}
	}
	var queue []client.HostEntry
	for _, hc := range m.manager.List() {
		if hc.Entry.Kind == "ssh" && hc.State != client.HostConnected {
			queue = append(queue, hc.Entry)
		}
	}
	if len(queue) == 0 {
		return m, nil
	}
	m.signIn = signInState{active: true, queue: queue}
	return m.signInAttempt(queue[0].ID, "")
}

func (m Model) signInCurrent() (client.HostEntry, bool) {
	if !m.signIn.active || m.signIn.current >= len(m.signIn.queue) {
		return client.HostEntry{}, false
	}
	return m.signIn.queue[m.signIn.current], true
}

// signInAttempt dials one host (with or without a password) under the bound.
func (m Model) signInAttempt(id, password string) (tea.Model, tea.Cmd) {
	m.signIn.phase = signInAttempting
	m.signIn.reason = ""
	mgr := m.manager
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), signInTimeout)
		defer cancel()
		err := mgr.ConnectWithPassword(ctx, id, password)
		return signInResultMsg{id: id, password: password != "", err: err}
	}
}

func (m Model) applySignInResult(msg signInResultMsg) (tea.Model, tea.Cmd) {
	host, ok := m.signInCurrent()
	if !ok || host.ID != msg.id {
		return m, nil // stale result from a skipped host
	}
	if msg.err == nil {
		m.signIn.connected++
		return m.signInNext()
	}
	var de *client.DialError
	if errors.As(msg.err, &de) && de.Kind == client.DialAuthFailed && !msg.password {
		// Key/agent auth was refused: this host wants a password (FR-088).
		return m.enterHostPasswordFrom(host.ID, host.SSHTarget, pwSignIn, "key/agent authentication was refused — enter the password, or leave it blank to retry keys")
	}
	m.signIn.phase = signInFailed
	m.signIn.reason = msg.err.Error()
	return m, nil
}

// signInNext advances to the next host, or finishes the sequence.
func (m Model) signInNext() (tea.Model, tea.Cmd) {
	m.signIn.current++
	if host, ok := m.signInCurrent(); ok {
		return m.signInAttempt(host.ID, "")
	}
	st := m.signIn
	m.signIn = signInState{}
	m.status = fmt.Sprintf("signed in to %d of %d saved hosts", st.connected, len(st.queue))
	if st.failed+st.skipped > 0 {
		m.status += fmt.Sprintf(" (%d failed, %d skipped)", st.failed, st.skipped)
	}
	return m, m.hostsCmd()
}

// signInSkip moves on from the current host (esc on the prompt or the card).
func (m Model) signInSkip() (tea.Model, tea.Cmd) {
	m.signIn.skipped++
	return m.signInNext()
}

// signInModalActive reports whether the sequence owns the keyboard right now:
// the password prompt or the failure card is showing (research R7 — modal per
// prompt, never during an attempt).
func (m Model) signInModalActive() bool {
	if !m.signIn.active {
		return false
	}
	return m.signIn.phase == signInFailed || (m.hosts.connecting && m.hosts.pwMode == pwSignIn)
}

// updateSignInKey handles the failure card: r retries without a password, esc
// skips to the next host.
func (m Model) updateSignInKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "r":
		if host, ok := m.signInCurrent(); ok {
			return m.signInAttempt(host.ID, "")
		}
	case "esc", "s", "n", "q":
		m.signIn.failed++
		return m.signInNext()
	}
	return m, nil
}

// signInModal renders the failure card.
func (m Model) signInModal() string {
	host, _ := m.signInCurrent()
	lines := []string{
		sectionStyle.Render("Could not sign in to " + host.DisplayName),
		dimStyle.Render("ssh " + host.SSHTarget),
		"",
		statusErrStyle.Render(m.signIn.reason),
		"",
		dimStyle.Render(fmt.Sprintf("host %d of %d", m.signIn.current+1, len(m.signIn.queue))),
		"",
		helpStyle.Render("r retry · esc skip (you can connect it later from the hosts screen)"),
	}
	return modalStyle.Width(m.modalInnerWidth()).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}
