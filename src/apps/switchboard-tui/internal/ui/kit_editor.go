package ui

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
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

// kitSection identifies a top-level area of the spec (kit schema version 2).
type kitSection int

const (
	secIdentity kitSection = iota
	secInstall
	secStartup
	secSetupFiles
	secNetwork
	secEnvironment
	secCredentials
	secAgentInstructions
	secEscapeHatch
	secServices
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
	case secSetupFiles:
		return "Setup files"
	case secNetwork:
		return "Network permissions"
	case secEnvironment:
		return "Environment"
	case secCredentials:
		return "Credential services"
	case secAgentInstructions:
		return "Agent instructions"
	case secEscapeHatch:
		return "Escape-hatch commands"
	case secServices:
		return "Services"
	}
	return ""
}

func (s kitSection) blurb() string {
	switch s {
	case secIdentity:
		return "name, display name, description, version, base agent"
	case secInstall:
		return "run once at creation (sh -c)"
	case secStartup:
		return "run at every start (argv, idempotent)"
	case secSetupFiles:
		return "written into the container at every start"
	case secNetwork:
		return "domains the sandbox may reach / is blocked from"
	case secEnvironment:
		return "container variables"
	case secCredentials:
		return "services the kit needs; values are bound on the host"
	case secAgentInstructions:
		return "markdown written to the agent's kit memory"
	case secEscapeHatch:
		return "commands the agent may run on the host (switchboard-owned)"
	case secServices:
		return "long-running services you can start + forward (switchboard-owned)"
	}
	return ""
}

// itemized reports whether a section is a list of items (each edited in its own
// form) rather than a single scalar form.
func (s kitSection) itemized() bool {
	return s == secInstall || s == secStartup || s == secSetupFiles || s == secCredentials || s == secEscapeHatch || s == secServices
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
	name, displayName, description, version, requiresAgent string
	allowed, denied                                        string
	vars                                                   string
	agentInstructions                                      string

	// credential item fields (schema 2 `credentials[]`; feature 008). credInject is
	// one `domain | header | format` line per injection target, parsed on apply.
	credService, credDesc, credEnvName, credInject string
	credRequired, credProxyManaged                 bool

	// item-form fields
	itemCommand, itemUser, itemDesc string
	itemBackground                  bool
	itemPath, itemContent, itemMode string
	itemOnlyIfMissing               bool

	// escape-hatch item fields (feature 005)
	ehName, ehWhenToUse, ehWorkingDir, ehMaxDuration string
	ehRequiresApproval                               bool
	// ehSubcommands and ehWorkspaces are newline-separated lists in the form;
	// ehArgsPattern is a raw regex. Parsed into slices on apply.
	ehSubcommands, ehArgsPattern, ehWorkspaces string

	// service item fields (feature 006). svcPort and svcReadiness are strings so a
	// non-numeric entry can be rejected with a message instead of silently zeroing.
	svcName, svcPort, svcWorkingDir, svcReadiness string
	svcOnHost, svcIsWebsite                       bool
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
	// migration is non-nil when the kit was translated from the legacy schema on
	// load (feature 008, FR-092); shown as a banner until the kit is saved.
	migration *store.Migration
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
	formAgentInstructions
	formInstall
	formStartup
	formInitFile
	formCredential
	formEscapeHatch
	formService
)

// enterKitEditor opens the editor on an existing kit, or a blank one when nil.
func (m Model) enterKitEditor(k *store.Kit) (tea.Model, tea.Cmd) {
	st := kitEditorState{formItem: -1}
	if k != nil {
		if k.Unsupported != "" {
			m.status = "cannot edit " + k.Name + ": " + k.Unsupported
			return m, nil
		}
		st.kit = *k // by value: an abandoned edit must not touch the stored kit
		st.editing = k.ID()
		// Re-read the stored file to learn whether it was migrated from the legacy
		// schema, so the editor can say what was carried and dropped (FR-092).
		if m.kits != nil {
			if _, mig, err := m.kits.GetWithMigration(k.ID()); err == nil && mig != nil {
				st.migration = mig
			}
		}
	}
	m.kitEditor = st
	m.screen = screenKitEditor
	return m, nil
}

