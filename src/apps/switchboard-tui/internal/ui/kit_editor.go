package ui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// The kit editor is a two-level UI rather than one huh form.
//
// huh renders a flat list of fields, but a kit's substance is repeated, nested
// items — N install commands, each with a command/user/description; N initFiles,
// each with multi-line content. Those cannot be expressed as a flat form, and
// flattening them (one command per line in a text area) would drop per-item fields
// and mangle multi-line file content.
//
// So: a section list (this file's kitEditorState.section) drills into a per-section
// item list, and editing one item opens a small huh form scoped to just that item.
// Scalar sections (identity, agent context) open a form directly.

// kitSection identifies a top-level area of the spec.
type kitSection int

const (
	secIdentity kitSection = iota
	secInstall
	secStartup
	secInitFiles
	secNetwork
	secEnvironment
	secCredentials
	secAgentContext
	secCount
)

func (s kitSection) title() string {
	switch s {
	case secIdentity:
		return "Identity"
	case secInstall:
		return "Install commands"
	case secStartup:
		return "Startup commands"
	case secInitFiles:
		return "Init files"
	case secNetwork:
		return "Network"
	case secEnvironment:
		return "Environment"
	case secCredentials:
		return "Credentials"
	case secAgentContext:
		return "Agent context"
	}
	return ""
}

func (s kitSection) blurb() string {
	switch s {
	case secIdentity:
		return "name, display name, description"
	case secInstall:
		return "run once at creation, as sh -c"
	case secStartup:
		return "run at every start, argv (must be idempotent)"
	case secInitFiles:
		return "written at every start; ${WORKDIR} expands"
	case secNetwork:
		return "allowed / denied domains (deny wins)"
	case secEnvironment:
		return "container env vars + proxy-managed names"
	case secCredentials:
		return "service credential sources (host-side, via proxy)"
	case secAgentContext:
		return "markdown appended to the agent's memory"
	}
	return ""
}

// itemized reports whether a section holds a list of items (vs. a single form).
func (s kitSection) itemized() bool {
	return s == secInstall || s == secStartup || s == secInitFiles
}

// kitFormVals holds the values bound into whichever form is open.
//
// Values are read back through these bound pointers rather than via
// huh.Form.GetString: huh writes a bound pointer live as the user types, but only
// syncs the form's key/value store when a field BLURS. Since the editor applies a
// form with ctrl+s while a field is still focused, GetString would return a stale
// (usually empty) value and silently drop what the user just typed.
//
// The struct is heap-allocated and referenced by pointer from kitEditorState. That
// is what makes it survive Bubble Tea's value-copy of Model: a pointer INTO the
// model (&m.field) would target a stale copy after the next Update, but every copy
// of this pointer refers to the same allocation huh is writing to.
type kitFormVals struct {
	name, displayName, description string
	allowed, denied                string
	vars, proxied                  string
	credSources                    string
	agentContext                   string

	// item-form fields
	itemCommand, itemUser, itemDesc string
	itemBackground                  bool
	itemPath, itemContent, itemMode string
	itemOnlyIfMissing               bool
}

// kitEditorState backs the editor. The kit under edit is held by value so an
// abandoned edit cannot mutate the stored kit.
type kitEditorState struct {
	kit store.Kit
	// vals backs the open form; nil when no form is open.
	vals *kitFormVals
	// editing is the id of the kit being updated; empty when creating. Kept so a
	// rename can delete the old directory rather than orphan it.
	editing string
	// section is the highlighted section; inSection is true once drilled in.
	section   kitSection
	inSection bool
	// item is the highlighted item index within an itemized section.
	item int
	// form is non-nil while a form (section-level or item-level) is open.
	form *huh.Form
	// formKind records what the open form edits, so save routes correctly.
	formKind kitFormKind
	// formItem is the index being edited, or -1 when appending a new item.
	formItem int
	status   string
	// validating/validation hold the result of the last `sbx kit validate`.
	validating bool
	validation []string
	validOK    bool
}

type kitFormKind int

const (
	formNone kitFormKind = iota
	formIdentity
	formNetwork
	formEnvironment
	formCredentials
	formAgentContext
	formInstall
	formStartup
	formInitFile
)

