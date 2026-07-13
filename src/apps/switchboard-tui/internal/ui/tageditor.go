package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// enterTag opens the tag editor for a sandbox (US5, FR-021). Unlike rename, a tag
// is mutable at any time and has no effect on the sandbox, so it works for running
// and stopped sandboxes alike.
func (m Model) enterTag(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	m.screen = screenTag
	m.tagID = sb.GetId()
	m.tagHost = host
	m.tagInput.SetValue(sb.GetTag())
	m.tagInput.CursorEnd()
	return m, m.tagInput.Focus()
}

func (m Model) tagHelp() helpBindings {
	return helpBindings{m.keys.Confirm, m.keys.Cancel}
}

func (m Model) updateTagKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.tagInput.Blur()
		m.screen = screenList
		return m, nil
	case tea.KeyEnter:
		id, tag := m.tagID, strings.TrimSpace(m.tagInput.Value())
		host := m.tagHost
		m.tagInput.Blur()
		m.screen = screenList
		m.busy[id] = "tagging"
		m.refreshListItems()
		return m, m.setTagCmd(m.daemonForHost(host), id, tag)
	}
	var cmd tea.Cmd
	m.tagInput, cmd = m.tagInput.Update(msg)
	return m, cmd
}

func (m Model) setTagCmd(d Daemon, id, tag string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := d.SetTag(ctx, id, tag); err != nil {
			return errMsg{err}
		}
		if tag == "" {
			return statusMsg("cleared tag on " + short(id))
		}
		return statusMsg("tagged " + short(id) + " as “" + tag + "”")
	}
}

func (m Model) viewTag() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("Tag sandbox ")+dimStyle.Render(short(m.tagID)),
		dimStyle.Render("A short label for its purpose — changeable anytime, empty to clear."),
		"",
		panelStyle.Width(m.bodyWidth()-2).Render(m.tagInput.View()),
	)
}
