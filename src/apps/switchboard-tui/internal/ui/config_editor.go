package ui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// ---- Config editor (US2: FR-013/014/015/016) ----

// editorState backs the huh-driven configuration editor. Field values live
// inside the (pointer) huh.Form and are read back by key — never via external
// pointers, which would dangle as the value-type model is copied each Update.
type editorState struct {
	form    *huh.Form
	options []*pb.OptionManifest_Option
	loading bool
	status  string
}

// enterConfigEditor opens a fresh config editor and loads the option manifest.
func (m Model) enterConfigEditor() (tea.Model, tea.Cmd) {
	if m.configs == nil {
		m.status = "no config store available"
		return m, nil
	}
	m.screen = screenConfigEditor
	m.editor = editorState{loading: true}
	return m, m.manifestCmd()
}

func (m Model) manifestCmd() tea.Cmd {
	d := m.daemon
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		man, err := d.OptionManifest(ctx)
		if err != nil {
			return errMsg{err}
		}
		return manifestMsg{manifest: man}
	}
}

// applyManifest builds the editor form from the host's option surface (FR-014).
func (m Model) applyManifest(man *pb.OptionManifest) (tea.Model, tea.Cmd) {
	m.editor.options = man.GetOptions()
	m.editor.loading = false
	m.editor.form = m.buildEditorForm()
	return m, m.editor.form.Init()
}

func (m Model) buildEditorForm() *huh.Form {
	fields := []huh.Field{
		huh.NewInput().Key("__name").Title("Configuration name").
			Placeholder("e.g. backend-dev"),
		huh.NewSelect[string]().Key("__mode").Title("Seeding mode").
			Options(huh.NewOption("duplicate (default)", "duplicate"), huh.NewOption("clone", "clone")),
	}
	for _, opt := range m.editor.options {
		key := opt.GetKey()
		switch opt.GetType() {
		case "bool":
			fields = append(fields, huh.NewConfirm().Key(key).
				Title(key).Description(opt.GetDescription()))
		default:
			fields = append(fields, huh.NewInput().Key(key).
				Title(key+" ("+opt.GetType()+")").Description(opt.GetDescription()))
		}
	}
	return huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huhTheme()).
		WithShowHelp(true).
		WithShowErrors(false).
		WithWidth(m.bodyWidth()).
		WithHeight(m.bodyHeight())
}

func (m Model) editorHelp() helpBindings {
	return helpBindings{hkey("tab/enter", "next"), hkey("space", "toggle"), hkey("ctrl+s", "save"), hkey("esc", "cancel")}
}

func (m Model) updateEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editor.loading || m.editor.form == nil {
		if msg.Type == tea.KeyEsc {
			m.screen = screenList
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.screen = screenList
		return m, nil
	case "ctrl+s":
		return m.saveConfig()
	}
	return m.advanceEditorForm(msg)
}

// advanceEditorForm feeds a message to the editor form (key or huh follow-up).
func (m Model) advanceEditorForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.editor.form == nil || m.editor.loading {
		return m, nil
	}
	f, cmd := m.editor.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		m.editor.form = ff
	}
	if m.editor.form.State == huh.StateCompleted {
		return m.saveConfig()
	}
	if m.editor.form.State == huh.StateAborted {
		m.screen = screenList
		return m, nil
	}
	return m, cmd
}

func (m Model) saveConfig() (tea.Model, tea.Cmd) {
	if m.editor.form == nil {
		return m, nil
	}
	name := strings.TrimSpace(m.editor.form.GetString("__name"))
	if name == "" {
		m.editor.status = "name required"
		return m, nil
	}
	values := map[string]string{}
	for _, opt := range m.editor.options {
		key := opt.GetKey()
		if opt.GetType() == "bool" {
			if m.editor.form.GetBool(key) {
				values[key] = "true"
			}
			continue
		}
		if raw := strings.TrimSpace(m.editor.form.GetString(key)); raw != "" {
			values[key] = encodeOption(opt.GetType(), raw)
		}
	}
	mode := m.editor.form.GetString("__mode")
	if mode == "" {
		mode = "duplicate"
	}
	cfg := &store.Configuration{
		Name:        name,
		KitOptions:  values,
		SeedingMode: mode,
	}
	if _, err := m.configs.Save(cfg, time.Now()); err != nil {
		m.editor.status = "save failed: " + err.Error()
		return m, nil
	}
	m.screen = screenList
	m.status = "saved config " + cfg.Name
	return m, nil
}