// enterKitEditor opens the editor on an existing kit, or a blank one when nil.
func (m Model) enterKitEditor(k *store.Kit) (tea.Model, tea.Cmd) {
	st := kitEditorState{formItem: -1}
	if k != nil {
		st.kit = *k // by value: an abandoned edit must not touch the stored kit
		st.editing = k.ID()
	}
	m.kitEditor = st
	m.screen = screenKitEditor
	return m, nil
}

func (m Model) kitEditorHelp() helpBindings {
	switch {
	case m.kitEditor.form != nil:
		return helpBindings{hkey("tab/enter", "next"), hkey("ctrl+s", "apply"), hkey("esc", "cancel")}
	case m.kitEditor.inSection && m.kitEditor.section.itemized():
		return helpBindings{hkey("a", "add"), hkey("enter", "edit"), hkey("d", "delete"), hkey("ctrl+s", "save"), hkey("esc", "back")}
	default:
		return helpBindings{hkey("enter", "open"), hkey("ctrl+s", "save"), hkey("v", "validate"), hkey("esc", "back")}
	}
}

func (m Model) updateKitEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A form owns all input while open.
	if m.kitEditor.form != nil {
		switch msg.String() {
		case "esc":
			m.kitEditor.form = nil
			m.kitEditor.formKind = formNone
			return m, nil
		case "ctrl+s":
			return m.applyKitForm()
		}
		return m.advanceKitForm(msg)
	}

	if m.kitEditor.inSection {
		return m.updateKitSectionKey(msg)
	}

	switch msg.String() {
	case "esc", "q":
		return m.enterKitPicker()
	case "ctrl+s":
		return m.saveKit()
	case "v":
		return m.validateKit()
	case "up", "k":
		if m.kitEditor.section > 0 {
			m.kitEditor.section--
		}
		return m, nil
	case "down", "j":
		if m.kitEditor.section < secCount-1 {
			m.kitEditor.section++
		}
		return m, nil
	case "enter":
		return m.openKitSection()
	}
	return m, nil
}

// updateKitSectionKey handles the item list inside an itemized section.
func (m Model) updateKitSectionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := m.kitSectionLen()
	switch msg.String() {
	case "esc", "q":
		m.kitEditor.inSection = false
		m.kitEditor.item = 0
		return m, nil
	case "ctrl+s":
		// Save is editor-global: having just added the last command, the user is
		// still inside a section, and making them esc out first to save is a trap.
		return m.saveKit()
	case "up", "k":
		if m.kitEditor.item > 0 {
			m.kitEditor.item--
		}
		return m, nil
	case "down", "j":
		if m.kitEditor.item < n-1 {
			m.kitEditor.item++
		}
		return m, nil
	case "a":
		return m.openKitItemForm(-1)
	case "enter":
		if n == 0 {
			return m.openKitItemForm(-1)
		}
		return m.openKitItemForm(m.kitEditor.item)
	case "d":
		if n > 0 {
			m.deleteKitItem(m.kitEditor.item)
			if m.kitEditor.item >= m.kitSectionLen() && m.kitEditor.item > 0 {
				m.kitEditor.item--
			}
		}
		return m, nil
	}
	return m, nil
}

// openKitSection drills into a section: itemized ones show a list, scalar ones open
// their form straight away.
func (m Model) openKitSection() (tea.Model, tea.Cmd) {
	s := m.kitEditor.section
	if s.itemized() {
		m.kitEditor.inSection = true
		m.kitEditor.item = 0
		return m, nil
	}
	m.kitEditor.vals = &kitFormVals{}
	switch s {
	case secIdentity:
		return m.openKitForm(formIdentity, m.identityForm())
	case secNetwork:
		return m.openKitForm(formNetwork, m.networkForm())
	case secEnvironment:
		return m.openKitForm(formEnvironment, m.environmentForm())
	case secCredentials:
		return m.openKitForm(formCredentials, m.credentialsForm())
	case secAgentContext:
		return m.openKitForm(formAgentContext, m.agentContextForm())
	}
	return m, nil
}

