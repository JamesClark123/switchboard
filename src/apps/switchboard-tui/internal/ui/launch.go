package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// browseVisible is how many filesystem entries the launch browser shows at once.
const browseVisible = 10

// fsEntry is one row in the launch directory browser.
type fsEntry struct {
	name   string
	path   string
	isDir  bool
	isRepo bool // dir contains a .git (drives SourceRef.IsRepo)
	up     bool // the ".." parent-navigation row
}

// hostChoice is one selectable target host in the launch wizard's host picker.
type hostChoice struct {
	id    string
	label string
}

// launchState backs the launch layer: a filesystem browser that supports
// selecting MULTIPLE directories/files to seed (space toggles), rendered as a
// modal overlaid on the sandbox list. Navigation is →/enter-into-dir, ←/up.
type launchState struct {
	config *store.Configuration // non-nil => launching from a saved config (T042)

	// targetHost is the host the sandbox is created on and whose filesystem the
	// source browser enumerates (US3 multi-host). Empty == the active/local host.
	targetHost string

	dir       string          // directory currently being browsed (on targetHost)
	entries   []fsEntry       // rows of dir (".." first when not at root)
	cursor    int             // highlighted row
	offset    int             // scroll offset into entries
	selected  map[string]bool // chosen host-side paths (the sources to seed)
	srcRepo   map[string]bool // path -> is-a-repo, captured at selection time
	order     []string        // selection order, so sources keep a stable order
	loading   bool            // a remote ListSources RPC is in flight
	loadErr   string          // directory read error, if any
	cloneMode bool            // false => duplicate (default, FR-009)

	// Host picker sub-mode: choose which connected host to create the sandbox on.
	hostPick    bool
	hostCursor  int
	hostChoices []hostChoice

	name   textinput.Model // optional per-host sandbox name (becomes the workspace dir)
	naming bool            // true while editing the name field

	// Agent-kit selection (feature 004, FR-032). Creation is the ONLY point sbx
	// accepts `--kit`, so kits chosen here are passed to `sbx create`; attaching
	// later goes through `sbx kit add`, which restarts the sandbox.
	kitPick   bool            // true while choosing kits
	kitCursor int             // highlighted kit row
	kitAll    []*store.Kit    // kits available to attach
	kitOn     map[string]bool // chosen kit ids
	kitOrder  []string        // selection order — sbx composes stacked kits in order

	// progress carries a pre-launch validation/kit error shown under the browser
	// (e.g. "select at least one directory"). Live launch progress no longer lives
	// here — once a launch starts it detaches to an optimistic list row (see
	// launchInFlight) so the modal closes and the TUI stays usable.
	progress string
}

// launchInFlight is one optimistic, still-streaming launch. Its placeholder shows
// as a CREATING row on the sandbox list (allRows appends it) so the user gets
// immediate feedback and can keep using the TUI — even start further launches —
// while the workspace copies and the container boots. Progress events mutate this
// struct in place and the row re-renders on the shared spinner tick.
type launchInFlight struct {
	tempID   string       // client-generated placeholder id (until the daemon assigns the real one)
	host     string       // target host id the sandbox is being created on (for row attribution)
	seq      int          // stable ordering among concurrent launches
	sb       *pb.Sandbox  // the placeholder row (State = CREATING)
	progress string       // latest human-readable progress ("copying 45%")
	ch       chan tea.Msg // per-launch progress/result channel
}

// newNameInput builds the optional sandbox-name field for the launch overlay.
func newNameInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "optional — unique per host, becomes the workspace folder"
	ti.PromptStyle = selectedStyle
	ti.Cursor.Style = cursorBarStyle
	ti.CharLimit = 64
	return ti
}

// enterLaunch opens the launch overlay rooted at the source directory of the
// active host.
func (m Model) enterLaunch() (tea.Model, tea.Cmd) {
	m.screen = screenLaunch
	m.launch = launchState{
		selected:   map[string]bool{},
		srcRepo:    map[string]bool{},
		name:       newNameInput(),
		targetHost: m.activeHost,
	}
	return m.startBrowse(m.launchRootFor(m.activeHost))
}

