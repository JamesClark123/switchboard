package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// The settings screen (feature 008, FR-101) lists every TUI-persisted setting
// with its value and toggles it in place, saving immediately. Reachable with `,`
// from either top-level screen; esc returns to the screen it was opened from.
// The first (and, in this feature, only) entry is automatic host sign-in.

type settingsState struct {
	cursor   int
	returnTo screen
	notice   string // "settings could not be read — defaults apply" (FR-101)
}

// settingItem is one row; the toggle writes back through set.
type settingItem struct {
	key, label, desc string
	get              func(store.Settings) bool
	set              func(*store.Settings, bool)
}

var settingItems = []settingItem{{
	key:   "auto_connect_hosts",
	label: "Automatic host sign-in at startup",
	desc:  "Try every saved remote host when the TUI starts; prompt for a password where one is needed (next start)",
	get:   func(s store.Settings) bool { return s.AutoConnectHosts },
	set:   func(s *store.Settings, v bool) { s.AutoConnectHosts = v },
}}

// WithSettings attaches the settings store and loads the persisted preferences.
// An unreadable file leaves the defaults in force and records a notice.
func (m Model) WithSettings(ss *store.SettingsStore) Model {
	m.settingsStore = ss
	m.prefs = store.DefaultSettings()
	if ss == nil {
		return m
	}
	prefs, err := ss.Load()
	m.prefs = prefs
	if err != nil {
		m.settingsView.notice = "settings could not be read (" + err.Error() + ") — defaults apply until you change one"
	}
	return m
}

func (m Model) enterSettings(returnTo screen) (tea.Model, tea.Cmd) {
	m.settingsView.returnTo = returnTo
	m.settingsView.cursor = 0
	m.screen = screenSettings
	return m, nil
}

func (m Model) settingsHelp() helpBindings {
	return helpBindings{hkey("space/enter", "toggle"), hkey("↑/↓", "move"), hkey("esc", "back")}
}

func (m Model) updateSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", ",":
		m.screen = m.settingsView.returnTo
		return m, nil
	case "up", "k":
		if m.settingsView.cursor > 0 {
			m.settingsView.cursor--
		}
	case "down", "j":
		if m.settingsView.cursor < len(settingItems)-1 {
			m.settingsView.cursor++
		}
	case " ", "enter":
		it := settingItems[m.settingsView.cursor]
		it.set(&m.prefs, !it.get(m.prefs))
		if m.settingsStore == nil {
			m.status = "settings store unavailable — change applies to this session only"
			return m, nil
		}
		if err := m.settingsStore.Save(m.prefs); err != nil {
			m.status = "error: could not save settings: " + err.Error()
			return m, nil
		}
		m.settingsView.notice = ""
		m.status = "saved settings"
	}
	return m, nil
}

func (m Model) viewSettings() string {
	rows := []string{sectionStyle.Render("Settings"), dimStyle.Render("persisted by the TUI in settings.toml under the config dir"), ""}
	if n := m.settingsView.notice; n != "" {
		rows = append(rows, statusErrStyle.Render("⚠ "+n), "")
	}
	for i, it := range settingItems {
		cursor := "  "
		if i == m.settingsView.cursor {
			cursor = cursorBarStyle.Render("> ")
		}
		state := dimStyle.Render("[off]")
		if it.get(m.prefs) {
			state = selectedStyle.Render("[on] ")
		}
		rows = append(rows, cursor+state+" "+it.label, "       "+dimStyle.Render(it.desc))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}