func (m Model) openKitForm(kind kitFormKind, f *huh.Form) (tea.Model, tea.Cmd) {
	m.kitEditor.form = f
	m.kitEditor.formKind = kind
	return m, f.Init()
}

// advanceKitForm feeds a message to the open form. Also called from Model.forward,
// so huh's internal async work (cursor blink, field advance) keeps running.
func (m Model) advanceKitForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.kitEditor.form == nil {
		return m, nil
	}
	f, cmd := m.kitEditor.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		m.kitEditor.form = ff
	}
	switch m.kitEditor.form.State {
	case huh.StateCompleted:
		return m.applyKitForm()
	case huh.StateAborted:
		m.kitEditor.form = nil
		m.kitEditor.formKind = formNone
		return m, nil
	}
	return m, cmd
}

// ---------- section forms ----------

func (m *Model) identityForm() *huh.Form {
	v := m.kitEditor.vals
	v.name, v.displayName, v.description = m.kitEditor.kit.Name, m.kitEditor.kit.DisplayName, m.kitEditor.kit.Description
	return m.newKitForm(
		huh.NewInput().Key("name").Title("Name").
			Description("Kit id — lowercase, hyphens. Also the directory name.").Value(&v.name),
		huh.NewInput().Key("displayName").Title("Display name").Value(&v.displayName),
		huh.NewInput().Key("description").Title("Description").Value(&v.description),
	)
}

func (m *Model) networkForm() *huh.Form {
	v := m.kitEditor.vals
	if n := m.kitEditor.kit.Network; n != nil {
		v.allowed = strings.Join(n.AllowedDomains, "\n")
		v.denied = strings.Join(n.DeniedDomains, "\n")
	}
	return m.newKitForm(
		huh.NewText().Key("allowed").Title("Allowed domains").
			Description("One per line. Wildcards allowed. The sandbox can reach nothing else.").Value(&v.allowed),
		huh.NewText().Key("denied").Title("Denied domains").
			Description("One per line. Deny wins over allow, including across other kits.").Value(&v.denied),
	)
}

func (m *Model) environmentForm() *huh.Form {
	v := m.kitEditor.vals
	if e := m.kitEditor.kit.Environment; e != nil {
		v.vars = joinEnv(e.Variables)
		v.proxied = strings.Join(e.ProxyManaged, "\n")
	}
	return m.newKitForm(
		huh.NewText().Key("vars").Title("Variables").
			Description("KEY=value, one per line. Set directly in the container.").Value(&v.vars),
		huh.NewText().Key("proxied").Title("Proxy-managed").
			Description("Variable names populated by the proxy at request time. One per line.").Value(&v.proxied),
	)
}

func (m *Model) credentialsForm() *huh.Form {
	v := m.kitEditor.vals
	if c := m.kitEditor.kit.Credentials; c != nil {
		v.credSources = joinCredentials(c.Sources)
	}
	return m.newKitForm(
		huh.NewText().Key("sources").Title("Credential sources").
			Description("service=ENV_VAR[,ENV_VAR], one per line (e.g. github=GH_TOKEN).\nSecrets stay on the host; the proxy injects them.").Value(&v.credSources),
	)
}

func (m *Model) agentContextForm() *huh.Form {
	v := m.kitEditor.vals
	v.agentContext = m.kitEditor.kit.AgentContext
	return m.newKitForm(
		huh.NewText().Key("agentContext").Title("Agent context").
			Description("Markdown appended to the agent's memory.").Value(&v.agentContext),
	)
}

// ---------- item forms ----------