// enterLaunchWithConfig opens the launch overlay pre-bound to a saved config; its
// frozen ConfigSnapshot (name, kit options, agent) is sent at launch (T042).
func (m Model) enterLaunchWithConfig(cfg *store.Configuration) (tea.Model, tea.Cmd) {
	m.screen = screenLaunch
	m.launch = launchState{
		config:     cfg,
		selected:   map[string]bool{},
		srcRepo:    map[string]bool{},
		cloneMode:  cfg.SeedingMode == "clone",
		name:       newNameInput(),
		targetHost: m.activeHost,
	}
	return m.startBrowse(m.launchRootFor(m.activeHost))
}

func (m Model) launchRoot() string {
	if m.srcRoot != "" {
		return m.srcRoot
	}
	wd, _ := os.Getwd()
	return wd
}

// isLocalHost reports whether host is the local daemon (its filesystem is the
// client's own). Single-host clients (no manager) are always local.
func (m Model) isLocalHost(host string) bool {
	if m.manager == nil {
		return true
	}
	if hc, ok := m.manager.Get(host); ok {
		return hc.Entry.Kind == "local"
	}
	return false
}

// launchRootFor is the directory the source browser starts in for host. The
// local daemon shares the client's filesystem, so browse from the client cwd
// (the user is usually in their project tree); a remote host is browsed from the
// daemon's advertised workspace root, an absolute path that exists on that host.
func (m Model) launchRootFor(host string) string {
	if m.isLocalHost(host) {
		if r := m.launchRoot(); r != "" {
			return r
		}
	}
	if wr := m.daemonForHost(host).WorkspaceRoot(); wr != "" {
		return wr
	}
	return m.launchRoot()
}

// startBrowse points the browser at dir on the current target host. The local
// host is read synchronously (os.ReadDir); a remote host is fetched over gRPC
// (ListSources), showing a loading state until browseMsg lands.
func (m Model) startBrowse(dir string) (tea.Model, tea.Cmd) {
	if m.isLocalHost(m.launch.targetHost) {
		m.launch.loading = false
		m.launch.loadDir(dir)
		return m, nil
	}
	m.launch.loading = true
	m.launch.loadErr = ""
	m.launch.dir = dir
	m.launch.entries = nil
	return m, m.browseCmd(m.launch.targetHost, dir)
}

// browseCmd fetches a remote directory listing from host's daemon. The daemon
// returns child directories (with is-repo flags); we prepend a ".." row for
// upward navigation unless already at the filesystem root.
func (m Model) browseCmd(host, dir string) tea.Cmd {
	d := m.daemonForHost(host)
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		cands, err := d.ListSources(ctx, dir, false)
		if err != nil {
			return browseMsg{host: host, dir: dir, err: err.Error()}
		}
		rows := make([]fsEntry, 0, len(cands)+1)
		if parent := filepath.Dir(dir); dir != "" && parent != dir {
			rows = append(rows, fsEntry{name: "..", path: parent, isDir: true, up: true})
		}
		for _, c := range cands {
			rows = append(rows, fsEntry{
				name:   filepath.Base(c.GetPath()),
				path:   c.GetPath(),
				isDir:  true,
				isRepo: c.GetIsRepo(),
			})
		}
		return browseMsg{host: host, dir: dir, entries: rows}
	}
}

// applyBrowse installs a remote directory listing, ignoring a stale response
// whose host/dir no longer matches the current browse target.
func (m Model) applyBrowse(msg browseMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenLaunch || msg.host != m.launch.targetHost || msg.dir != m.launch.dir {
		return m, nil
	}
	m.launch.loading = false
	if msg.err != "" {
		m.launch.loadErr = msg.err
		m.launch.entries = nil
		return m, nil
	}
	m.launch.loadErr = ""
	m.launch.entries = msg.entries
	m.launch.cursor = 0
	if len(msg.entries) > 0 && msg.entries[0].up {
		m.launch.cursor = 1
	}
	m.launch.offset = 0
	return m, nil
}

