// Package ui is the Bubble Tea client. It presents a sandbox list and the
// launch/config/hosts/groups/notification flows driving the local daemon. The
// daemon is reached through the Daemon interface so the UI is testable with a
// fake (teatest). Rendering is built on Charm's bubbles (list, textinput,
// textarea, spinner, progress, help), lipgloss (styling), and huh (forms).
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/notify"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/vscode"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Daemon is the subset of daemon operations the UI needs. *client.Conn
// implements it; tests inject a fake.
type Daemon interface {
	HostID() string
	List(ctx context.Context) ([]*pb.Sandbox, error)
	Launch(ctx context.Context, req *pb.LaunchSandboxRequest, onUpdate func(client.LaunchUpdate)) (*pb.Sandbox, *pb.ResourceReport, error)
	Stop(ctx context.Context, id string) (*pb.Sandbox, error)
	Restart(ctx context.Context, id string) (*pb.Sandbox, error)
	Destroy(ctx context.Context, id string) (bool, error)
	Rename(ctx context.Context, id, name string) (*pb.Sandbox, error)
	ListSources(ctx context.Context, root string, reposOnly bool) ([]*pb.SourceRef, error)
	OptionManifest(ctx context.Context) (*pb.OptionManifest, error)
	// US4: agent prompting + live event subscription.
	PromptAgent(ctx context.Context, id, prompt string) error
	Subscribe(ctx context.Context, replay bool) (client.EventStream, error)
	AckNotifications(ctx context.Context, ids []string) error
	// US5: VS Code open target.
	VSCodeTarget(ctx context.Context, id string) (*pb.VSCodeTarget, error)
	// Distribution/self-update: the daemon's advertised version, and a request
	// to self-update it to a target release (empty = latest).
	DaemonVersion() string
	UpdateDaemon(ctx context.Context, target string, onProgress func(stage, message string)) error
}

type screen int

const (
	screenList screen = iota
	screenLaunch
	screenRename
	screenConfigEditor
	screenConfigPicker
	screenHosts
	screenNotifications
	screenGroups
	screenUpdate
)

// Model is the root Bubble Tea model.
type Model struct {
	daemon   Daemon
	srcRoot  string
	sbxBin   string // host sandbox CLI used to open a sandbox's interactive terminal
	terminal string // external terminal command prefix for the popout terminal (T)

	screen    screen
	sandboxes []*pb.Sandbox
	list      list.Model
	status    string
	err       error
	width     int
	height    int

	// group tab bar on the sandbox list: "All Sandboxes", one per connected
	// remote daemon, and one per user-defined group. Switched with ←/→.
	tabs       []sandboxTab
	tabIndex   int
	hostAgg    []client.HostSandboxes // per-host sandboxes across connected hosts
	userGroups []store.Group

	// loading feedback on the sandbox list while awaiting the daemon: busy maps a
	// sandbox id to the in-flight verb (its row shows a spinner); listLoading is a
	// list-wide refresh in flight (a spinner shows above the list).
	busy        map[string]string
	listLoading bool

	// shared chrome components.
	keys    keyMap
	help    help.Model
	spinner spinner.Model

	// launch wizard state (launch.go)
	launch launchState
	// rename input
	rename     textinput.Model
	renameID   string
	renameHost string

	// US2: saved configurations + sbx option manifest.
	configs *store.ConfigStore
	editor  editorState
	picker  pickerState

	// US3: multi-host over SSH.
	manager    *client.Manager
	hostStore  *store.HostStore
	activeHost string
	hosts      hostsState

	// US4: notifications + live event stream.
	notifier   notify.Notifier
	inbox      []*pb.NotificationEvent
	notifyList list.Model
	unread     int
	sub        client.EventStream
	subErr     error

	// US5: groups + VS Code.
	groupStore *store.GroupStore
	groups     groupsState
	opener     *vscode.Opener

	// self-update: the running client version, the latest release seen, a
	// dismissable banner when an update is available, the in-flight update
	// screen state, and a flag telling main to re-exec the new sxb on exit.
	clientVersion string
	latestVersion string
	updateBanner  string
	update        updateState
	reexec        bool

	quitting bool
}

