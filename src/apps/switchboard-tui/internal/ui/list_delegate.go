package ui

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// sandboxDelegate renders a sandbox row on THREE lines (feature 009, FR-149,
// research R13): title, description, and a third line naming the row's MCP
// servers and orchestrator (`mcp: a, b · orchestrator: review-bot`), truncated
// with `…` and never wrapped. Rows with neither leave the third line blank —
// exactly today's one-line gap, since Spacing() is 0 — so the visual rhythm of
// two-line rows is unchanged. Styles mirror the bubbles default delegate.
type sandboxDelegate struct {
	styles list.DefaultItemStyles
}

// sandboxItemThird is implemented by rows that carry a third line.
type sandboxItemThird interface {
	third() string
}

func newSandboxDelegate() sandboxDelegate {
	d := list.NewDefaultDelegate()
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.Foreground(colAccent).BorderForeground(colAccent)
	d.Styles.SelectedDesc = d.Styles.SelectedDesc.Foreground(colAccentB).BorderForeground(colAccent)
	d.Styles.NormalTitle = d.Styles.NormalTitle.Foreground(colText)
	d.Styles.NormalDesc = d.Styles.NormalDesc.Foreground(colMuted)
	d.Styles.DimmedTitle = d.Styles.DimmedTitle.Foreground(colMuted)
	d.Styles.FilterMatch = d.Styles.FilterMatch.Foreground(colWarn).Underline(true)
	return sandboxDelegate{styles: d.Styles}
}

func (d sandboxDelegate) Height() int                             { return 3 }
func (d sandboxDelegate) Spacing() int                            { return 0 }
func (d sandboxDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

// Render draws one row. The first two lines follow the default delegate; the
// third is the row's extra line in the description style.
func (d sandboxDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	var title, desc, third string
	if i, ok := item.(list.DefaultItem); ok {
		title, desc = i.Title(), i.Description()
	}
	if i, ok := item.(sandboxItemThird); ok {
		third = i.third()
	}
	width := m.Width() - d.styles.NormalTitle.GetPaddingLeft() - d.styles.NormalTitle.GetPaddingRight()
	if width <= 0 {
		return
	}
	cut := func(s string) string { return ansi.Truncate(s, width, "…") }
	title, desc, third = cut(title), cut(desc), cut(third)

	selected := index == m.Index()
	dimmed := m.FilterState() == list.Filtering && m.FilterValue() == ""
	titleStyle, descStyle := d.styles.NormalTitle, d.styles.NormalDesc
	switch {
	case dimmed:
		titleStyle, descStyle = d.styles.DimmedTitle, d.styles.DimmedDesc
	case selected:
		titleStyle, descStyle = d.styles.SelectedTitle, d.styles.SelectedDesc
	}
	_, _ = fmt.Fprintf(w, "%s\n%s\n%s", titleStyle.Render(title), descStyle.Render(desc), descStyle.Render(third))
}

// thirdLine renders a sandbox's third row line: `mcp: a, b · orchestrator:
// <name>`, either half omitted when absent, empty when both are (FR-149).
func thirdLine(sb *pb.Sandbox) string {
	var parts []string
	if names := sb.GetMcpServers(); len(names) > 0 {
		mode := ""
		if sb.GetMcpAttachMode() == pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
			mode = " (exclusive)"
		}
		parts = append(parts, "mcp: "+strings.Join(names, ", ")+mode)
	}
	// SWI-21 (FR-425): marked servers the launch skipped (being replaced, or not
	// attached at launch); the MCP overlay (M) lists them.
	if n := len(sb.GetMcpSkipped()); n > 0 {
		parts = append(parts, "mcp skipped: "+strconv.Itoa(n))
	}
	if c := sb.GetController(); c != nil {
		name := c.GetName()
		if name == "" {
			name = short(c.GetOrchestratorId())
		}
		role := "orchestrator: " + name
		if c.GetReasoning() {
			role += " (reasoning)"
		}
		parts = append(parts, role)
		// SWI-18 (FR-309b): lane text only once the orchestrator has sent it.
		switch {
		case c.GetReasoning() || !c.GetLanesKnown():
		case c.GetLanesPending():
			parts = append(parts, "lanes pending")
		case len(c.GetLaneNames()) > 0:
			parts = append(parts, "lanes: "+strings.Join(c.GetLaneNames(), ", "))
		default:
			parts = append(parts, "open lane")
		}
	}
	// Daemon-supplied names (FR-309).
	return sanitizeLine(strings.Join(parts, " · "), 0)
}

var _ = lipgloss.Left