// loadDir reads dir into the browser, listing sub-directories first then files
// (hidden entries skipped), with a ".." row for going up unless at the root.
func (l *launchState) loadDir(dir string) {
	dir = filepath.Clean(dir)
	ents, err := os.ReadDir(dir)
	l.loadErr = ""
	if err != nil {
		l.loadErr = err.Error()
		return
	}
	var dirs, files []fsEntry
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // skip hidden entries to keep the list uncluttered
		}
		entry := fsEntry{name: name, path: filepath.Join(dir, name), isDir: e.IsDir()}
		if entry.isDir {
			entry.isRepo = isGitRepo(entry.path)
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].name < dirs[j].name })
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	var rows []fsEntry
	if parent := filepath.Dir(dir); parent != dir {
		rows = append(rows, fsEntry{name: "..", path: parent, isDir: true, up: true})
	}
	rows = append(rows, dirs...)
	rows = append(rows, files...)

	l.dir = dir
	l.entries = rows
	// Start on the first real entry rather than the ".." row.
	l.cursor = 0
	if len(rows) > 1 && rows[0].up {
		l.cursor = 1
	}
	l.offset = 0
}

func (l *launchState) current() (fsEntry, bool) {
	if l.cursor < 0 || l.cursor >= len(l.entries) {
		return fsEntry{}, false
	}
	return l.entries[l.cursor], true
}

// toggle adds/removes the highlighted entry from the selection (".." ignored).
func (l *launchState) toggle() {
	e, ok := l.current()
	if !ok || e.up {
		return
	}
	if l.selected[e.path] {
		delete(l.selected, e.path)
		delete(l.srcRepo, e.path)
		for i, p := range l.order {
			if p == e.path {
				l.order = append(l.order[:i], l.order[i+1:]...)
				break
			}
		}
		return
	}
	l.selected[e.path] = true
	if l.srcRepo == nil {
		l.srcRepo = map[string]bool{}
	}
	l.srcRepo[e.path] = e.isRepo
	l.order = append(l.order, e.path)
}

func (l *launchState) moveCursor(delta int) {
	l.cursor += delta
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor > len(l.entries)-1 {
		l.cursor = len(l.entries) - 1
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+browseVisible {
		l.offset = l.cursor - browseVisible + 1
	}
}

func (m Model) launchHelp() helpBindings {
	if m.launch.naming {
		return helpBindings{hkey("enter", "done"), hkey("esc", "cancel name")}
	}
	if m.launch.kitPick {
		return helpBindings{hkey("space", "toggle kit"), hkey("↑/↓", "move"), hkey("enter/esc", "done")}
	}
	if m.launch.hostPick {
		return helpBindings{hkey("↑/↓", "move"), hkey("enter", "select host"), hkey("esc", "cancel")}
	}
	return helpBindings{
		hkey("space", "select"),
		hkey("→/←", "open/up"),
		hkey("N", "name"),
		hkey("m", "seeding mode"),
		hkey("K", "kits"),
		hkey("H", "host"),
		hkey("enter", "launch"),
		hkey("esc", "cancel"),
	}
}

// openLaunchKitPick enters the kit-selection sub-mode, loading the saved kits.
func (m Model) openLaunchKitPick() (tea.Model, tea.Cmd) {
	if m.kits == nil {
		return m, nil
	}
	kits, err := m.kits.List()
	if err != nil {
		m.launch.loadErr = "kits unavailable: " + err.Error()
		return m, nil
	}
	m.launch.kitAll = kits
	m.launch.kitPick = true
	m.launch.kitCursor = 0
	if m.launch.kitOn == nil {
		m.launch.kitOn = map[string]bool{}
	}
	return m, nil
}

// updateLaunchKitKey drives the kit-selection sub-mode.
func (m Model) updateLaunchKitKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.launch.kitPick = false
		return m, nil
	case "up", "k":
		if m.launch.kitCursor > 0 {
			m.launch.kitCursor--
		}
	case "down", "j":
		if m.launch.kitCursor < len(m.launch.kitAll)-1 {
			m.launch.kitCursor++
		}
	case " ":
		m.launch.toggleKit()
	}
	return m, nil
}