func (m Model) kitEditorHelp() helpBindings {
	switch {
	case m.kitEditor.form != nil:
		return helpBindings{hkey("tab", "next"), hkey("enter", "new line"), hkey("ctrl+s", "apply"), hkey("esc", "cancel")}
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
	case secAgentInstructions:
		return m.openKitForm(formAgentInstructions, m.agentInstructionsForm())
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
	k := m.kitEditor.kit
	v.name, v.displayName, v.description, v.version = k.Name, k.DisplayName, k.Description, k.Version
	if k.Requires != nil {
		v.requiresAgent = k.Requires.Agent
	}
	return m.newKitForm(
		huh.NewInput().Key("name").Title("Name").
			Description("Kit id — lowercase, hyphens. Also the directory name.").Value(&v.name),
		huh.NewInput().Key("displayName").Title("Display name").Value(&v.displayName),
		huh.NewInput().Key("description").Title("Description").Value(&v.description),
		huh.NewInput().Key("version").Title("Version").Description("Optional kit version, e.g. 1.0.0.").Value(&v.version),
		huh.NewInput().Key("requiresAgent").Title("Base agent").
			Description("Optional. Pins the agent this mixin is designed for (e.g. claude).").Value(&v.requiresAgent),
	)
}

func (m *Model) networkForm() *huh.Form {
	v := m.kitEditor.vals
	if p := m.kitEditor.kit.Permissions; p != nil && p.Network != nil {
		v.allowed = strings.Join(p.Network.Allow, "\n")
		v.denied = strings.Join(p.Network.Deny, "\n")
	}
	return m.newKitForm(
		huh.NewText().Key("allowed").Title("Allow").
			Description("Domains the sandbox may reach, one per line (wildcards allowed). Credential\ninjection domains must be listed here too.").Value(&v.allowed),
		huh.NewText().Key("denied").Title("Deny").
			Description("One per line. Deny wins over allow, including across other kits.").Value(&v.denied),
	)
}

func (m *Model) environmentForm() *huh.Form {
	v := m.kitEditor.vals
	if e := m.kitEditor.kit.Environment; e != nil {
		v.vars = joinEnv(e.Variables)
	}
	return m.newKitForm(
		huh.NewText().Key("vars").Title("Variables").
			Description("KEY=value, one per line. Set directly in the container.").Value(&v.vars),
	)
}

func (m *Model) agentInstructionsForm() *huh.Form {
	v := m.kitEditor.vals
	if a := m.kitEditor.kit.AgentInstructions; a != nil {
		v.agentInstructions = a.Content
	}
	return m.newKitForm(
		huh.NewText().Key("agentInstructions").Title("Agent instructions").
			Description("Markdown the runtime writes into the agent's kit memory.").Value(&v.agentInstructions),
	)
}

// ---------- item forms ----------

// openKitItemForm opens the form for one item; idx < 0 appends a new one.
func (m Model) openKitItemForm(idx int) (tea.Model, tea.Cmd) {
	m.kitEditor.formItem = idx
	m.kitEditor.vals = &kitFormVals{}
	v := m.kitEditor.vals
	setup := m.kitEditor.kit.Setup
	switch m.kitEditor.section {
	case secInstall:
		var it store.KitInstallCommand
		if setup != nil && idx >= 0 && idx < len(setup.Install) {
			it = setup.Install[idx]
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
		if setup != nil && idx >= 0 && idx < len(setup.Startup) {
			it = setup.Startup[idx]
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
	case secSetupFiles:
		var it store.KitInitFile
		if setup != nil && idx >= 0 && idx < len(setup.Files) {
			it = setup.Files[idx]
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
	case secCredentials:
		var it store.KitCredential
		if idx >= 0 && idx < len(m.kitEditor.kit.Credentials) {
			it = m.kitEditor.kit.Credentials[idx]
		}
		v.credService, v.credDesc, v.credRequired = it.Service, it.Description, it.Required
		if it.APIKey != nil {
			v.credEnvName, v.credProxyManaged = it.APIKey.Name, it.APIKey.ProxyManaged
			v.credInject = joinInject(it.APIKey.Inject)
		}
		return m.openKitForm(formCredential, m.newKitForm(
			huh.NewNote().Title("Values stay on the host").
				Description("A kit declares which credential a service needs and how it is injected.\nThe value itself is supplied and bound on the host (sbx secret set)."),
			huh.NewInput().Key("service").Title("Service").
				Description("kebab-case id, e.g. github. Matches the secret stored on the host.").Value(&v.credService),
			huh.NewInput().Key("description").Title("Description").Value(&v.credDesc),
			huh.NewConfirm().Key("required").Title("Required").
				Description("On = the agent cannot do its job without it (the runtime warns when unbound).").Value(&v.credRequired),
			huh.NewInput().Key("envName").Title("API-key env var").
				Description("Variable name the agent sees, e.g. GITHUB_TOKEN. Blank = no API-key mechanism.").Value(&v.credEnvName),
			huh.NewConfirm().Key("proxyManaged").Title("Proxy-managed").
				Description("On = the container only sees a placeholder; the proxy injects the real value.").Value(&v.credProxyManaged),
			huh.NewText().Key("inject").Title("Inject into").
				Description("One target per line: domain | header | format (format has one %s, e.g. Bearer %s).\nEach domain must also be allowed under Network permissions.").Value(&v.credInject),
		))
	case secEscapeHatch:
		var it store.KitEscapeHatchCommand
		if idx >= 0 && idx < len(m.kitEditor.kit.EscapeHatch) {
			it = m.kitEditor.kit.EscapeHatch[idx]
		}
		v.ehName, v.itemCommand, v.ehWhenToUse = it.Name, it.Command, it.WhenToUse
		v.ehRequiresApproval, v.ehWorkingDir = it.RequiresApproval, it.WorkingDir
		v.ehSubcommands = strings.Join(it.Subcommands, "\n")
		v.ehArgsPattern = it.ArgsPattern
		v.ehWorkspaces = strings.Join(it.Workspaces, "\n")
		if it.MaxDurationSecs > 0 {
			v.ehMaxDuration = strconv.Itoa(int(it.MaxDurationSecs))
		}
		return m.openKitForm(formEscapeHatch, m.newKitForm(
			huh.NewInput().Key("name").Title("Name").
				Description("kebab-case. The agent invokes the command by this name.").Value(&v.ehName),
			huh.NewText().Key("command").Title("Command").
				Description("The fixed command prefix, run on the host via sh -c. The agent cannot change it.").Value(&v.itemCommand),
			huh.NewText().Key("whenToUse").Title("When to use").
				Description("Plain-language guidance the agent's rule shows for this command.").Value(&v.ehWhenToUse),
			huh.NewText().Key("subcommands").Title("Allowed subcommands (one per line)").
				Description("Optional. The agent may pass one of these as arguments (e.g. install, dev). Leave blank for none.").Value(&v.ehSubcommands),
			huh.NewInput().Key("argsPattern").Title("Args regex (advanced)").
				Description("Optional alternative to subcommands. WARNING: a loose regex lets the agent pass more arguments to the command and lowers safety.").Value(&v.ehArgsPattern),
			huh.NewText().Key("workspaces").Title("Targetable workspaces (one per line)").
				Description("Optional. Workspace-relative paths or globs (e.g. src/apps/*). If set, the agent picks one with --workspace.").Value(&v.ehWorkspaces),
			huh.NewConfirm().Key("requiresApproval").Title("Requires approval").
				Description("On = the developer must approve each run. Off = auto-run.").Value(&v.ehRequiresApproval),
			huh.NewInput().Key("workingDir").Title("Default working dir").
				Description("Optional, relative to the workspace. Used when no targetable workspaces are set.").Value(&v.ehWorkingDir),
			huh.NewInput().Key("maxDuration").Title("Max duration (seconds)").
				Description("Optional; blank = 30-minute default.").Value(&v.ehMaxDuration),
		))
	case secServices:
		var it store.KitService
		if idx >= 0 && idx < len(m.kitEditor.kit.Services) {
			it = m.kitEditor.kit.Services[idx]
		}
		v.svcName, v.itemCommand = it.Name, it.Command
		v.svcOnHost, v.svcIsWebsite, v.svcWorkingDir = it.OnHost, it.IsWebsite, it.WorkingDir
		if it.ListenPort > 0 {
			v.svcPort = strconv.Itoa(int(it.ListenPort))
		}
		if it.ReadinessTimeoutSecs > 0 {
			v.svcReadiness = strconv.Itoa(int(it.ReadinessTimeoutSecs))
		}
		return m.openKitForm(formService, m.newKitForm(
			huh.NewInput().Key("name").Title("Name").
				Description("kebab-case, e.g. web. You start the service by this name.").Value(&v.svcName),
			huh.NewText().Key("command").Title("Start command").
				Description("Run via sh -c. Must listen on ALL interfaces (e.g. --host 0.0.0.0), or it\nwill be unreachable from outside its environment.").Value(&v.itemCommand),
			huh.NewInput().Key("listenPort").Title("Listen port").
				Description("The port THIS COMMAND binds in its own environment — not the one you\nconnect to. A free local port is assigned for you when it starts.").Value(&v.svcPort),
			huh.NewConfirm().Key("onHost").Title("Run on the host").
				Description("Off = inside the sandbox. On = on the host supervising it, in this\nsandbox's workspace. On-host commands may use {{port}} for a fresh port.").Value(&v.svcOnHost),
			huh.NewConfirm().Key("isWebsite").Title("Serves a website").
				Description("On = offer to open it in a browser. Off = show a copyable address.").Value(&v.svcIsWebsite),
			huh.NewInput().Key("workingDir").Title("Working dir").
				Description("Optional, relative to the workspace.").Value(&v.svcWorkingDir),
			huh.NewInput().Key("readiness").Title("Readiness timeout (seconds)").
				Description("Optional; blank = 60s. How long it may take to start listening.").Value(&v.svcReadiness),
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
		k.Version = strings.TrimSpace(v.version)
		if agent := strings.TrimSpace(v.requiresAgent); agent != "" {
			k.Requires = &store.KitRequires{Agent: agent}
		} else {
			k.Requires = nil
		}
	case formNetwork:
		allowed, denied := splitLines(v.allowed), splitLines(v.denied)
		if len(allowed) == 0 && len(denied) == 0 {
			k.Permissions = nil
		} else {
			k.Permissions = &store.KitPermissions{Network: &store.KitNetworkPerms{Allow: allowed, Deny: denied}}
		}
	case formEnvironment:
		vars, err := parseEnv(v.vars)
		if err != nil {
			m.kitEditor.status = err.Error()
			return m, nil
		}
		if len(vars) == 0 {
			k.Environment = nil
		} else {
			k.Environment = &store.KitEnvironment{Variables: vars}
		}
	case formAgentInstructions:
		if content := strings.TrimSpace(v.agentInstructions); content != "" {
			k.AgentInstructions = &store.KitAgentInstructions{Content: content}
		} else {
			k.AgentInstructions = nil
		}
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
		m.ensureSetup()
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.Setup.Install) {
			k.Setup.Install[i] = it
		} else {
			k.Setup.Install = append(k.Setup.Install, it)
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
		m.ensureSetup()
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.Setup.Startup) {
			k.Setup.Startup[i] = it
		} else {
			k.Setup.Startup = append(k.Setup.Startup, it)
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
		m.ensureSetup()
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.Setup.Files) {
			k.Setup.Files[i] = it
		} else {
			k.Setup.Files = append(k.Setup.Files, it)
		}
	case formCredential:
		service := strings.TrimSpace(v.credService)
		if service == "" {
			m.kitEditor.status = "service is required"
			return m, nil
		}
		inject, err := parseInject(v.credInject)
		if err != nil {
			m.kitEditor.status = err.Error()
			return m, nil
		}
		it := store.KitCredential{Service: service, Description: strings.TrimSpace(v.credDesc), Required: v.credRequired}
		if env := strings.TrimSpace(v.credEnvName); env != "" {
			it.APIKey = &store.KitAPIKey{Name: env, ProxyManaged: v.credProxyManaged, Inject: inject}
		} else if len(inject) > 0 || v.credProxyManaged {
			m.kitEditor.status = "an API-key env var name is required for proxy-managed or injected credentials"
			return m, nil
		}
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.Credentials) {
			k.Credentials[i] = it
		} else {
			k.Credentials = append(k.Credentials, it)
		}
	case formEscapeHatch:
		name, cmd := strings.TrimSpace(v.ehName), strings.TrimSpace(v.itemCommand)
		if name == "" || cmd == "" {
			m.kitEditor.status = "name and command are required"
			return m, nil
		}
		var maxSecs uint32
		if d := strings.TrimSpace(v.ehMaxDuration); d != "" {
			n, err := strconv.Atoi(d)
			if err != nil || n < 0 {
				m.kitEditor.status = "max duration must be a non-negative number of seconds"
				return m, nil
			}
			maxSecs = uint32(n)
		}
		it := store.KitEscapeHatchCommand{
			Name:             name,
			Command:          cmd,
			WhenToUse:        strings.TrimSpace(v.ehWhenToUse),
			RequiresApproval: v.ehRequiresApproval,
			WorkingDir:       strings.TrimSpace(v.ehWorkingDir),
			MaxDurationSecs:  maxSecs,
			Subcommands:      splitLines(v.ehSubcommands),
			ArgsPattern:      strings.TrimSpace(v.ehArgsPattern),
			Workspaces:       splitLines(v.ehWorkspaces),
		}
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.EscapeHatch) {
			k.EscapeHatch[i] = it
		} else {
			k.EscapeHatch = append(k.EscapeHatch, it)
		}
	case formService:
		name, cmd := strings.TrimSpace(v.svcName), strings.TrimSpace(v.itemCommand)
		if name == "" || cmd == "" {
			m.kitEditor.status = "name and command are required"
			return m, nil
		}
		port, err := parsePort(v.svcPort)
		if err != nil {
			m.kitEditor.status = "listen port: " + err.Error()
			return m, nil
		}
		var readiness uint32
		if d := strings.TrimSpace(v.svcReadiness); d != "" {
			n, err := strconv.Atoi(d)
			if err != nil || n < 0 {
				m.kitEditor.status = "readiness timeout must be a non-negative number of seconds"
				return m, nil
			}
			readiness = uint32(n)
		}
		it := store.KitService{
			Name:                 name,
			Command:              cmd,
			ListenPort:           port,
			OnHost:               v.svcOnHost,
			IsWebsite:            v.svcIsWebsite,
			WorkingDir:           strings.TrimSpace(v.svcWorkingDir),
			ReadinessTimeoutSecs: readiness,
		}
		if i := m.kitEditor.formItem; i >= 0 && i < len(k.Services) {
			k.Services[i] = it
		} else {
			k.Services = append(k.Services, it)
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

func (m *Model) ensureSetup() {
	if m.kitEditor.kit.Setup == nil {
		m.kitEditor.kit.Setup = &store.KitSetup{}
	}
}

func (m *Model) deleteKitItem(idx int) {
	k := &m.kitEditor.kit
	m.kitEditor.validation = nil
	switch m.kitEditor.section {
	case secServices:
		if idx < len(k.Services) {
			k.Services = append(k.Services[:idx], k.Services[idx+1:]...)
		}
	case secEscapeHatch:
		if idx < len(k.EscapeHatch) {
			k.EscapeHatch = append(k.EscapeHatch[:idx], k.EscapeHatch[idx+1:]...)
		}
	case secCredentials:
		if idx < len(k.Credentials) {
			k.Credentials = append(k.Credentials[:idx], k.Credentials[idx+1:]...)
		}
	case secInstall:
		if k.Setup != nil && idx < len(k.Setup.Install) {
			k.Setup.Install = append(k.Setup.Install[:idx], k.Setup.Install[idx+1:]...)
		}
	case secStartup:
		if k.Setup != nil && idx < len(k.Setup.Startup) {
			k.Setup.Startup = append(k.Setup.Startup[:idx], k.Setup.Startup[idx+1:]...)
		}
	case secSetupFiles:
		if k.Setup != nil && idx < len(k.Setup.Files) {
			k.Setup.Files = append(k.Setup.Files[:idx], k.Setup.Files[idx+1:]...)
		}
	}
}

// ---------- save / validate ----------

func (m Model) saveKit() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.kitEditor.kit.Name) == "" {
		m.kitEditor.status = "name required (Identity)"
		return m, nil
	}
	// Escape-hatch commands are switchboard-owned; validate them client-side (the
	// Docker portion is validated separately via `v`/ValidateKit).
	if errs := m.kitEditor.kit.ValidateEscapeHatch(); len(errs) > 0 {
		m.kitEditor.status = errs[0]
		return m, nil
	}
	// Services are switchboard-owned too (feature 006); same client-side gate.
	if errs := m.kitEditor.kit.ValidateServices(); len(errs) > 0 {
		m.kitEditor.status = errs[0]
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
	if mig := m.kitEditor.migration; mig != nil {
		for _, line := range mig.Summary() {
			rows = append(rows, statusErrStyle.Render("⚠ "+line))
		}
		rows = append(rows, dimStyle.Render("  the file on disk is unchanged until you save (ctrl+s)"), "")
	}
	for s := kitSection(0); s < secCount; s++ {
		cursor := "  "
		label := s.title()
		if s == m.kitEditor.section {
			cursor = cursorBarStyle.Render("▌ ")
			label = selectedStyle.Render(label)
		}
		rows = append(rows, cursor+pad(label, 34)+dimStyle.Render(m.kitSectionCount(s))+"  "+dimStyle.Render(s.blurb()))
	}
	rows = append(rows, "", dimStyle.Render("kind: mixin · schemaVersion: 2"))
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
	k := m.kitEditor.kit
	switch m.kitEditor.section {
	case secServices:
		return len(k.Services)
	case secEscapeHatch:
		return len(k.EscapeHatch)
	case secCredentials:
		return len(k.Credentials)
	}
	if k.Setup == nil {
		return 0
	}
	switch m.kitEditor.section {
	case secInstall:
		return len(k.Setup.Install)
	case secStartup:
		return len(k.Setup.Startup)
	case secSetupFiles:
		return len(k.Setup.Files)
	}
	return 0
}

// kitSectionCount renders a section's item count for the section list.
func (m Model) kitSectionCount(s kitSection) string {
	k := m.kitEditor.kit
	n := 0
	switch s {
	case secInstall:
		if k.Setup != nil {
			n = len(k.Setup.Install)
		}
	case secStartup:
		if k.Setup != nil {
			n = len(k.Setup.Startup)
		}
	case secSetupFiles:
		if k.Setup != nil {
			n = len(k.Setup.Files)
		}
	case secNetwork:
		if k.Permissions != nil && k.Permissions.Network != nil {
			n = len(k.Permissions.Network.Allow) + len(k.Permissions.Network.Deny)
		}
	case secEnvironment:
		if k.Environment != nil {
			n = len(k.Environment.Variables)
		}
	case secCredentials:
		n = len(k.Credentials)
	case secIdentity:
		if k.Name != "" {
			return "✓"
		}
		return "—"
	case secAgentInstructions:
		if k.AgentInstructions != nil && strings.TrimSpace(k.AgentInstructions.Content) != "" {
			return "✓"
		}
		return "—"
	case secEscapeHatch:
		n = len(k.EscapeHatch)
	case secServices:
		n = len(k.Services)
	}
	if n == 0 {
		return "—"
	}
	return strconv.Itoa(n)
}

func (m Model) kitItemLabel(s kitSection, i int) string {
	k := m.kitEditor.kit
	switch s {
	case secServices:
		if i >= len(k.Services) {
			return ""
		}
		it := k.Services[i]
		suffix := fmt.Sprintf("  :%d %s", it.ListenPort, it.Location())
		if it.IsWebsite {
			suffix += " web"
		}
		return truncate(it.Name+": "+it.Command, 48) + dimStyle.Render(suffix)
	case secEscapeHatch:
		if i >= len(k.EscapeHatch) {
			return ""
		}
		it := k.EscapeHatch[i]
		gate := " auto"
		if it.RequiresApproval {
			gate = " approval"
		}
		return truncate(it.Name+": "+it.Command, 56) + dimStyle.Render(gate)
	case secCredentials:
		if i >= len(k.Credentials) {
			return ""
		}
		it := k.Credentials[i]
		label := it.Service
		if it.APIKey != nil {
			label += ": " + it.APIKey.Name
			if it.APIKey.ProxyManaged {
				label += dimStyle.Render(" proxy")
			}
			if n := len(it.APIKey.Inject); n > 0 {
				label += dimStyle.Render(fmt.Sprintf("  → %d %s", n, plural(n, "target", "targets")))
			}
		}
		if it.Required {
			label += dimStyle.Render("  required")
		}
		return label
	}
	if k.Setup == nil {
		return ""
	}
	switch s {
	case secInstall:
		it := k.Setup.Install[i]
		return truncate(it.Command, 60) + dimStyle.Render(userSuffix(it.User, "0"))
	case secStartup:
		it := k.Setup.Startup[i]
		label := truncate(strings.Join(it.Command, " "), 60)
		if it.Background {
			label += dimStyle.Render(" &")
		}
		return label + dimStyle.Render(userSuffix(it.User, "1000"))
	case secSetupFiles:
		it := k.Setup.Files[i]
		return truncate(it.Path, 60) + dimStyle.Render("  "+defaultStr(it.Mode, "0644"))
	}
	return ""
}

// parsePort validates a listen-port entry. A blank or non-numeric port is rejected
// with a message rather than silently becoming 0, which would attach a service that
// can never be reached.
func parsePort(in string) (uint32, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return 0, errors.New("a listen port is required")
	}
	n, err := strconv.Atoi(in)
	if err != nil {
		return 0, errors.New("must be a number")
	}
	if n < 1 || n > 65535 {
		return 0, errors.New("must be between 1 and 65535")
	}
	return uint32(n), nil
}

// ---------- helpers ----------

func (m Model) newKitForm(fields ...huh.Field) *huh.Form {
	return huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huhTheme()).WithShowHelp(true).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).
		WithKeyMap(kitFormKeyMap())
}

// kitFormKeyMap makes Enter insert a newline in the editor's multi-line Text fields
// (subcommands, workspaces, allowed/denied domains, env vars, … — one list entry per
// line) instead of advancing to the next field. huh's default binds Enter to "next"
// and puts newline on alt+enter/ctrl+j, which is unintuitive for these list fields.
//
// Tab still moves between fields; the editor applies the whole form with ctrl+s
// (intercepted before the form sees it), so Enter is freed up for newlines. Enter is
// removed from Text.Next/Submit because huh dispatches those before the textarea's
// newline, and the textarea's newline key is driven from Text.NewLine. Single-line
// Input/Confirm fields keep huh's default (Enter advances).
func kitFormKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Text.NewLine = key.NewBinding(key.WithKeys("enter", "alt+enter", "ctrl+j"), key.WithHelp("enter", "new line"))
	km.Text.Next = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next"))
	km.Text.Prev = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	km.Text.Submit = key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "apply"))
	return km
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

// parseInject reads `domain | header | format` lines (format optional).
func parseInject(s string) ([]store.KitInject, error) {
	var out []store.KitInject
	for _, l := range splitLines(s) {
		parts := strings.Split(l, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
			return nil, errKitLine("expected domain | header | format", l)
		}
		in := store.KitInject{Domain: parts[0], Header: parts[1]}
		if len(parts) == 3 {
			in.Format = parts[2]
		}
		out = append(out, in)
	}
	return out, nil
}

func joinInject(in []store.KitInject) string {
	lines := make([]string, 0, len(in))
	for _, i := range in {
		line := i.Domain + " | " + i.Header
		if i.Format != "" {
			line += " | " + i.Format
		}
		lines = append(lines, line)
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
