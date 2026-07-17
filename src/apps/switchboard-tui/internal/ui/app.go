// Package ui is the Bubble Tea client. It presents a sandbox list and the
// launch/config/hosts/groups/notification flows driving the local daemon. The
// daemon is reached through the Daemon interface so the UI is testable with a
// fake (teatest). Rendering is built on Charm's bubbles (list, textinput,
// textarea, spinner, help), lipgloss (styling), and huh (forms).
package ui

import (
	"context"
	"fmt"
	"io"
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
	// Refresh re-seeds a sandbox's workspace from its recorded sources and
	// restarts it (feature 004, FR-030). DESTRUCTIVE — gate it behind a
	// confirmation.
	Refresh(ctx context.Context, id string, onUpdate func(client.LaunchUpdate)) (*pb.Sandbox, error)
	// AddKit attaches a kit to an existing sandbox (`sbx kit add`, FR-033); sbx
	// restarts it to apply. ValidateKit checks a kit against the host sbx (FR-034).
	AddKit(ctx context.Context, id string, ref *pb.KitRef, onUpdate func(client.LaunchUpdate)) (*pb.Sandbox, error)
	ValidateKit(ctx context.Context, spec *pb.KitSpec) (*pb.ValidateKitResponse, error)
	// SetTag sets/clears a sandbox's mutable purpose tag (US5, FR-021..024).
	SetTag(ctx context.Context, id, tag string) (*pb.Sandbox, error)
	// AttachTerminal attaches to a sandbox's persistent session, streaming
	// snapshot + live PTY bytes into sink (US2, feature 003).
	AttachTerminal(ctx context.Context, sandboxID string, kind client.AttachKind, cols, rows uint32, sink io.Writer) (client.TermSession, error)
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
	// WorkspaceRoot is the daemon's controlled folder on ITS host (FR-006); the
	// launch browser uses it as the starting directory when targeting a remote
	// host, whose filesystem the client cannot read directly.
	WorkspaceRoot() string
}

type screen int