// toggleKit selects/deselects the highlighted kit, preserving selection order:
// sbx composes stacked kits in the order given, so the order is meaningful.
func (l *launchState) toggleKit() {
	if l.kitCursor < 0 || l.kitCursor >= len(l.kitAll) {
		return
	}
	id := l.kitAll[l.kitCursor].ID()
	if l.kitOn == nil {
		l.kitOn = map[string]bool{}
	}
	if l.kitOn[id] {
		delete(l.kitOn, id)
		for i, existing := range l.kitOrder {
			if existing == id {
				l.kitOrder = append(l.kitOrder[:i], l.kitOrder[i+1:]...)
				break
			}
		}
		return
	}
	l.kitOn[id] = true
	l.kitOrder = append(l.kitOrder, id)
}

// selectedKitRefs renders the chosen kits as wire refs, in selection order.
func (l *launchState) selectedKitRefs() ([]*pb.KitRef, error) {
	byID := map[string]*store.Kit{}
	for _, k := range l.kitAll {
		byID[k.ID()] = k
	}
	refs := make([]*pb.KitRef, 0, len(l.kitOrder))
	for _, id := range l.kitOrder {
		k, ok := byID[id]
		if !ok {
			continue
		}
		ref, err := k.ToRef()
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (m Model) updateLaunchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.launch.naming {
		return m.updateLaunchNameKey(msg)
	}
	if m.launch.kitPick {
		return m.updateLaunchKitKey(msg)
	}
	if m.launch.hostPick {
		return m.updateLaunchHostKey(msg)
	}
	switch msg.String() {
	case "esc":
		m.screen = screenList
		return m, nil
	case "K":
		return m.openLaunchKitPick()
	case "H":
		return m.openLaunchHostPick()
	case "up", "k":
		m.launch.moveCursor(-1)
	case "down", "j":
		m.launch.moveCursor(1)
	case "right", "l":
		if e, ok := m.launch.current(); ok && e.isDir {
			return m.startBrowse(e.path)
		}
	case "left", "h", "backspace":
		return m.startBrowse(filepath.Dir(m.launch.dir))
	case " ":
		m.launch.toggle()
	case "N":
		m.launch.naming = true
		return m, m.launch.name.Focus()
	case "m":
		m.launch.cloneMode = !m.launch.cloneMode
	case "enter":
		return m.startLaunch()
	}
	return m, nil
}

// connectedHostChoices lists the hosts a sandbox can be created on right now:
// every connected host (the local daemon is always one). Empty when multi-host
// support is not configured.
func (m Model) connectedHostChoices() []hostChoice {
	if m.manager == nil {
		return nil
	}
	var out []hostChoice
	for _, hc := range m.manager.List() {
		if hc.State == client.HostConnected && hc.Conn != nil {
			out = append(out, hostChoice{id: hc.Entry.ID, label: hc.Entry.DisplayName})
		}
	}
	return out
}

// openLaunchHostPick enters the host-selection sub-mode, cursor on the current
// target. A no-op when there is nothing to switch between (single/no host).
func (m Model) openLaunchHostPick() (tea.Model, tea.Cmd) {
	choices := m.connectedHostChoices()
	if len(choices) < 2 {
		m.launch.progress = "no other connected host — connect one on the hosts screen (h)"
		return m, nil
	}
	m.launch.hostChoices = choices
	m.launch.hostPick = true
	m.launch.hostCursor = 0
	for i, c := range choices {
		if c.id == m.launch.targetHost {
			m.launch.hostCursor = i
			break
		}
	}
	return m, nil
}

// updateLaunchHostKey drives the host-selection sub-mode. Choosing a different
// host re-roots the browser on it and clears selections (paths are host-local).
func (m Model) updateLaunchHostKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.launch.hostPick = false
		return m, nil
	case "enter":
		m.launch.hostPick = false
		c, ok := m.launch.currentHostChoice()
		if !ok || c.id == m.launch.targetHost {
			return m, nil
		}
		m.launch.targetHost = c.id
		m.launch.progress = ""
		m.launch.selected = map[string]bool{}
		m.launch.srcRepo = map[string]bool{}
		m.launch.order = nil
		return m.startBrowse(m.launchRootFor(c.id))
	case "up", "k":
		if m.launch.hostCursor > 0 {
			m.launch.hostCursor--
		}
	case "down", "j":
		if m.launch.hostCursor < len(m.launch.hostChoices)-1 {
			m.launch.hostCursor++
		}
	}
	return m, nil
}