// openKitItemForm opens the form for one item; idx < 0 appends a new one.
func (m Model) openKitItemForm(idx int) (tea.Model, tea.Cmd) {
	m.kitEditor.formItem = idx
	m.kitEditor.vals = &kitFormVals{}
	v := m.kitEditor.vals
	cmds := m.kitEditor.kit.Commands
	switch m.kitEditor.section {
	case secInstall:
		var it store.KitInstallCommand
		if cmds != nil && idx >= 0 && idx < len(cmds.Install) {
			it = cmds.Install[idx]
		}
		v.itemCommand, v.itemUser, v.itemDesc = it.Command, defaultStr(it.User, "0"), it.Description
		return m.openKitForm(formInstall, m.newKitForm(
			huh.NewText().Key("command").Title("Command").
				Description("Shell string, run once at creation via sh -c.").Value(&v.itemCommand),
			huh.NewInput().Key("user").Title("User").Description(`"0" = root, "1000" = agent.`).Value(&v.itemUser),
			huh.NewInput().Key("description").Title("Description").Value(&v.itemDesc),
		))
	case secStartup:
		var it store.KitStartupCommand
		if cmds != nil && idx >= 0 && idx < len(cmds.Startup) {
			it = cmds.Startup[idx]
		}
		v.itemCommand = strings.Join(it.Command, "\n")
		v.itemUser, v.itemDesc, v.itemBackground = defaultStr(it.User, "1000"), it.Description, it.Background
		return m.openKitForm(formStartup, m.newKitForm(
			huh.NewText().Key("command").Title("Command (argv)").
				Description("One argument per line — no shell is involved.\nRuns at every start, so it MUST be idempotent.").Value(&v.itemCommand),
			huh.NewInput().Key("user").Title("User").Value(&v.itemUser),
			huh.NewConfirm().Key("background").Title("Background").Value(&v.itemBackground),
			huh.NewInput().Key("description").Title("Description").Value(&v.itemDesc),
		))
	case secInitFiles:
		var it store.KitInitFile
		if cmds != nil && idx >= 0 && idx < len(cmds.InitFiles) {
			it = cmds.InitFiles[idx]
		}
		v.itemPath, v.itemContent = it.Path, it.Content
		v.itemMode, v.itemOnlyIfMissing, v.itemDesc = defaultStr(it.Mode, "0644"), it.OnlyIfMissing, it.Description
		return m.openKitForm(formInitFile, m.newKitForm(
			huh.NewInput().Key("path").Title("Path").Description("Absolute path in the container.").Value(&v.itemPath),
			huh.NewText().Key("content").Title("Content").
				Description("${WORKDIR} expands to the workspace path.").Value(&v.itemContent),
			huh.NewInput().Key("mode").Title("Mode").Description("Octal, e.g. 0755.").Value(&v.itemMode),
			huh.NewConfirm().Key("onlyIfMissing").Title("Only if missing").Value(&v.itemOnlyIfMissing),
			huh.NewInput().Key("description").Title("Description").Value(&v.itemDesc),
		))
	}
	return m, nil
}

