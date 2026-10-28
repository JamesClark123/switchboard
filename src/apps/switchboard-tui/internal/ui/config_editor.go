package ui

import (
	"encoding/json"
	"maps"
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

// editorState backs the huh-driven configuration editor. The form binds into
// vals, which is heap-allocated and held by pointer: pointers into the
// value-type model itself would dangle as it is copied each Update. baseline is
// vals as the form opened, for the unsaved-changes guard (SWI-9).
type editorState struct {
	form     *huh.Form
	vals     *configFormVals
	baseline configSnapshot
	options  []*pb.OptionManifest_Option
	loading  bool
	status   string
}

// configFormVals are the editor's bound values. The option fields come from
// the host's manifest, so they are maps of pointers by option key.
type configFormVals struct {
	name, mode string
	strs       map[string]*string
	bools      map[string]*bool
}

// configSnapshot is configFormVals flattened to plain values.
type configSnapshot struct {
	name, mode string
	strs       map[string]string
	bools      map[string]bool
}

func newConfigFormVals(options []*pb.OptionManifest_Option) *configFormVals {
	v := &configFormVals{strs: map[string]*string{}, bools: map[string]*bool{}}
	for _, opt := range options {
		if opt.GetType() == "bool" {
			v.bools[opt.GetKey()] = new(bool)
		} else {
			v.strs[opt.GetKey()] = new(string)
		}
	}
	return v
}

func (v *configFormVals) snapshot() configSnapshot {
	s := configSnapshot{name: v.name, mode: v.mode, strs: map[string]string{}, bools: map[string]bool{}}
	for k, p := range v.strs {
		s.strs[k] = *p
	}
	for k, p := range v.bools {
		s.bools[k] = *p
	}
	return s
}

func (s configSnapshot) equal(o configSnapshot) bool {
	return s.name == o.name && s.mode == o.mode && maps.Equal(s.strs, o.strs) && maps.Equal(s.bools, o.bools)
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
	m.editor.vals = newConfigFormVals(m.editor.options)
	m.editor.form = m.buildEditorForm(m.editor.vals)
	// After the build: the mode Select writes its first option into the vals.
	m.editor.baseline = m.editor.vals.snapshot()
	return m, m.editor.form.Init()
}

// buildEditorForm builds the form bound to v; it never fills v, so reopenForm
// keeps what the user typed.
func (m Model) buildEditorForm(v *configFormVals) *huh.Form {
	fields := []huh.Field{
		huh.NewInput().Key("__name").Title("Configuration name").
			Placeholder("e.g. backend-dev").Value(&v.name),
		huh.NewSelect[string]().Key("__mode").Title("Seeding mode").
			Options(huh.NewOption("duplicate (default)", "duplicate"), huh.NewOption("clone", "clone")).Value(&v.mode),
	}
	for _, opt := range m.editor.options {
		key := opt.GetKey()
		switch opt.GetType() {
		case "bool":
			fields = append(fields, huh.NewConfirm().Key(key).
				Title(key).Description(opt.GetDescription()).Value(v.bools[key]))
		default:
			fields = append(fields, huh.NewInput().Key(key).
				Title(key+" ("+opt.GetType()+")").Description(opt.GetDescription()).Value(v.strs[key]))
		}
	}
	return huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huhTheme()).
		WithShowHelp(true).
		WithShowErrors(false).
		WithWidth(m.bodyWidth()).
		WithHeight(m.bodyHeight()).
		WithKeyMap(formKeyMap())
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
	return m.formKey(gConfig, msg)
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
		return reviveCompleted(gConfig)(m.saveConfig())
	}
	return m, cmd
}

func (m Model) saveConfig() (tea.Model, tea.Cmd) {
	v := m.editor.vals
	if m.editor.form == nil || v == nil {
		return m, nil
	}
	name := strings.TrimSpace(v.name)
	if name == "" {
		m.editor.status = "name required"
		return m, nil
	}
	values := map[string]string{}
	for _, opt := range m.editor.options {
		key := opt.GetKey()
		if opt.GetType() == "bool" {
			if p := v.bools[key]; p != nil && *p {
				values[key] = "true"
			}
			continue
		}
		if p := v.strs[key]; p != nil {
			if raw := strings.TrimSpace(*p); raw != "" {
				values[key] = encodeOption(opt.GetType(), raw)
			}
		}
	}
	mode := v.mode
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