// New constructs the root model. srcRoot is the directory whose children are
// offered as launch sources.
func New(daemon Daemon, srcRoot string) Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.PromptStyle = selectedStyle
	ti.Cursor.Style = cursorBarStyle

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

	m := Model{
		daemon:      daemon,
		srcRoot:     srcRoot,
		screen:      screenList,
		notifier:    notify.NoopNotifier{},
		keys:        newKeyMap(),
		help:        newHelp(),
		spinner:     sp,
		rename:      ti,
		width:       80,
		height:      24,
		busy:        map[string]string{},
		listLoading: true, // the first list load is in flight until it arrives
	}
	m.help.Width = m.width
	m.list = newSandboxList(m.bodyWidth(), m.mainListHeight())
	return m
}

// WithNotifier attaches the OS desktop notifier (US4). Returns the updated model.
func (m Model) WithNotifier(n notify.Notifier) Model {
	m.notifier = n
	return m
}

// WithSbx sets the host sandbox CLI used to open a sandbox's interactive agent
// terminal (`t` on the list). Defaults to "sbx" when empty.
func (m Model) WithSbx(bin string) Model {
	m.sbxBin = bin
	return m
}

// WithVersion records the running client's build version so the TUI can detect
// a newer release and warn about client/daemon version skew.
func (m Model) WithVersion(v string) Model {
	m.clientVersion = v
	return m
}

// ShouldReexec reports whether main should replace the process with the freshly
// installed sxb binary after the TUI exits (set by a completed self-update).
func (m Model) ShouldReexec() bool { return m.reexec }

// WithTerminal sets the external terminal command prefix used by the popout
// terminal (`T` on the list). Empty disables the popout with a hint.
func (m Model) WithTerminal(cmd string) Model {
	m.terminal = cmd
	return m
}

// WithGroups attaches the group store and VS Code opener (US5).
func (m Model) WithGroups(gs *store.GroupStore, opener *vscode.Opener) Model {
	m.groupStore = gs
	m.opener = opener
	return m
}

// WithConfigs attaches a client-side configuration store, enabling the config
// editor and the launch-from-saved-config flow (US2). Returns the updated model.
func (m Model) WithConfigs(cs *store.ConfigStore) Model {
	m.configs = cs
	return m
}

// WithHosts attaches the multi-host connection manager, known-host store, and the
// id of the initially-active host (US3). Returns the updated model.
func (m Model) WithHosts(mgr *client.Manager, hs *store.HostStore, activeHost string) Model {
	m.manager = mgr
	m.hostStore = hs
	m.activeHost = activeHost
	return m
}

// --- messages ---

type sandboxesMsg []*pb.Sandbox
type statusMsg string
type errMsg struct{ err error }
type manifestMsg struct{ manifest *pb.OptionManifest }
type configsMsg []*store.Configuration
type hostsMsg []client.HostSandboxes
type subOpenedMsg struct{ stream client.EventStream }
type eventMsg struct{ ev *pb.Event }
type eventErrMsg struct{ err error }
type groupsMsg []store.Group
type listDataMsg struct {
	hosts  []client.HostSandboxes
	groups []store.Group
}
type launchProgressMsg client.LaunchUpdate
type launchResultMsg struct {
	sb      *pb.Sandbox
	blocked *pb.ResourceReport
	err     error
}

// agentExitMsg is delivered after an interactive agent terminal session (opened
// with `p`) exits and the TUI is restored.
type agentExitMsg struct {
	name string
	err  error
}

func (e errMsg) Error() string { return e.err.Error() }

// --- commands ---

func ctxTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func (m Model) refreshCmd() tea.Cmd {
	d := m.daemon
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		list, err := d.List(ctx)
		if err != nil {
			return errMsg{err}
		}
		return sandboxesMsg(list)
	}
}

func (m Model) stopCmd(d Daemon, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := d.Stop(ctx, id); err != nil {
			return errMsg{err}
		}
		return statusMsg("stopped " + short(id))
	}
}

func (m Model) restartCmd(d Daemon, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := d.Restart(ctx, id); err != nil {
			return errMsg{err}
		}
		return statusMsg("restarted " + short(id))
	}
}

func (m Model) destroyCmd(d Daemon, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := d.Destroy(ctx, id); err != nil {
			return errMsg{err}
		}
		return statusMsg("destroyed " + short(id))
	}
}