func (l *launchState) currentHostChoice() (hostChoice, bool) {
	if l.hostCursor < 0 || l.hostCursor >= len(l.hostChoices) {
		return hostChoice{}, false
	}
	return l.hostChoices[l.hostCursor], true
}

// launchTargetLabel is the display name of the current target host.
func (m Model) launchTargetLabel() string {
	for _, c := range m.connectedHostChoices() {
		if c.id == m.launch.targetHost {
			return c.label
		}
	}
	return m.daemonForHost(m.launch.targetHost).HostID()
}

// updateLaunchNameKey edits the optional sandbox-name field; enter/esc return to
// the browser.
func (m Model) updateLaunchNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter, tea.KeyEsc:
		m.launch.naming = false
		m.launch.name.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.launch.name, cmd = m.launch.name.Update(msg)
	return m, cmd
}

func (m Model) selectedSources() []*pb.SourceRef {
	out := make([]*pb.SourceRef, 0, len(m.launch.order))
	for _, p := range m.launch.order {
		// IsRepo is captured at selection time (from the target host's listing),
		// since p may be a path on a remote host the client cannot stat.
		out = append(out, &pb.SourceRef{Path: p, IsRepo: m.launch.srcRepo[p]})
	}
	return out
}

// startLaunch begins a streaming launch from the selected sources. Rather than
// hold the modal open behind a progress bar, it drops an optimistic CREATING row
// onto the list and closes the wizard immediately, so the workspace copy + boot
// (FR-028) happens in the background while the user keeps using the TUI. Progress
// events flow over a per-launch channel and are pumped into Bubble Tea one message
// at a time (the canonical streaming pattern), updating that row live.
func (m Model) startLaunch() (tea.Model, tea.Cmd) {
	sources := m.selectedSources()
	if len(sources) == 0 {
		m.launch.progress = "select at least one directory (space), then enter"
		return m, nil
	}
	mode := pb.SeedingMode_SEEDING_MODE_DUPLICATE
	if m.launch.cloneMode {
		mode = pb.SeedingMode_SEEDING_MODE_CLONE
	}
	// Freeze the snapshot from the saved config when launching from one (T042),
	// otherwise an ad-hoc snapshot carrying just the seeding mode.
	var snapshot *pb.ConfigSnapshot
	if m.launch.config != nil {
		snapshot = m.launch.config.ToSnapshot()
	} else {
		snapshot = &pb.ConfigSnapshot{}
	}
	snapshot.SeedingMode = mode
	// Kits chosen in the wizard ride along with the create — sbx only honours
	// `--kit` at creation (FR-032).
	kitRefs, err := m.launch.selectedKitRefs()
	if err != nil {
		m.launch.progress = ""
		m.launch.loadErr = "kit error: " + err.Error()
		return m, nil
	}
	name := strings.TrimSpace(m.launch.name.Value())
	req := &pb.LaunchSandboxRequest{
		Config:      snapshot,
		Sources:     sources,
		DisplayName: name,
		Kits:        kitRefs,
	}

	// Register the optimistic placeholder and detach the launch to the background.
	// It is attributed to the chosen target host so its row lands under the right
	// host tab (the launch may target a host other than the active one).
	host := m.launch.targetHost
	if host == "" {
		host = m.activeHost
	}
	m.launchSeq++
	tempID := fmt.Sprintf("pending-%d", m.launchSeq)
	ch := make(chan tea.Msg, 32)
	if m.launching == nil {
		m.launching = map[string]*launchInFlight{}
	}
	m.launching[tempID] = &launchInFlight{
		tempID:   tempID,
		host:     host,
		seq:      m.launchSeq,
		progress: "starting…",
		ch:       ch,
		sb: &pb.Sandbox{
			Id:          tempID,
			DisplayName: name,
			State:       pb.SandboxState_SANDBOX_STATE_CREATING,
			Sources:     sources,
			SeedingMode: mode,
		},
	}
	m.screen = screenList
	m.refreshListItems()

	d := m.daemonForHost(m.launch.targetHost)
	go func() {
		sb, blocked, err := d.Launch(context.Background(), req, func(u client.LaunchUpdate) {
			ch <- launchProgressMsg{id: tempID, update: u}
		})
		ch <- launchResultMsg{id: tempID, sb: sb, blocked: blocked, err: err}
		close(ch)
	}()

	return m, waitForMsg(ch)
}