const (
	screenList screen = iota
	screenLaunch
	screenConfirm
	screenRename
	screenTag
	screenTerminal
	screenConfigEditor
	screenConfigPicker
	screenKitPicker
	screenKitEditor
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

	// launching holds optimistic placeholders for launches still copying/booting,
	// keyed by a client-generated temp id. They render as CREATING rows (allRows
	// appends them) and are kept separate from sandboxes/hostAgg so a list reload
	// never wipes them. launchSeq stamps a stable order onto concurrent launches.
	launching map[string]*launchInFlight
	launchSeq int

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

	// US5: tag editor (mirrors rename).
	tagInput textinput.Model
	tagID    string
	tagHost  string

	// confirm backs the modal yes/no gate in front of destructive actions
	// (feature 004); valid only while screen == screenConfirm.
	confirm confirmState

	// Agent kits (feature 004): the client-side store plus the picker/editor state.
	kits      *store.KitStore
	kitPicker kitPickerState
	kitEditor kitEditorState

	// US2/US3: in-place persistent terminal view + tracking of external terminals.
	term    termState
	extTerm map[string]*extTerminal // sandbox id -> spawned external terminal

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

	tagTi := textinput.New()
	tagTi.Prompt = "› "
	tagTi.PromptStyle = selectedStyle
	tagTi.Cursor.Style = cursorBarStyle
	tagTi.CharLimit = 64 // matches the daemon's tag cap (research.md R6)

	m := Model{
		daemon:      daemon,
		srcRoot:     srcRoot,
		screen:      screenList,
		notifier:    notify.NoopNotifier{},
		keys:        newKeyMap(),
		help:        newHelp(),
		spinner:     sp,
		rename:      ti,
		tagInput:    tagTi,
		extTerm:     map[string]*extTerminal{},
		width:       80,
		height:      24,
		busy:        map[string]string{},
		launching:   map[string]*launchInFlight{},
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

// WithKits attaches the client-side agent-kit store (feature 004). Kits are owned
// client-side, so one kit is usable against every connected host.
func (m Model) WithKits(ks *store.KitStore) Model {
	m.kits = ks
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

// browseMsg carries one directory listing fetched from a REMOTE target host's
// daemon for the launch source browser (the local host is browsed synchronously).
type browseMsg struct {
	host    string
	dir     string
	entries []fsEntry
	err     string
}
type listDataMsg struct {
	hosts  []client.HostSandboxes
	groups []store.Group
}

// launchProgressMsg carries one streamed progress frame for the launch identified
// by its optimistic placeholder id.
type launchProgressMsg struct {
	id     string
	update client.LaunchUpdate
}
type launchResultMsg struct {
	id      string
	sb      *pb.Sandbox
	blocked *pb.ResourceReport
	err     error
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
		if m.screen == screenTerminal {
			m.resizeTerminal()
		}
		return m, nil

	case termOpenedMsg:
		return m.handleTermOpened(msg)
	case termUpdateMsg:
		// New PTY output arrived; re-render and keep listening while attached.
		if m.screen == screenTerminal && m.term.session != nil {
			return m, m.waitTermUpdateCmd()
		}
		return m, nil
	case termClosedMsg:
		return m.handleTermClosed(msg)
	case extTermClosedMsg:
		// The external terminal window was closed; forget it so a later `T` opens
		// a fresh one rather than treating the dead process as still attached. Match
		// on the exact process so a terminal reopened for the same sandbox survives.
		if et, ok := m.extTerm[msg.sandboxID]; ok && et.proc == msg.proc {
			delete(m.extTerm, msg.sandboxID)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// Re-render the list so spinners advance each frame (item strings are
		// static, so they must be rebuilt to animate): both busy-action rows and
		// any row whose agent is currently working.
		if m.screen == screenList && m.list.FilterState() != list.Filtering && (len(m.busy) > 0 || len(m.launching) > 0 || m.anyAgentWorking()) {
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

	case kitsMsg:
		return m.applyKits(msg)

	case kitValidatedMsg:
		return m.applyKitValidation(msg)

	case hostsMsg:
		return m.applyHosts(msg)

	case browseMsg:
		return m.applyBrowse(msg)

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
		return m.handleLaunchProgress(msg)

	case launchResultMsg:
		return m.handleLaunchResult(msg)

	case updateAvailableMsg:
		m.latestVersion = msg.latest
		if hint := updateHint(msg.latest, m.clientVersion); hint != "" {
			m.updateBanner = hint
			m.notifier.Notify("Switchboard update available", msg.latest)
		}
		return m, nil

	case updateResultMsg:
		return m.applyUpdateResult(msg)

	case tea.MouseMsg:
		// The terminal view consumes the wheel to scroll its own history; other
		// screens forward it (e.g. the sandbox list scrolls with the wheel).
		if m.screen == screenTerminal {
			return m.updateTerminalMouse(msg)
		}
		return m.forward(msg)

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
	case screenTag:
		m.tagInput, cmd = m.tagInput.Update(msg)
	case screenConfigPicker:
		m.picker.list, cmd = m.picker.list.Update(msg)
	case screenKitPicker:
		m.kitPicker.list, cmd = m.kitPicker.list.Update(msg)
	case screenKitEditor:
		// Only forward once a form is open; the section/item lists are rendered
		// directly and have no async work of their own.
		if m.kitEditor.form != nil {
			return m.advanceKitForm(msg)
		}
		return m, nil
	case screenHosts:
		switch {
		case m.hosts.adding:
			m.hosts.input, cmd = m.hosts.input.Update(msg)
		case m.hosts.connecting:
			m.hosts.pwInput, cmd = m.hosts.pwInput.Update(msg)
		default:
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
	case screenConfirm:
		return m.updateConfirmKey(msg)
	case screenRename:
		return m.updateRenameKey(msg)
	case screenTag:
		return m.updateTagKey(msg)
	case screenTerminal:
		return m.updateTerminalKey(msg)
	case screenConfigEditor:
		return m.updateEditorKey(msg)
	case screenConfigPicker:
		return m.updatePickerKey(msg)
	case screenKitPicker:
		return m.updateKitPickerKey(msg)
	case screenKitEditor:
		return m.updateKitEditorKey(msg)
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

	// The destructive-action gate floats over the list it refers to, so the user can
	// still see the row they are about to act on (feature 004).
	if m.screen == screenConfirm {
		bg := m.chrome(m.viewList(), m.confirmHelp())
		return overlayCenter(bg, m.confirmModal(), m.width, m.height)
	}

	// The in-place terminal view takes the full body (US2).
	if m.screen == screenTerminal {
		return m.chrome(m.viewTerminal(), m.terminalHelp())
	}

	var body string
	var hb helpBindings
	switch m.screen {
	case screenRename:
		body, hb = m.viewRename(), m.renameHelp()
	case screenTag:
		body, hb = m.viewTag(), m.tagHelp()
	case screenConfigEditor:
		body, hb = m.viewEditor(), m.editorHelp()
	case screenConfigPicker:
		body, hb = m.viewPicker(), m.pickerHelp()
	case screenKitPicker:
		body, hb = m.kitPicker.list.View(), m.kitPickerHelp()
	case screenKitEditor:
		body, hb = m.viewKitEditor(), m.kitEditorHelp()
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
