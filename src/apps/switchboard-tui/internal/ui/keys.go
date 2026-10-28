package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

// keyMap collects every binding the TUI reacts to. Each screen exposes the subset
// relevant to it for the help bar via the help* helpers below.
type keyMap struct {
	Up, Down    key.Binding
	Enter, Back key.Binding
	Quit        key.Binding
	Refresh     key.Binding
	// RefreshSandbox re-seeds ONE sandbox's workspace (destructive). Distinct from
	// Refresh, which just reloads the list from the daemons.
	RefreshSandbox key.Binding
	// Kits opens the kit manager; AddKit attaches a kit to the selected sandbox.
	Kits   key.Binding
	AddKit key.Binding
	// EscapeHatchRuns opens the escape-hatch run review for the selected sandbox
	// (feature 005).
	EscapeHatchRuns key.Binding
	// Services opens the per-sandbox port-forwarding screen (feature 006, FR-045).
	// `p` for ports; verified free against every other binding in this map.
	Services key.Binding
	// Sources opens the per-sandbox seeded-folder editor (feature 007). `S` —
	// capital-letter management surfaces are the house pattern (K/A/E/F); verified
	// free against every other binding in this map.
	Sources key.Binding
	// McpServers opens the per-sandbox MCP overlay (feature 008, FR-102).
	McpServers key.Binding
	// ScreenNext/ScreenPrev move between the three top-level screens — the
	// sandbox list, the daemon screen (feature 008, FR-068) and the Jobs screen
	// (feature 010, FR-001; it replaced feature 009's Orchestrators screen), wrapping
	// around. Shift+arrows so plain arrows
	// keep their list meaning. The help labels are dynamic (Model.screenNextKey /
	// screenPrevKey name the screen the key reaches).
	ScreenNext key.Binding
	ScreenPrev key.Binding
	// OrchestratorAttach attaches the selected sandbox to an orchestrator, or
	// detaches a worker (feature 009, FR-107/FR-111). MarkFree closes a run
	// awaiting review on a worker (FR-156). OrchestratedTab toggles the hidden
	// Orchestrator Sandboxes tab (FR-115). `o`, `m` and `O` were free against
	// every other binding in this map.
	OrchestratorAttach key.Binding
	MarkFree           key.Binding
	OrchestratedTab    key.Binding
	// Settings opens the TUI-persisted settings screen (feature 008, FR-101).
	// `,` is free against every other binding and reachable from both top-level
	// screens.
	Settings key.Binding
	// Log opens the sbx output of the selected sandbox's last launch / kit attach /
	// refresh / source add (oplog.go). `l`; verified free against every other
	// binding in this map (the list's own `l` page binding never sees it because
	// list keys are dispatched here first).
	Log        key.Binding
	Launch     key.Binding
	NewConfig  key.Binding
	FromConfig key.Binding
	Hosts      key.Binding
	Groups     key.Binding
	VSCode     key.Binding
	Terminal   key.Binding
	Popout     key.Binding
	Inbox      key.Binding
	StartStop  key.Binding
	Destroy    key.Binding
	Rename     key.Binding
	Tag        key.Binding
	Update     key.Binding
	Add        key.Binding
	Delete     key.Binding
	Connect    key.Binding
	Disconnect key.Binding
	Toggle     key.Binding
	Confirm    key.Binding
	Cancel     key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:  key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		// "r" reloads the list; the per-sandbox re-seed is "F" (feature 004). The
		// help text says "reload" so the two are not confusable in the footer.
		Refresh:            key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")),
		RefreshSandbox:     key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "refresh repos")),
		Kits:               key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "kits")),
		AddKit:             key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "add kit")),
		EscapeHatchRuns:    key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "runs")),
		Services:           key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "services")),
		Sources:            key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "sources")),
		McpServers:         key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "mcp")),
		Log:                key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "sbx output")),
		ScreenNext:         key.NewBinding(key.WithKeys("shift+right"), key.WithHelp("shift+→", "daemons")),
		ScreenPrev:         key.NewBinding(key.WithKeys("shift+left"), key.WithHelp("shift+←", "sandboxes")),
		Settings:           key.NewBinding(key.WithKeys(","), key.WithHelp(",", "settings")),
		OrchestratorAttach: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "attach/detach")),
		MarkFree:           key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mark free")),
		OrchestratedTab:    key.NewBinding(key.WithKeys("O"), key.WithHelp("O", "orchestrated")),
		Launch:             key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "launch")),
		NewConfig:          key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new config")),
		FromConfig:         key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "from config")),
		Hosts:              key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "hosts")),
		Groups:             key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "groups")),
		VSCode:             key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "vscode")),
		Terminal:           key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "terminal")),
		Popout:             key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "external terminal")),
		Inbox:              key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "inbox")),
		StartStop:          key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start/stop")),
		Destroy:            key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "destroy")),
		Rename:             key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "rename")),
		Tag:                key.NewBinding(key.WithKeys("#"), key.WithHelp("#", "tag")),
		Update:             key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "update")),
		Add:                key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		Delete:             key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Connect:            key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "connect")),
		Disconnect:         key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "disconnect")),
		Toggle:             key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
		Confirm:            key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		Cancel:             key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
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
	hb = append(hb, k.Launch, k.FromConfig, k.Hosts, k.Groups, k.VSCode, k.Terminal, k.Popout, k.Inbox, k.StartStop, k.Destroy, k.Rename, k.Tag, k.RefreshSandbox, k.Sources, k.Kits, k.AddKit, k.EscapeHatchRuns, k.Services, k.McpServers, k.Log, k.OrchestratorAttach)
	// feature 009: mark free is offered only while the selected worker's run
	// awaits review (FR-156); every other per-sandbox key works on a worker
	// wherever it is shown (FR-116).
	if sb := m.current(); sb != nil && sb.GetController().GetRun().GetNeedsReview() {
		hb = append(hb, k.MarkFree)
	}
	hb = append(hb, k.OrchestratedTab, m.screenNextKey(), k.Settings)
	// Surface the update key only when a newer release is available.
	if m.updateBanner != "" {
		hb = append(hb, k.Update)
	}
	return append(hb, k.Quit)
}

// unsavedHelp is the unsaved-changes dialog's keys (SWI-9 contracts/forms-ui.md
// §3), rendered as the dialog's own hint line. The reasoning setup's Save is
// Rebuild, on r.
func (m Model) unsavedHelp() helpBindings {
	save := hkey("s", "save")
	if m.unsaved.surface == gReasoning {
		save = hkey("r", "rebuild")
	}
	return helpBindings{save, hkey("d", "discard"), hkey("esc", "cancel"), hkey("←/→", ""), hkey("enter", "")}
}

// helpLine renders bindings as one plain "key desc · key desc" line, for hints
// drawn inside a modal rather than in the footer.
func helpLine(hb helpBindings) string {
	parts := make([]string, len(hb))
	for i, b := range hb {
		parts[i] = strings.TrimSpace(b.Help().Key + " " + b.Help().Desc)
	}
	return strings.Join(parts, " · ")
}