// applyKitForm folds the open form's values back into the kit under edit.
//
// Values come from the bound kitFormVals, never from Form.GetString: huh only syncs
// its key/value store when a field blurs, so a ctrl+s landing while a field is
// focused would read back an empty string and silently discard the user's input.
func (m Model) applyKitForm() (tea.Model, tea.Cmd) {
	v := m.kitEditor.vals
	if m.kitEditor.form == nil || v == nil {
		return m, nil
	}
	k := &m.kitEditor.kit
	switch m.kitEditor.formKind {
	case formIdentity:
		k.Name = strings.TrimSpace(v.name)
		k.DisplayName = strings.TrimSpace(v.displayName)
		k.Description = strings.TrimSpace(v.description)
	case formNetwork:
		allowed, denied := splitLines(v.allowed), splitLines(v.denied)
		if len(allowed) == 0 && len(denied) == 0 {
			k.Network = nil
		} else {
			k.Network = &store.KitNetwork{AllowedDomains: allowed, DeniedDomains: denied}
		}
	case formEnvironment:
		vars, err := parseEnv(v.vars)
		if err != nil {
			m.kitEditor.status = err.Error()
			return m, nil
		}
		proxied := splitLines(v.proxied)
		if len(vars) == 0 && len(proxied) == 0 {
			k.Environment = nil
		} else {
			k.Environment = &store.KitEnvironment{Variables: vars, ProxyManaged: proxied}
		}
	case formCredentials:
		src, err := parseCredentials(v.credSources)
		if err != nil {
			m.kitEditor.status = err.Error()
			return m, nil
		}
		if len(src) == 0 {
			k.Credentials = nil
		} else {
			k.Credentials = &store.KitCredentials{Sources: src}
		}
	case formAgentContext:
		k.AgentContext = strings.TrimSpace(v.agentContext)
	case formInstall:
		cmd := strings.TrimSpace(v.itemCommand)
		if cmd == "" {
			m.kitEditor.status = "command is required"
			return m, nil
		}
		it := store.KitInstallCommand{
			Command:     cmd,
			User:        omitDefault(v.itemUser, "0"),
			Description: strings.TrimSpace(v.itemDesc),
		}
		m.ensureCommands()
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.Commands.Install) {
			k.Commands.Install[i] = it
		} else {
			k.Commands.Install = append(k.Commands.Install, it)
		}
	case formStartup:
		argv := splitLines(v.itemCommand)
		if len(argv) == 0 {
			m.kitEditor.status = "command is required"
			return m, nil
		}
		it := store.KitStartupCommand{
			Command:     argv,
			User:        omitDefault(v.itemUser, "1000"),
			Background:  v.itemBackground,
			Description: strings.TrimSpace(v.itemDesc),
		}
		m.ensureCommands()
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.Commands.Startup) {
			k.Commands.Startup[i] = it
		} else {
			k.Commands.Startup = append(k.Commands.Startup, it)
		}
	case formInitFile:
		path := strings.TrimSpace(v.itemPath)
		if path == "" {
			m.kitEditor.status = "path is required"
			return m, nil
		}
		it := store.KitInitFile{
			Path:          path,
			Content:       v.itemContent,
			Mode:          omitDefault(v.itemMode, "0644"),
			OnlyIfMissing: v.itemOnlyIfMissing,
			Description:   strings.TrimSpace(v.itemDesc),
		}
		m.ensureCommands()
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.Commands.InitFiles) {
			k.Commands.InitFiles[i] = it
		} else {
			k.Commands.InitFiles = append(k.Commands.InitFiles, it)
		}
	}
	m.kitEditor.form = nil
	m.kitEditor.vals = nil
	m.kitEditor.formKind = formNone
	m.kitEditor.formItem = -1
	m.kitEditor.status = ""
	// The kit changed, so any previous validation result no longer describes it.
	m.kitEditor.validation = nil
	m.kitEditor.validOK = false
	return m, nil
}

func (m *Model) ensureCommands() {
	if m.kitEditor.kit.Commands == nil {
		m.kitEditor.kit.Commands = &store.KitCommands{}
	}
}

func (m *Model) deleteKitItem(idx int) {
	c := m.kitEditor.kit.Commands
	if c == nil {
		return
	}
	switch m.kitEditor.section {
	case secInstall:
		if idx < len(c.Install) {
			c.Install = append(c.Install[:idx], c.Install[idx+1:]...)
		}
	case secStartup:
		if idx < len(c.Startup) {
			c.Startup = append(c.Startup[:idx], c.Startup[idx+1:]...)
		}
	case secInitFiles:
		if idx < len(c.InitFiles) {
			c.InitFiles = append(c.InitFiles[:idx], c.InitFiles[idx+1:]...)
		}
	}
	m.kitEditor.validation = nil
}

// ---------- save / validate ----------

func (m Model) saveKit() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.kitEditor.kit.Name) == "" {
		m.kitEditor.status = "name required (Identity)"
		return m, nil
	}
	kit := m.kitEditor.kit
	saved, err := m.kits.Save(&kit)
	if err != nil {
		m.kitEditor.status = "save failed: " + err.Error()
		return m, nil
	}
	// A rename changes the id (the directory name), so the old directory would
	// otherwise linger as a duplicate kit.
	if prev := m.kitEditor.editing; prev != "" && prev != saved.ID() {
		if err := m.kits.Delete(prev); err != nil {
			m.status = "warning: old kit dir not removed: " + err.Error()
		}
	}
	m.status = "saved kit " + saved.Name
	return m.enterKitPicker()
}

// kitValidatedMsg carries a `sbx kit validate` result back to the editor.
type kitValidatedMsg struct {
	ok    bool
	lines []string
}