func (m Model) viewEditor() string {
	if m.editor.loading {
		return sectionStyle.Render("Configuration editor") + "\n\n" +
			m.spinner.View() + " loading option manifest…"
	}
	head := sectionStyle.Render("Configuration editor")
	if len(m.editor.options) == 0 {
		head = lipgloss.JoinVertical(lipgloss.Left, head,
			dimStyle.Render("Host advertised no sbx options (introspection unavailable)."))
	}
	body := ""
	if m.editor.form != nil {
		body = m.editor.form.View()
	}
	out := lipgloss.JoinVertical(lipgloss.Left, head, "", body)
	if m.editor.status != "" {
		out = lipgloss.JoinVertical(lipgloss.Left, out, "", statusErrStyle.Render(m.editor.status))
	}
	return out
}

// ---- Config picker (launch from a saved config — FR-015, T042) ----

type pickerState struct {
	list    list.Model
	configs []*store.Configuration
	ready   bool
}

func (m Model) enterConfigPicker() (tea.Model, tea.Cmd) {
	if m.configs == nil {
		m.status = "no config store available"
		return m, nil
	}
	m.screen = screenConfigPicker
	l := newItemList("Saved configurations", "config", "configs", m.bodyWidth(), m.bodyHeight())
	m.picker = pickerState{list: l, ready: true}
	return m, m.configsCmd()
}

func (m Model) configsCmd() tea.Cmd {
	cs := m.configs
	return func() tea.Msg {
		list, err := cs.List()
		if err != nil {
			return errMsg{err}
		}
		return configsMsg(list)
	}
}

func (m Model) applyConfigs(msg configsMsg) (tea.Model, tea.Cmd) {
	m.picker.configs = []*store.Configuration(msg)
	items := make([]list.Item, 0, len(m.picker.configs))
	for _, cfg := range m.picker.configs {
		items = append(items, listItem{
			id:      cfg.ID,
			title:   cfg.Name,
			desc:    dimStyle.Render(cfg.SeedingMode) + " · " + plural(len(cfg.KitOptions), "option", "options"),
			filter:  cfg.Name,
			payload: cfg,
		})
	}
	if m.picker.ready {
		m.picker.list.SetItems(items)
	}
	return m, nil
}

func (m Model) pickerHelp() helpBindings {
	return helpBindings{m.keys.Enter, m.keys.Delete, hkey("/", "filter"), m.keys.Back}
}

func (m Model) updatePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.picker.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.picker.list, cmd = m.picker.list.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "esc", "q":
		m.screen = screenList
		return m, nil
	case "d":
		if cfg := m.pickerCurrent(); cfg != nil {
			if err := m.configs.Delete(cfg.ID); err != nil {
				return m, func() tea.Msg { return errMsg{err} }
			}
			return m, m.configsCmd()
		}
		return m, nil
	case "enter":
		if cfg := m.pickerCurrent(); cfg != nil {
			return m.enterLaunchWithConfig(cfg)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.picker.list, cmd = m.picker.list.Update(msg)
	return m, cmd
}

func (m Model) pickerCurrent() *store.Configuration {
	if it, ok := m.picker.list.SelectedItem().(listItem); ok {
		if cfg, ok := it.payload.(*store.Configuration); ok {
			return cfg
		}
	}
	return nil
}

func (m Model) viewPicker() string {
	return m.picker.list.View()
}

// ---- value encoding helpers ----

// encodeOption JSON-encodes a raw typed value for ConfigSnapshot.kit_options.
func encodeOption(typ, raw string) string {
	raw = strings.TrimSpace(raw)
	switch typ {
	case "bool":
		if raw == "true" || raw == "false" {
			return raw
		}
		return "false"
	case "int":
		if _, err := strconv.Atoi(raw); err == nil {
			return raw
		}
	}
	b, _ := json.Marshal(raw)
	return string(b)
}

// plural renders "n singular" or "n plural" for compact summaries.
func plural(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}