// isGitRepo reports whether path (a directory) contains a .git entry.
func isGitRepo(path string) bool {
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	return false
}

func waitForMsg(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m Model) handleLaunchProgress(msg launchProgressMsg) (tea.Model, tea.Cmd) {
	lf, ok := m.launching[msg.id]
	if !ok {
		return m, nil // launch already resolved; drop a late progress frame
	}
	u := msg.update
	if u.Copy != nil {
		total := u.Copy.GetBytesTotal()
		pct := 0
		if total > 0 {
			pct = int(100 * u.Copy.GetBytesCopied() / total)
		}
		lf.progress = fmt.Sprintf("copying %d%% %s", pct, filepath.Base(u.Copy.GetCurrentPath()))
	} else if u.LogLine != "" {
		lf.progress = "sbx: " + u.LogLine
	}
	m.refreshListItems()
	return m, waitForMsg(lf.ch)
}

func (m Model) handleLaunchResult(msg launchResultMsg) (tea.Model, tea.Cmd) {
	host := m.activeHost
	if lf, ok := m.launching[msg.id]; ok && lf.host != "" {
		host = lf.host
	}
	delete(m.launching, msg.id)
	if msg.err != nil {
		m.err = msg.err
		m.status = "launch failed: " + msg.err.Error()
		m.refreshListItems()
		return m, nil
	}
	if msg.blocked != nil {
		m.err = nil
		m.status = "launch blocked (low resources): " +
			strings.Join(msg.blocked.GetWarnings(), "; ") + " — free disk and retry"
		m.refreshListItems()
		return m, nil
	}
	// Success: swap the placeholder for the daemon's real sandbox (inserted now so
	// the row doesn't blink out before the reload lands), then reload for canonical
	// fields + the tab-bar aggregate.
	m.err = nil
	m.status = "launched " + short(msg.sb.GetId())
	m.insertSandbox(msg.sb, host)
	m.listLoading = true
	m.refreshListItems()
	return m, m.reloadCmd()
}

// launchModal renders the launch layer's modal box (browser or live progress).
func (m Model) launchModal() string {
	title := sectionStyle.Render("Launch sandbox") + dimStyle.Render("  ·  host "+m.launchTargetLabel())

	var content string
	switch {
	case m.launch.kitPick:
		content = m.launchKitPicker()
	case m.launch.hostPick:
		content = m.launchHostPicker()
	default:
		content = m.launchBrowser()
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, title, "", content)
	if m.launch.progress != "" {
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", statusErrStyle.Render(m.launch.progress))
	}
	help := "space select · →/← open/up · N name · m mode · K kits · H host · enter launch · esc cancel"
	switch {
	case m.launch.naming:
		help = "type a name · enter done · esc cancel name"
	case m.launch.kitPick:
		help = "space toggle · ↑/↓ move · enter/esc done"
	case m.launch.hostPick:
		help = "↑/↓ move · enter select host · esc cancel"
	}
	inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", helpStyle.Render(help))
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}