// validateKit checks the kit against the host sbx rather than a second local
// implementation of Docker's experimental schema.
func (m Model) validateKit() (tea.Model, tea.Cmd) {
	kit := m.kitEditor.kit
	spec, err := kit.ToSpec()
	if err != nil {
		m.kitEditor.status = err.Error()
		return m, nil
	}
	d := m.daemon
	if d == nil {
		m.kitEditor.status = "no daemon connected to validate against"
		return m, nil
	}
	m.kitEditor.validating = true
	m.kitEditor.status = ""
	return m, func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		resp, err := d.ValidateKit(ctx, spec)
		if err != nil {
			return kitValidatedMsg{ok: false, lines: []string{err.Error()}}
		}
		lines := resp.GetErrors()
		if resp.GetOk() {
			lines = resp.GetWarnings()
		}
		return kitValidatedMsg{ok: resp.GetOk(), lines: lines}
	}
}

func (m Model) applyKitValidation(msg kitValidatedMsg) (tea.Model, tea.Cmd) {
	m.kitEditor.validating = false
	m.kitEditor.validOK = msg.ok
	m.kitEditor.validation = msg.lines
	return m, nil
}

// ---------- view ----------

func (m Model) viewKitEditor() string {
	if m.kitEditor.form != nil {
		return m.kitEditor.form.View() + m.kitEditorStatusLine()
	}
	if m.kitEditor.inSection {
		return m.viewKitSection()
	}

	title := "New kit"
	if m.kitEditor.editing != "" {
		title = "Edit kit " + m.kitEditor.kit.Name
	}
	rows := []string{sectionStyle.Render(title), ""}
	for s := kitSection(0); s < secCount; s++ {
		cursor := "  "
		label := s.title()
		if s == m.kitEditor.section {
			cursor = cursorBarStyle.Render("▌ ")
			label = selectedStyle.Render(label)
		}
		rows = append(rows, cursor+pad(label, 34)+dimStyle.Render(m.kitSectionCount(s))+"  "+dimStyle.Render(s.blurb()))
	}
	rows = append(rows, "", dimStyle.Render("kind: mixin · schemaVersion: 1"))
	return lipgloss.JoinVertical(lipgloss.Left, rows...) + m.kitEditorStatusLine()
}

func (m Model) viewKitSection() string {
	s := m.kitEditor.section
	rows := []string{sectionStyle.Render(s.title()), dimStyle.Render(s.blurb()), ""}
	n := m.kitSectionLen()
	if n == 0 {
		rows = append(rows, dimStyle.Render("  (none) — press a to add"))
	}
	for i := 0; i < n; i++ {
		cursor := "  "
		label := m.kitItemLabel(s, i)
		if i == m.kitEditor.item {
			cursor = cursorBarStyle.Render("▌ ")
			label = selectedStyle.Render(label)
		}
		rows = append(rows, cursor+label)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...) + m.kitEditorStatusLine()
}