func (m Model) renameCmd(d Daemon, id, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := d.Rename(ctx, id, name); err != nil {
			return errMsg{err}
		}
		return statusMsg("renamed " + short(id))
	}
}

// listDataCmd loads the data backing the group tab bar: the per-host sandbox
// aggregate (for remote-daemon tabs) and the user-defined groups.
func (m Model) listDataCmd() tea.Cmd {
	mgr := m.manager
	gs := m.groupStore
	if mgr == nil && gs == nil {
		return nil
	}
	return func() tea.Msg {
		var hosts []client.HostSandboxes
		if mgr != nil {
			ctx, cancel := ctxTimeout()
			defer cancel()
			hosts = mgr.AggregateSandboxes(ctx)
		}
		var groups []store.Group
		if gs != nil {
			groups, _ = gs.List()
		}
		return listDataMsg{hosts: hosts, groups: groups}
	}
}

// reloadCmd refreshes the active daemon's list and, when multi-host or grouping
// is available, the tab-bar data too.
func (m Model) reloadCmd() tea.Cmd {
	if c := m.listDataCmd(); c != nil {
		return tea.Batch(m.refreshCmd(), c)
	}
	return m.refreshCmd()
}

// Init kicks off the first sandbox list load, opens the event subscription,
// loads the tab-bar data, and starts the shared spinner.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), m.subscribeCmd(), m.listDataCmd(), checkUpdateCmd(), m.spinner.Tick)
}

// Update routes messages by screen.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		m.list.SetSize(m.bodyWidth(), m.mainListHeight())
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// Re-render busy rows so their spinner advances each frame (the list's
		// item strings are static, so they must be rebuilt to animate).
		if m.screen == screenList && len(m.busy) > 0 && m.list.FilterState() != list.Filtering {
			m.refreshListItems()
		}
		return m, cmd

	case sandboxesMsg:
		m.sandboxes = []*pb.Sandbox(msg)
		m.listLoading = false
		m.rebuildTabs()
		m.refreshListItems()
		return m, nil

	case listDataMsg:
		m.hostAgg = msg.hosts
		m.userGroups = msg.groups
		m.listLoading = false
		m.rebuildTabs()
		m.refreshListItems()
		return m, nil

	case statusMsg:
		m.status = string(msg)
		m.err = nil
		// The action finished; clear its per-row spinner and show the list-wide
		// spinner while the follow-up refresh is in flight.
		m.busy = map[string]string{}
		m.listLoading = true
		m.refreshListItems()
		return m, m.reloadCmd()

	case errMsg:
		m.err = msg.err
		m.status = "error: " + msg.err.Error()
		m.busy = map[string]string{}
		m.listLoading = false
		m.refreshListItems()
		return m, nil

	case manifestMsg:
		return m.applyManifest(msg.manifest)

	case configsMsg:
		return m.applyConfigs(msg)

	case hostsMsg:
		return m.applyHosts(msg)

	case subOpenedMsg:
		m.sub = msg.stream
		return m, m.recvCmd(msg.stream)

	case eventMsg:
		return m.handleEvent(msg.ev)

	case eventErrMsg:
		// Subscription dropped; surface quietly. A reconnect (US3) re-subscribes.
		m.subErr = msg.err
		return m, nil

	case groupsMsg:
		return m.applyGroups(msg)

	case launchProgressMsg:
		return m.handleLaunchProgress(client.LaunchUpdate(msg))

	case launchResultMsg:
		return m.handleLaunchResult(msg)

	case agentExitMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "terminal session failed: " + msg.err.Error()
			return m, nil
		}
		// The agent may have changed sandbox state; refresh on return.
		m.err = nil
		m.status = "returned from " + msg.name
		m.listLoading = true
		return m, m.reloadCmd()

	case updateAvailableMsg:
		m.latestVersion = msg.latest
		if hint := updateHint(msg.latest, m.clientVersion); hint != "" {
			m.updateBanner = hint
			m.notifier.Notify("Switchboard update available", msg.latest)
		}
		return m, nil

	case updateResultMsg:
		return m.applyUpdateResult(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	// Non-key follow-up messages (huh field navigation, cursor blink, list
	// filter/spinner ticks) must reach the active screen's component.
	return m.forward(msg)
}

