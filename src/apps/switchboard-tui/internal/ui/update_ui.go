package ui

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	upd "github.com/jamesclark123/switchboard/libs/switchboard-update"
)

// progress marks for the update summary + the list banner style.
var (
	okMark            = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("✔")
	failMark          = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render("✗")
	updateBannerStyle = lipgloss.NewStyle().Foreground(colAccentB).Bold(true)
)

// --- self-update state, messages, and injectable side effects ---

// hostUpdate is the outcome of updating one connected daemon.
type hostUpdate struct {
	name string
	err  error
}

// updateState backs the screenUpdate progress/summary view.
type updateState struct {
	running   bool
	finished  bool
	target    string
	results   []hostUpdate
	localVer  string
	localErr  error
	localBrew bool // local binary is Homebrew-managed → defer to `brew upgrade`
}

type updateAvailableMsg struct{ latest string }

type updateResultMsg struct {
	results   []hostUpdate
	localVer  string
	localErr  error
	localBrew bool
}

// applyLocalUpdate swaps the local sxb binary to target (empty = latest). It is
// a package var so tests can stub it (real work touches the network + disk). A
// Homebrew-managed binary is left untouched (skippedBrew=true).
var applyLocalUpdate = func(ctx context.Context, target, execPath string) (version string, skippedBrew bool, err error) {
	if upd.IsBrewManaged(execPath) {
		return "", true, nil
	}
	ver, bin, err := upd.FetchBinary(ctx, target, runtime.GOOS, runtime.GOARCH, "sxb")
	if err != nil {
		return "", false, err
	}
	if err := upd.ApplyToPath(bin, execPath); err != nil {
		return "", false, err
	}
	return ver, false, nil
}

// selfExecPath resolves the running client's executable path (stubbed in tests).
var selfExecPath = os.Executable

// checkUpdateCmd asks GitHub for the latest release on startup. It is
// offline-safe (any error is swallowed) and opt-out via SXB_NO_UPDATE_CHECK.
func checkUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		if os.Getenv("SXB_NO_UPDATE_CHECK") != "" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		rel, err := upd.LatestRelease(ctx)
		if err != nil || rel.Version == "" {
			return nil
		}
		return updateAvailableMsg{latest: rel.Version}
	}
}

// updateHint is the banner text shown when a newer release exists, or "" when
// the client is current (or versions are unknown).
func updateHint(latest, current string) string {
	if latest == "" || current == "" {
		return ""
	}
	if upd.SemverNewer(latest, current) {
		return fmt.Sprintf("⬆ update available: %s (you have %s) — press u to update the client and all hosts", latest, current)
	}
	return ""
}

// updateTarget is one daemon to update, with a display name.
type updateTarget struct {
	name string
	d    Daemon
}

// updateTargets returns every connected daemon to update: all connected hosts in
// the manager, or the single local daemon when there is no manager.
func (m Model) updateTargets() []updateTarget {
	if m.manager != nil {
		var ts []updateTarget
		for _, hc := range m.manager.List() {
			if hc.State == client.HostConnected && hc.Conn != nil {
				ts = append(ts, updateTarget{name: hc.Entry.DisplayName, d: hc.Conn})
			}
		}
		if len(ts) > 0 {
			return ts
		}
	}
	return []updateTarget{{name: "local", d: m.daemon}}
}

// runUpdateCmd updates every connected daemon to target, then swaps the local
// sxb binary. All hosts converge on the same version. Runs in tea's goroutine
// and returns a single terminal updateResultMsg.
func (m Model) runUpdateCmd(target string) tea.Cmd {
	targets := m.updateTargets()
	execPath, _ := selfExecPath()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		results := make([]hostUpdate, 0, len(targets))
		for _, t := range targets {
			results = append(results, hostUpdate{name: t.name, err: t.d.UpdateDaemon(ctx, target, nil)})
		}
		ver, brew, err := applyLocalUpdate(ctx, target, execPath)
		return updateResultMsg{results: results, localVer: ver, localErr: err, localBrew: brew}
	}
}

// --- screenUpdate rendering + keys ---

func (m Model) enterUpdate() (tea.Model, tea.Cmd) {
	target := m.latestVersion
	if target == "" || !upd.SemverNewer(target, m.clientVersion) {
		m.status = "sxb is up to date (" + m.clientVersion + ")"
		return m, nil
	}
	m.screen = screenUpdate
	m.update = updateState{running: true, target: target}
	return m, tea.Batch(m.runUpdateCmd(target), m.spinner.Tick)
}

func (m Model) applyUpdateResult(msg updateResultMsg) (tea.Model, tea.Cmd) {
	m.update.running = false
	m.update.finished = true
	m.update.results = msg.results
	m.update.localVer = msg.localVer
	m.update.localErr = msg.localErr
	m.update.localBrew = msg.localBrew
	// A successful client swap clears the update banner; a restart runs the new
	// binary. Skew/brew cases keep the banner so the nudge persists.
	if msg.localErr == nil && !msg.localBrew {
		m.updateBanner = ""
	}
	return m, nil
}

func (m Model) updateUpdateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.update.running {
		return m, nil // no input while the update is in flight
	}
	switch msg.String() {
	case "enter":
		// Restart into the freshly-installed client when the local swap succeeded.
		if m.update.finished && m.update.localErr == nil && !m.update.localBrew {
			m.reexec = true
			m.quitting = true
			return m, tea.Quit
		}
		m.screen = screenList
		return m, nil
	case "esc", "q":
		m.screen = screenList
		return m, nil
	}
	return m, nil
}

func (m Model) updateHelpBindings() helpBindings {
	if m.update.running {
		return helpBindings{}
	}
	if m.update.localErr == nil && !m.update.localBrew {
		return helpBindings{hkey("enter", "restart into new version"), hkey("esc", "later")}
	}
	return helpBindings{hkey("esc", "back")}
}

func (m Model) viewUpdate() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("Updating to " + m.update.target)
	b.WriteString(title + "\n\n")

	if m.update.running {
		b.WriteString(m.spinner.View() + dimStyle.Render(" updating connected hosts and the client…"))
		return b.String()
	}

	for _, r := range m.update.results {
		mark := okMark
		detail := "updated"
		if r.err != nil {
			mark = failMark
			detail = r.err.Error()
		}
		b.WriteString(fmt.Sprintf("%s host %s — %s\n", mark, r.name, detail))
	}

	b.WriteString("\n")
	switch {
	case m.update.localBrew:
		b.WriteString(failMark + " client is Homebrew-managed — run " + selectedStyle.Render("brew upgrade switchboard") + "\n")
	case m.update.localErr != nil:
		b.WriteString(failMark + " client update failed: " + m.update.localErr.Error() + "\n")
	default:
		b.WriteString(okMark + " client updated to " + m.update.localVer + "\n")
	}
	return b.String()
}