func (m Model) kitEditorStatusLine() string {
	var out []string
	if m.kitEditor.status != "" {
		out = append(out, "", statusErrStyle.Render(m.kitEditor.status))
	}
	if m.kitEditor.validating {
		out = append(out, "", dimStyle.Render("validating against sbx…"))
	}
	if m.kitEditor.validation != nil || m.kitEditor.validOK {
		head := statusErrStyle.Render("invalid kit")
		if m.kitEditor.validOK {
			head = statusOKStyle.Render("kit is valid")
		}
		out = append(out, "", head)
		for _, l := range m.kitEditor.validation {
			out = append(out, dimStyle.Render("  "+l))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "\n" + lipgloss.JoinVertical(lipgloss.Left, out...)
}

func (m Model) kitSectionLen() int {
	c := m.kitEditor.kit.Commands
	if c == nil {
		return 0
	}
	switch m.kitEditor.section {
	case secInstall:
		return len(c.Install)
	case secStartup:
		return len(c.Startup)
	case secInitFiles:
		return len(c.InitFiles)
	}
	return 0
}

// kitSectionCount renders a section's item count for the section list.
func (m Model) kitSectionCount(s kitSection) string {
	k := m.kitEditor.kit
	n := 0
	switch s {
	case secInstall:
		if k.Commands != nil {
			n = len(k.Commands.Install)
		}
	case secStartup:
		if k.Commands != nil {
			n = len(k.Commands.Startup)
		}
	case secInitFiles:
		if k.Commands != nil {
			n = len(k.Commands.InitFiles)
		}
	case secNetwork:
		if k.Network != nil {
			n = len(k.Network.AllowedDomains) + len(k.Network.DeniedDomains)
		}
	case secEnvironment:
		if k.Environment != nil {
			n = len(k.Environment.Variables) + len(k.Environment.ProxyManaged)
		}
	case secCredentials:
		if k.Credentials != nil {
			n = len(k.Credentials.Sources)
		}
	case secIdentity:
		if k.Name != "" {
			return "✓"
		}
		return "—"
	case secAgentContext:
		if k.AgentContext != "" {
			return "✓"
		}
		return "—"
	}
	if n == 0 {
		return "—"
	}
	return strconv.Itoa(n)
}

func (m Model) kitItemLabel(s kitSection, i int) string {
	c := m.kitEditor.kit.Commands
	if c == nil {
		return ""
	}
	switch s {
	case secInstall:
		it := c.Install[i]
		return truncate(it.Command, 60) + dimStyle.Render(userSuffix(it.User, "0"))
	case secStartup:
		it := c.Startup[i]
		label := truncate(strings.Join(it.Command, " "), 60)
		if it.Background {
			label += dimStyle.Render(" &")
		}
		return label + dimStyle.Render(userSuffix(it.User, "1000"))
	case secInitFiles:
		it := c.InitFiles[i]
		return truncate(it.Path, 60) + dimStyle.Render("  "+defaultStr(it.Mode, "0644"))
	}
	return ""
}

// ---------- helpers ----------

func (m Model) newKitForm(fields ...huh.Field) *huh.Form {
	return huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huhTheme()).WithShowHelp(true).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight())
}

// splitLines turns a text-area value into a trimmed, non-empty list.
func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parseEnv reads KEY=value lines.
func parseEnv(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, l := range splitLines(s) {
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, errKitLine("expected KEY=value", l)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

func joinEnv(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+m[k])
	}
	return strings.Join(lines, "\n")
}

// parseCredentials reads `service=ENV[,ENV]` lines.
func parseCredentials(s string) (map[string]store.KitCredentialSource, error) {
	out := map[string]store.KitCredentialSource{}
	for _, l := range splitLines(s) {
		svc, envs, ok := strings.Cut(l, "=")
		svc = strings.TrimSpace(svc)
		if !ok || svc == "" || strings.TrimSpace(envs) == "" {
			return nil, errKitLine("expected service=ENV_VAR", l)
		}
		var list []string
		for _, e := range strings.Split(envs, ",") {
			if e = strings.TrimSpace(e); e != "" {
				list = append(list, e)
			}
		}
		out[svc] = store.KitCredentialSource{Env: list}
	}
	return out, nil
}

func joinCredentials(m map[string]store.KitCredentialSource) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+strings.Join(m[k].Env, ","))
	}
	return strings.Join(lines, "\n")
}

// defaultStr returns s, or def when s is empty — used to show sbx's documented
// default in a form rather than a blank field.
func defaultStr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// omitDefault is defaultStr's inverse: a value equal to sbx's default is stored as
// "" so it is omitted from spec.yaml rather than restated.
func omitDefault(s, def string) string {
	s = strings.TrimSpace(s)
	if s == def {
		return ""
	}
	return s
}

func userSuffix(user, def string) string {
	u := defaultStr(user, def)
	switch u {
	case "0":
		return "  root"
	case "1000":
		return "  agent"
	}
	return "  uid " + u
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func errKitLine(what, line string) error {
	return &kitLineError{what: what, line: line}
}

type kitLineError struct{ what, line string }

func (e *kitLineError) Error() string { return e.what + ": " + strconv.Quote(e.line) }