// forward routes an unhandled (non-key) message to the component backing the
// current screen so its internal async work (blink, field advance) completes.
func (m Model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.screen {
	case screenLaunch:
		// The browser is synchronous; only the name field (when editing) has
		// async work (cursor blink) to forward.
		if m.launch.naming {
			m.launch.name, cmd = m.launch.name.Update(msg)
		}
		return m, cmd
	case screenConfigEditor:
		return m.advanceEditorForm(msg)
	case screenRename:
		m.rename, cmd = m.rename.Update(msg)
	case screenConfigPicker:
		m.picker.list, cmd = m.picker.list.Update(msg)
	case screenHosts:
		if m.hosts.adding {
			m.hosts.input, cmd = m.hosts.input.Update(msg)
		} else {
			m.hosts.list, cmd = m.hosts.list.Update(msg)
		}
	case screenGroups:
		if m.groups.adding {
			m.groups.input, cmd = m.groups.input.Update(msg)
		} else {
			m.groups.list, cmd = m.groups.list.Update(msg)
		}
	case screenNotifications:
		m.notifyList, cmd = m.notifyList.Update(msg)
	case screenUpdate:
		// The update screen has no interactive component; advance the spinner.
		m.spinner, cmd = m.spinner.Update(msg)
	default:
		m.list, cmd = m.list.Update(msg)
	}
	return m, cmd
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenLaunch:
		return m.updateLaunchKey(msg)
	case screenRename:
		return m.updateRenameKey(msg)
	case screenConfigEditor:
		return m.updateEditorKey(msg)
	case screenConfigPicker:
		return m.updatePickerKey(msg)
	case screenHosts:
		return m.updateHostsKey(msg)
	case screenNotifications:
		return m.updateNotificationsKey(msg)
	case screenGroups:
		return m.updateGroupsKey(msg)
	case screenUpdate:
		return m.updateUpdateKey(msg)
	default:
		return m.updateListKey(msg)
	}
}

// View renders the active screen wrapped in the shared chrome.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	// The launch flow is a modal layer floating over the list page.
	if m.screen == screenLaunch {
		bg := m.chrome(m.viewList(), m.launchHelp())
		return overlayCenter(bg, m.launchModal(), m.width, m.height)
	}

	var body string
	var hb helpBindings
	switch m.screen {
	case screenRename:
		body, hb = m.viewRename(), m.renameHelp()
	case screenConfigEditor:
		body, hb = m.viewEditor(), m.editorHelp()
	case screenConfigPicker:
		body, hb = m.viewPicker(), m.pickerHelp()
	case screenHosts:
		body, hb = m.viewHosts(), m.hostsHelp()
	case screenNotifications:
		body, hb = m.viewNotifications(), m.notificationsHelp()
	case screenGroups:
		body, hb = m.viewGroups(), m.groupsHelp()
	case screenUpdate:
		body, hb = m.viewUpdate(), m.updateHelpBindings()
	default:
		body, hb = m.viewList(), m.listHelp()
	}
	return m.chrome(body, hb)
}

// --- chrome (header + body + footer) ---

func (m Model) bodyWidth() int {
	if m.width < 20 {
		return 20
	}
	return m.width
}

func (m Model) bodyHeight() int {
	// header (title + blank line) + footer (status + help) ≈ 5 lines.
	h := m.height - 6
	if h < 3 {
		return 3
	}
	return h
}

// mainListHeight leaves room above the sandbox list for the group tab bar and
// below it for the (possibly wrapped) help footer, so nothing is pushed off a
// narrow terminal.
func (m Model) mainListHeight() int {
	extraHelp := m.helpLineCount(m.listHelp()) - 1
	if extraHelp < 0 {
		extraHelp = 0
	}
	banner := 0
	if m.updateBanner != "" {
		banner = 1 // the update banner occupies one line above the list
	}
	if h := m.bodyHeight() - 2 - extraHelp - banner; h >= 3 {
		return h
	}
	return 3
}

// helpLineCount is how many lines the help footer wraps to at the current width.
func (m Model) helpLineCount(hb helpBindings) int {
	return strings.Count(m.renderHelp(hb), "\n") + 1
}

