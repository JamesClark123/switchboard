package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap collects every binding the TUI reacts to. Each screen exposes the subset
// relevant to it for the help bar via the help* helpers below.
type keyMap struct {
	Up, Down    key.Binding
	Enter, Back key.Binding
	Quit        key.Binding
	Refresh     key.Binding
	Launch      key.Binding
	NewConfig   key.Binding
	FromConfig  key.Binding
	Hosts       key.Binding
	Groups      key.Binding
	VSCode      key.Binding
	Terminal    key.Binding
	Popout      key.Binding
	Inbox       key.Binding
	StartStop   key.Binding
	Destroy     key.Binding
	Rename      key.Binding
	Tag         key.Binding
	Update      key.Binding
	Add         key.Binding
	Delete      key.Binding
	Connect     key.Binding
	Disconnect  key.Binding
	Toggle      key.Binding
	Confirm     key.Binding
	Cancel      key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:         key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:       key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Enter:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Back:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:       key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Refresh:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Launch:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "launch")),
		NewConfig:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new config")),
		FromConfig: key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "from config")),
		Hosts:      key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "hosts")),
		Groups:     key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "groups")),
		VSCode:     key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "vscode")),
		Terminal:   key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "terminal")),
		Popout:     key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "external terminal")),
		Inbox:      key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "inbox")),
		StartStop:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start/stop")),
		Destroy:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "destroy")),
		Rename:     key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "rename")),
		Tag:        key.NewBinding(key.WithKeys("#"), key.WithHelp("#", "tag")),
		Update:     key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "update")),
		Add:        key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		Delete:     key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Connect:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "connect")),
		Disconnect: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "disconnect")),
		Toggle:     key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
		Confirm:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		Cancel:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
}

// hkey builds a display-only binding (keys + help text) for footers that
// describe controls owned by an embedded component (e.g. a huh form or textarea).
func hkey(keys, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys), key.WithHelp(keys, desc))
}

// helpBindings implements help.KeyMap for an ordered slice of bindings so any
// screen can render a footer with help.Model.View.
type helpBindings []key.Binding

func (h helpBindings) ShortHelp() []key.Binding  { return h }
func (h helpBindings) FullHelp() [][]key.Binding { return [][]key.Binding{h} }

func (m Model) listHelp() helpBindings {
	k := m.keys
	hb := helpBindings{}
	if len(m.tabs) > 1 {
		hb = append(hb, hkey("←/→", "group"))
	}
	hb = append(hb, k.Launch, k.FromConfig, k.Hosts, k.Groups, k.VSCode, k.Terminal, k.Popout, k.Inbox, k.StartStop, k.Destroy, k.Rename, k.Tag)
	// Surface the update key only when a newer release is available.
	if m.updateBanner != "" {
		hb = append(hb, k.Update)
	}
	return append(hb, k.Quit)
}
