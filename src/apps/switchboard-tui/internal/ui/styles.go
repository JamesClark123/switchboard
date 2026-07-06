package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	swb "github.com/jamesclark123/switchboard/libs/switchboard-proto"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Palette — a small, cohesive set of adaptive colors used across every screen so
// the TUI reads as one product rather than a pile of ad-hoc styles.
var (
	colAccent  = lipgloss.AdaptiveColor{Light: "#7D56F4", Dark: "#9A7DFF"}
	colAccentB = lipgloss.AdaptiveColor{Light: "#5B3FD6", Dark: "#7D56F4"}
	colText    = lipgloss.AdaptiveColor{Light: "#1A1A2E", Dark: "#E6E6FA"}
	colMuted   = lipgloss.AdaptiveColor{Light: "#6C6C89", Dark: "#8A8AA3"}
	colSubtle  = lipgloss.AdaptiveColor{Light: "#D9D9E3", Dark: "#3A3A4A"}
	colRunning = lipgloss.AdaptiveColor{Light: "#1F9D55", Dark: "#43BF6D"}
	colStopped = lipgloss.AdaptiveColor{Light: "#8A8AA3", Dark: "#6C6C89"}
	colWarn    = lipgloss.AdaptiveColor{Light: "#B7791F", Dark: "#ECC94B"}
	colError   = lipgloss.AdaptiveColor{Light: "#C53030", Dark: "#F56565"}
	colOnAccnt = lipgloss.Color("#FFFFFF")
)

// Shared styles. Kept package-level so both the hand-rolled screens and the
// bubbles/huh components can be themed from one place.
var (
	appTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colOnAccnt).
			Background(colAccent).
			Padding(0, 1)

	hostBadgeStyle = lipgloss.NewStyle().
			Foreground(colText).
			Background(colSubtle).
			Padding(0, 1)

	unreadBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colOnAccnt).
				Background(colError).
				Padding(0, 1)

	headerBarStyle = lipgloss.NewStyle().MarginBottom(1)

	sectionStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colAccent)

	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	dimStyle      = lipgloss.NewStyle().Foreground(colMuted)
	helpStyle     = lipgloss.NewStyle().Foreground(colMuted)

	statusOKStyle  = lipgloss.NewStyle().Foreground(colRunning)
	statusErrStyle = lipgloss.NewStyle().Foreground(colError)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colSubtle).
			Padding(0, 1)

	cursorBarStyle = lipgloss.NewStyle().Foreground(colAccent).Bold(true)

	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colOnAccnt).
			Background(colAccent).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(colMuted).
				Background(colSubtle).
				Padding(0, 2)

	tabGapStyle = lipgloss.NewStyle().Foreground(colSubtle)

	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent).
			Padding(1, 2)
)

// stateBadge renders a colored, fixed-width state chip for a sandbox.
func stateBadge(st pb.SandboxState) string {
	label := strings.ToUpper(swb.SandboxStateLabel(st))
	var fg lipgloss.TerminalColor = colMuted
	switch st {
	case pb.SandboxState_SANDBOX_STATE_RUNNING:
		fg = colRunning
	case pb.SandboxState_SANDBOX_STATE_STOPPED:
		fg = colStopped
	case pb.SandboxState_SANDBOX_STATE_ERROR:
		fg = colError
	}
	return lipgloss.NewStyle().Foreground(fg).Bold(true).Render(pad(label, 8))
}

// huhTheme adapts the shared palette into a huh.Theme so forms match the rest of
// the TUI instead of using huh's stock look.
func huhTheme() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Base = t.Focused.Base.BorderForeground(colAccent)
	t.Focused.Title = t.Focused.Title.Foreground(colAccent).Bold(true)
	t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(colAccent).Bold(true)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(colAccent)
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(colAccent)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(colAccent)
	t.Focused.SelectedPrefix = t.Focused.SelectedPrefix.Foreground(colRunning)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Background(colAccent).Foreground(colOnAccnt)
	t.Focused.Description = t.Focused.Description.Foreground(colMuted)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(colAccent)
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(colAccent)
	t.Blurred = t.Focused
	t.Blurred.Base = t.Blurred.Base.BorderForeground(colSubtle)
	t.Blurred.Title = t.Blurred.Title.Foreground(colMuted)
	return t
}

// newHelp returns a help.Model themed to the palette.
func newHelp() help.Model {
	h := help.New()
	h.Styles.ShortKey = h.Styles.ShortKey.Foreground(colText)
	h.Styles.ShortDesc = h.Styles.ShortDesc.Foreground(colMuted)
	h.Styles.ShortSeparator = h.Styles.ShortSeparator.Foreground(colSubtle)
	h.Styles.FullKey = h.Styles.FullKey.Foreground(colText)
	h.Styles.FullDesc = h.Styles.FullDesc.Foreground(colMuted)
	return h
}