// daemonForHost returns the daemon connection owning a host, so per-sandbox
// actions taken from a remote-daemon or group tab target the right host. Falls
// back to the active daemon when the host is unknown or single-host.
func (m Model) daemonForHost(hostID string) Daemon {
	if m.manager != nil && hostID != "" {
		if hc, ok := m.manager.Get(hostID); ok && hc.State == client.HostConnected && hc.Conn != nil {
			return hc.Conn
		}
	}
	return m.daemon
}

// currentHostID is the host owning the list-selected sandbox.
func (m Model) currentHostID() string {
	if it, ok := m.list.SelectedItem().(listItem); ok && it.host != "" {
		return it.host
	}
	return m.activeHost
}

func (m Model) header() string {
	title := appTitleStyle.Render("⚡ Switchboard")
	right := hostBadgeStyle.Render("host " + m.daemon.HostID())
	if m.unread > 0 {
		right = lipgloss.JoinHorizontal(lipgloss.Top, right, " ", unreadBadgeStyle.Render(fmt.Sprintf("🔔 %d", m.unread)))
	}
	gap := m.width - lipgloss.Width(title) - lipgloss.Width(right)
	if gap < 2 {
		gap = 2
	}
	return headerBarStyle.Render(title + strings.Repeat(" ", gap) + right)
}

func (m Model) footer(hb helpBindings) string {
	var parts []string
	if m.status != "" {
		style := statusOKStyle
		if m.err != nil || strings.HasPrefix(m.status, "error") {
			style = statusErrStyle
		}
		parts = append(parts, style.Render(m.status))
	}
	parts = append(parts, m.renderHelp(hb))
	return "\n" + strings.Join(parts, "\n")
}

// renderHelp lays out the key/desc hints, wrapping onto multiple lines when the
// terminal is too narrow to fit them on one — bubbles' help.View truncates
// instead, which hides commands. Styling matches the help component.
func (m Model) renderHelp(hb helpBindings) string {
	width := m.width
	if width < 20 {
		width = 20
	}
	st := m.help.Styles
	sep := st.ShortSeparator.Render(" • ")
	sepW := lipgloss.Width(sep)

	var lines []string
	var cur strings.Builder
	curW := 0
	for _, b := range hb {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		if h.Key == "" && h.Desc == "" {
			continue
		}
		seg := st.ShortKey.Render(h.Key) + " " + st.ShortDesc.Render(h.Desc)
		segW := lipgloss.Width(seg)
		switch {
		case curW == 0:
			cur.WriteString(seg)
			curW = segW
		case curW+sepW+segW <= width:
			cur.WriteString(sep)
			cur.WriteString(seg)
			curW += sepW + segW
		default:
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(seg)
			curW = segW
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return strings.Join(lines, "\n")
}

func (m Model) chrome(body string, hb helpBindings) string {
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), body, m.footer(hb))
}

// --- rename screen (textinput) ---

func (m Model) enterRename(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	m.screen = screenRename
	m.renameID = sb.GetId()
	m.renameHost = host
	m.rename.SetValue(sb.GetDisplayName())
	m.rename.CursorEnd()
	return m, m.rename.Focus()
}

func (m Model) renameHelp() helpBindings {
	return helpBindings{m.keys.Confirm, m.keys.Cancel}
}

func (m Model) updateRenameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.rename.Blur()
		m.screen = screenList
		return m, nil
	case tea.KeyEnter:
		id, name := m.renameID, strings.TrimSpace(m.rename.Value())
		host := m.renameHost
		m.rename.Blur()
		m.screen = screenList
		if name == "" {
			return m, nil
		}
		m.busy[id] = "renaming"
		m.refreshListItems()
		return m, m.renameCmd(m.daemonForHost(host), id, name)
	}
	var cmd tea.Cmd
	m.rename, cmd = m.rename.Update(msg)
	return m, cmd
}

func (m Model) viewRename() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("Rename sandbox ")+dimStyle.Render(short(m.renameID)),
		"",
		panelStyle.Width(m.bodyWidth()-2).Render(m.rename.View()),
	)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// keyIs reports whether a key message matches a binding (thin wrapper kept for
// readability at call sites).
func keyIs(msg tea.KeyMsg, b key.Binding) bool { return key.Matches(msg, b) }
