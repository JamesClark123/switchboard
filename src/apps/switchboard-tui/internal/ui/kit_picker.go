package ui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// kitPickerState backs the kit manager: the list of authored kits, plus the
// sandbox an attach was launched from (empty when managing kits generally).
type kitPickerState struct {
	list  list.Model
	kits  []*store.Kit
	ready bool
	// attachTo/attachHost are set when the picker was opened to attach a kit to a
	// specific sandbox ("A" on a row) rather than to manage kits ("K").
	attachTo   string
	attachName string
	attachHost string
}

// kitsMsg carries the loaded kit list.
type kitsMsg []*store.Kit

// enterKitPicker opens the kit manager (create / edit / delete).
func (m Model) enterKitPicker() (tea.Model, tea.Cmd) {
	return m.openKitPicker(kitPickerState{})
}

// enterKitAttach opens the picker scoped to attaching a kit to one sandbox.
//
// sbx only honours `--kit` at creation, so attaching to an existing sandbox goes
// through `sbx kit add`, which restarts it. That restart is why this is gated by a
// confirmation rather than fired directly.
func (m Model) enterKitAttach(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	return m.openKitPicker(kitPickerState{
		attachTo:   sb.GetId(),
		attachName: sb.GetDisplayName(),
		attachHost: host,
	})
}

func (m Model) openKitPicker(st kitPickerState) (tea.Model, tea.Cmd) {
	if m.kits == nil {
		m.status = "no kit store available"
		return m, nil
	}
	title := "Agent kits"
	if st.attachTo != "" {
		title = "Attach kit to " + st.attachName
	}
	st.list = newItemList(title, "kit", "kits", m.bodyWidth(), m.bodyHeight())
	st.ready = true
	m.kitPicker = st
	m.screen = screenKitPicker
	return m, m.kitsCmd()
}

func (m Model) kitsCmd() tea.Cmd {
	ks := m.kits
	return func() tea.Msg {
		list, err := ks.List()
		if err != nil {
			return errMsg{err}
		}
		return kitsMsg(list)
	}
}

func (m Model) applyKits(msg kitsMsg) (tea.Model, tea.Cmd) {
	m.kitPicker.kits = []*store.Kit(msg)
	items := make([]list.Item, 0, len(m.kitPicker.kits))
	for _, k := range m.kitPicker.kits {
		items = append(items, listItem{
			id:      k.ID(),
			title:   kitLabel(k),
			desc:    kitSummary(k),
			filter:  k.Name + " " + k.DisplayName,
			payload: k,
		})
	}
	if m.kitPicker.ready {
		m.kitPicker.list.SetItems(items)
	}
	return m, nil
}

func kitLabel(k *store.Kit) string {
	if k.DisplayName != "" {
		return k.DisplayName + dimStyle.Render(" ("+k.Name+")")
	}
	return k.Name
}

// kitSummary describes what a kit actually does, so the picker is scannable
// without opening each one.
func kitSummary(k *store.Kit) string {
	var parts []string
	if c := k.Commands; c != nil {
		if n := len(c.Install); n > 0 {
			parts = append(parts, plural(n, "install", "installs"))
		}
		if n := len(c.Startup); n > 0 {
			parts = append(parts, plural(n, "startup", "startups"))
		}
		if n := len(c.InitFiles); n > 0 {
			parts = append(parts, plural(n, "file", "files"))
		}
	}
	if k.Network != nil {
		if n := len(k.Network.AllowedDomains) + len(k.Network.DeniedDomains); n > 0 {
			parts = append(parts, plural(n, "domain", "domains"))
		}
	}
	if k.Environment != nil {
		if n := len(k.Environment.Variables); n > 0 {
			parts = append(parts, plural(n, "env var", "env vars"))
		}
	}
	if len(parts) == 0 {
		return dimStyle.Render("empty kit")
	}
	return dimStyle.Render(strings.Join(parts, " · "))
}

func (m Model) kitPickerHelp() helpBindings {
	if m.kitPicker.attachTo != "" {
		return helpBindings{hkey("enter", "attach"), hkey("e", "edit"), hkey("/", "filter"), m.keys.Back}
	}
	return helpBindings{hkey("enter", "edit"), hkey("n", "new"), m.keys.Delete, hkey("/", "filter"), m.keys.Back}
}

func (m Model) updateKitPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.kitPicker.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.kitPicker.list, cmd = m.kitPicker.list.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "esc", "q":
		m.screen = screenList
		return m, nil
	case "n":
		if m.kitPicker.attachTo != "" {
			return m, nil // attaching: creating a new kit here would lose the target
		}
		return m.enterKitEditor(nil)
	case "e":
		if k := m.currentKit(); k != nil {
			return m.enterKitEditor(k)
		}
		return m, nil
	case "d":
		if m.kitPicker.attachTo != "" {
			return m, nil
		}
		if k := m.currentKit(); k != nil {
			if err := m.kits.Delete(k.ID()); err != nil {
				m.status = "error: " + err.Error()
				return m, nil
			}
			m.status = "deleted kit " + k.Name
			return m, m.kitsCmd()
		}
		return m, nil
	case "enter":
		k := m.currentKit()
		if k == nil {
			return m, nil
		}
		if m.kitPicker.attachTo != "" {
			return m.confirmAttachKit(k)
		}
		return m.enterKitEditor(k)
	}
	var cmd tea.Cmd
	m.kitPicker.list, cmd = m.kitPicker.list.Update(msg)
	return m, cmd
}

// currentKit returns the highlighted kit, or nil when the list is empty.
func (m Model) currentKit() *store.Kit {
	it, ok := m.kitPicker.list.SelectedItem().(listItem)
	if !ok {
		return nil
	}
	k, _ := it.payload.(*store.Kit)
	return k
}

// confirmAttachKit gates the attach: `sbx kit add` restarts the sandbox, which
// drops any attached terminal session, and a kit cannot be removed afterwards
// without destroying the sandbox.
func (m Model) confirmAttachKit(k *store.Kit) (tea.Model, tea.Cmd) {
	ref, err := k.ToRef()
	if err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	id, host, name := m.kitPicker.attachTo, m.kitPicker.attachHost, m.kitPicker.attachName
	return m.enterConfirm(confirmState{
		title: "Attach kit?",
		body: []string{
			"Attach " + selectedStyle.Render(k.Name) + " to " + selectedStyle.Render(name) + ".",
			"",
			"The sandbox restarts to apply the kit. Installed packages,",
			"images and agent history are preserved, but any attached",
			"terminal session ends and must be reattached.",
			"",
			dimStyle.Render("A kit cannot be removed from a sandbox afterwards —"),
			dimStyle.Render("destroy and recreate it to start clean."),
		},
		verb:      "kit add",
		sandboxID: id,
		onConfirm: m.addKitCmd(m.daemonForHost(host), id, ref, k.Name),
	})
}

func (m Model) addKitCmd(d Daemon, id string, ref *pb.KitRef, label string) tea.Cmd {
	return func() tea.Msg {
		// `sbx kit add` re-runs the kit's install commands and restarts the sandbox,
		// which can take minutes — well past the shared 60s RPC timeout.
		ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()
		if _, err := d.AddKit(ctx, id, ref, nil); err != nil {
			return errMsg{err}
		}
		return statusMsg("attached kit " + label)
	}
}