// launchHostPicker renders the target-host chooser: every connected host, with
// the current target marked.
func (m Model) launchHostPicker() string {
	rows := []string{dimStyle.Render("Create the sandbox on which host?"), ""}
	for i, c := range m.launch.hostChoices {
		mark := "   "
		if c.id == m.launch.targetHost {
			mark = statusOKStyle.Render(" ● ")
		}
		label := c.label
		if i == m.launch.hostCursor {
			label = selectedStyle.Render(label)
			rows = append(rows, cursorBarStyle.Render("▌")+mark+label)
			continue
		}
		rows = append(rows, " "+mark+label)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// launchKitPicker renders the kit-selection sub-mode. Selection order is shown
// because sbx composes stacked kits in the order they are passed.
func (m Model) launchKitPicker() string {
	if len(m.launch.kitAll) == 0 {
		return dimStyle.Render("No kits saved. Press K on the sandbox list to create one.")
	}
	rows := []string{dimStyle.Render("Kits are applied at creation, in the order selected."), ""}
	for i, k := range m.launch.kitAll {
		id := k.ID()
		mark := "[ ]"
		if m.launch.kitOn[id] {
			mark = "[" + strconv.Itoa(indexOfString(m.launch.kitOrder, id)+1) + "]"
		}
		label := kitLabel(k)
		if i == m.launch.kitCursor {
			label = selectedStyle.Render(label)
			rows = append(rows, cursorBarStyle.Render("▌ ")+mark+" "+label)
			continue
		}
		rows = append(rows, "  "+mark+" "+label)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// indexOfString returns the position of s in list, or -1.
func indexOfString(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// launchBrowser renders the directory browser: current path, seeding mode, the
// scrollable entry list with selection checkboxes, and the chosen sources.
func (m Model) launchBrowser() string {
	l := m.launch
	mode := "duplicate (default)"
	if l.cloneMode {
		mode = "clone"
	}
	var nameLine string
	if l.naming {
		nameLine = "Name: " + l.name.View()
	} else if v := strings.TrimSpace(l.name.Value()); v != "" {
		nameLine = "Name: " + selectedStyle.Render(v) + dimStyle.Render("  (N to edit)")
	} else {
		nameLine = "Name: " + dimStyle.Render("(optional — N to name)")
	}
	kitLine := "Kits: " + dimStyle.Render("(none — K to attach)")
	if n := len(l.kitOrder); n > 0 {
		kitLine = "Kits: " + selectedStyle.Render(strings.Join(l.kitOrder, ", ")) + dimStyle.Render("  (K to edit)")
	}
	head := lipgloss.JoinVertical(lipgloss.Left,
		nameLine,
		dimStyle.Render(l.dir),
		"Seeding mode: "+selectedStyle.Render(mode)+dimStyle.Render("  (m to toggle)"),
		kitLine,
		"",
	)

	var body string
	switch {
	case l.loading:
		body = dimStyle.Render("loading " + l.dir + " …")
	case l.loadErr != "":
		body = statusErrStyle.Render("cannot read directory: " + l.loadErr)
	case len(l.entries) == 0:
		body = dimStyle.Render("(empty directory)")
	default:
		var b strings.Builder
		end := l.offset + browseVisible
		if end > len(l.entries) {
			end = len(l.entries)
		}
		for i := l.offset; i < end; i++ {
			e := l.entries[i]
			cursor := "  "
			if i == l.cursor {
				cursor = cursorBarStyle.Render("> ")
			}
			check := "[ ]"
			if e.up {
				check = "   "
			} else if l.selected[e.path] {
				check = statusOKStyle.Render("[x]")
			}
			name := e.name
			if e.isDir && !e.up {
				name += "/"
			}
			b.WriteString(cursor + check + " " + name + "\n")
		}
		if len(l.entries) > browseVisible {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d of %d", end, len(l.entries))))
		}
		body = strings.TrimRight(b.String(), "\n")
	}

	sel := dimStyle.Render("No directories selected yet.")
	if len(l.order) > 0 {
		names := make([]string, 0, len(l.order))
		for _, p := range l.order {
			names = append(names, filepath.Base(p))
		}
		sel = selectedStyle.Render(fmt.Sprintf("Selected (%d): ", len(l.order))) + strings.Join(names, ", ")
	}

	return lipgloss.JoinVertical(lipgloss.Left, head, body, "", sel)
}

// modalInnerWidth is the content width inside the launch modal.
func (m Model) modalInnerWidth() int {
	w := m.width * 2 / 3
	if w > 72 {
		w = 72
	}
	if w < 40 {
		w = 40
	}
	return w
}
