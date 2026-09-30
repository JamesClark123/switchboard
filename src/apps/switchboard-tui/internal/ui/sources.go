package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	swb "github.com/jamesclark123/switchboard/libs/switchboard-proto"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Feature 007 — Edit Sandbox Sources. `S` on a sandbox row opens the sources
// overlay: the sandbox's recorded seeded folders, from which `a` adds folders
// (browsing the OWNING host's filesystem with the launch wizard's machinery) and
// `space` + `d` removes marked ones behind the confirm dialog (FR-060). Adds run
// in the background and badge live copy progress on the row while the sandbox
// stays RUNNING/STOPPED (FR-054/FR-055); nothing here ever restarts a sandbox.

// Client-side bounds for the two edits (research R8). Package constants — no env
// var: an add is the refreshTimeout class (multi-GB copies take minutes), and a
// removal of a huge dependency tree is not instant either.
const (
	addSourcesTimeout    = 30 * time.Minute
	removeSourcesTimeout = 10 * time.Minute
)

// sourcesState backs the sources overlay for one sandbox.
type sourcesState struct {
	sandboxID string
	sandbox   string // display name
	host      string // owning host id — every RPC and browse targets THIS host (FR-065)
	mode      pb.SeedingMode
	sources   []*pb.SourceRef // the recorded set as last seen
	cursor    int
	marked    map[string]bool // recorded paths marked for removal
	browsing  bool            // the add sub-mode (launch browser) is active
	status    string          // overlay-local status line
}

// sourceAddInFlight is one streaming add, keyed by sandbox id in Model.sourceAdds.
// Progress events mutate it in place; the row and the overlay re-render on the
// shared spinner tick. Unlike a launch placeholder it is attached to an EXISTING
// row, whose state badge stays what the daemon says it is.
type sourceAddInFlight struct {
	sandboxID string
	host      string
	sources   []*pb.SourceRef
	progress  string
	ch        chan tea.Msg
}

// sourcesProgressMsg carries one streamed frame of an in-flight add.
type sourcesProgressMsg struct {
	id     string
	update client.LaunchUpdate
}

// sourcesAddResultMsg is the terminal outcome of an add: the updated sandbox, a
// low-resource block awaiting override, or an error.
type sourcesAddResultMsg struct {
	id      string
	host    string
	sources []*pb.SourceRef
	sb      *pb.Sandbox
	blocked *pb.ResourceReport
	err     error
}

// sourcesOverrideMsg re-dispatches a blocked add with the override flag set; it is
// what the low-resource confirm's onConfirm yields (a Cmd cannot mutate the model).
type sourcesOverrideMsg struct {
	id      string
	host    string
	sources []*pb.SourceRef
}

// sourcesRemovedMsg is the outcome of a confirmed removal.
type sourcesRemovedMsg struct {
	id      string
	removed []string
	sb      *pb.Sandbox
	err     error
}

// enterSources opens the overlay for a sandbox.
func (m Model) enterSources(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	m.sourcesView = sourcesState{
		sandboxID: sb.GetId(),
		sandbox:   sb.GetDisplayName(),
		host:      host,
		mode:      sb.GetSeedingMode(),
		sources:   sb.GetSources(),
		marked:    map[string]bool{},
	}
	m.screen = screenSources
	return m, nil
}

// overlayFor reports whether the sources overlay is open for sandbox id.
func (m Model) overlayFor(id string) bool {
	return m.screen == screenSources && m.sourcesView.sandboxID == id
}

// syncSourcesOverlay refreshes an open overlay from a live sandbox update
// (Event.sandbox_changed or a completed edit), so edits made anywhere reach it
// without a manual reload (FR-067). Marks on folders that no longer exist are
// dropped and the cursor is clamped.
func (m *Model) syncSourcesOverlay(sb *pb.Sandbox) {
	if sb == nil || !m.overlayFor(sb.GetId()) {
		return
	}
	m.sourcesView.sources = sb.GetSources()
	present := map[string]bool{}
	for _, s := range sb.GetSources() {
		present[s.GetPath()] = true
	}
	for p := range m.sourcesView.marked {
		if !present[p] {
			delete(m.sourcesView.marked, p)
		}
	}
	if n := len(m.sourcesView.sources); m.sourcesView.cursor >= n {
		m.sourcesView.cursor = max(n-1, 0)
	}
}

func (m Model) sourcesHelp() helpBindings {
	if m.sourcesView.browsing {
		return helpBindings{
			hkey("space", "select"),
			hkey("→/←", "open/up"),
			hkey("enter", "add selected"),
			hkey("esc", "back"),
		}
	}
	return helpBindings{
		hkey("a", "add folders"),
		hkey("space", "mark"),
		hkey("d", "remove marked"),
		hkey("↑/↓", "move"),
		hkey("esc", "close"),
	}
}

// updateSourcesKey drives the overlay. `d` is the only destructive key and it only
// opens the confirm dialog; every other key is non-destructive (FR-060).
func (m Model) updateSourcesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sourcesView.browsing {
		return m.updateSourcesBrowseKey(msg)
	}
	switch msg.String() {
	case "esc", "q":
		m.sourcesView = sourcesState{}
		m.screen = screenList
		return m, nil
	case "up", "k":
		if m.sourcesView.cursor > 0 {
			m.sourcesView.cursor--
		}
	case "down", "j":
		if m.sourcesView.cursor < len(m.sourcesView.sources)-1 {
			m.sourcesView.cursor++
		}
	case " ":
		if src, ok := m.highlightedSource(); ok {
			if m.sourcesView.marked[src.GetPath()] {
				delete(m.sourcesView.marked, src.GetPath())
			} else {
				m.sourcesView.marked[src.GetPath()] = true
			}
		}
	case "a":
		return m.startSourcesBrowse()
	case "d":
		return m.confirmRemoveSources()
	}
	return m, nil
}

func (m Model) highlightedSource() (*pb.SourceRef, bool) {
	c := m.sourcesView.cursor
	if c < 0 || c >= len(m.sourcesView.sources) {
		return nil, false
	}
	return m.sourcesView.sources[c], true
}

// startSourcesBrowse enters the add sub-mode: the launch wizard's filesystem
// browser rooted at the OWNING host's launch root, in the sandbox's seeding mode.
func (m Model) startSourcesBrowse() (tea.Model, tea.Cmd) {
	if _, busy := m.sourceAdds[m.sourcesView.sandboxID]; busy {
		m.sourcesView.status = "an add is already in progress for this sandbox"
		return m, nil
	}
	host := m.sourcesView.host
	m.launch = launchState{
		selected:   map[string]bool{},
		srcRepo:    map[string]bool{},
		targetHost: host,
		cloneMode:  m.sourcesView.mode == pb.SeedingMode_SEEDING_MODE_CLONE,
	}
	m.sourcesView.browsing = true
	m.sourcesView.status = ""
	return m.startBrowse(m.launchRootFor(host))
}

// updateSourcesBrowseKey navigates the browser exactly as the launch wizard does;
// enter dispatches the add, esc returns to the folder list.
func (m Model) updateSourcesBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.sourcesView.browsing = false
		return m, nil
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
	case "enter":
		sources := m.selectedSources()
		if len(sources) == 0 {
			m.sourcesView.status = "select at least one directory (space), then enter"
			return m, nil
		}
		return m.dispatchAddSources(m.sourcesView.sandboxID, m.sourcesView.host, sources, false)
	}
	return m, nil
}

// folderNames renders the workspace folder names of a source set.
func folderNames(sources []*pb.SourceRef) string {
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, filepath.Base(s.GetPath()))
	}
	return strings.Join(names, ", ")
}

// dispatchAddSources starts a streaming add in the background: it registers the
// in-flight entry (so the row and overlay badge progress), closes the browser,
// and pumps progress/result frames into Bubble Tea over a per-add channel — the
// same pattern launches use, so the TUI stays fully usable during the copy.
func (m Model) dispatchAddSources(id, host string, sources []*pb.SourceRef, override bool) (tea.Model, tea.Cmd) {
	if _, busy := m.sourceAdds[id]; busy {
		m.status = "an add is already in progress for this sandbox"
		return m, nil
	}
	if m.sourceAdds == nil {
		m.sourceAdds = map[string]*sourceAddInFlight{}
	}
	ch := make(chan tea.Msg, 32)
	m.sourceAdds[id] = &sourceAddInFlight{
		sandboxID: id,
		host:      host,
		sources:   sources,
		progress:  "adding " + folderNames(sources) + " — starting",
		ch:        ch,
	}
	if m.overlayFor(id) {
		m.sourcesView.browsing = false
		m.sourcesView.status = ""
	}
	m.refreshListItems()

	d := m.daemonForHost(host)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), addSourcesTimeout)
		defer cancel()
		sb, blocked, err := d.AddSources(ctx, id, sources, override, func(u client.LaunchUpdate) {
			ch <- sourcesProgressMsg{id: id, update: u}
		})
		ch <- sourcesAddResultMsg{id: id, host: host, sources: sources, sb: sb, blocked: blocked, err: err}
		close(ch)
	}()
	return m, waitForMsg(ch)
}

// handleSourcesProgress updates the badge text ("adding <folder> — NN%") and
// re-arms the receiver.
func (m Model) handleSourcesProgress(msg sourcesProgressMsg) (tea.Model, tea.Cmd) {
	add, ok := m.sourceAdds[msg.id]
	if !ok {
		return m, nil // already resolved; drop a late frame
	}
	u := msg.update
	switch {
	case u.Copy != nil:
		total := u.Copy.GetBytesTotal()
		pct := 0
		if total > 0 {
			pct = int(100 * u.Copy.GetBytesCopied() / total)
		}
		folder := folderNames(add.sources)
		for _, s := range add.sources { // name the folder currently copying
			if p := s.GetPath(); p != "" && strings.HasPrefix(u.Copy.GetCurrentPath(), p) {
				folder = filepath.Base(p)
				break
			}
		}
		add.progress = fmt.Sprintf("adding %s — %d%%", folder, pct)
	case u.LogLine != "":
		add.progress = "adding " + folderNames(add.sources) + " — " + truncate(u.LogLine, 40)
	}
	m.refreshListItems()
	return m, waitForMsg(add.ch)
}

// handleSourcesAddResult resolves an add: surface a failure, open the override
// prompt on a low-resource block (FR-057), or merge the updated record.
func (m Model) handleSourcesAddResult(msg sourcesAddResultMsg) (tea.Model, tea.Cmd) {
	delete(m.sourceAdds, msg.id)
	name := m.sandboxName(msg.id)
	switch {
	case msg.err != nil:
		m.err = msg.err
		m.status = "add failed: " + msg.err.Error()
		if m.overlayFor(msg.id) {
			m.sourcesView.status = m.status
		}
		m.refreshListItems()
		return m, nil
	case msg.blocked != nil:
		m.refreshListItems()
		return m.confirmAddOverride(msg, name)
	}
	m.err = nil
	m.status = "added " + folderNames(msg.sources) + " to " + name
	m.mergeSandboxUpdate(msg.sb)
	m.syncSourcesOverlay(msg.sb)
	m.listLoading = true
	m.refreshListItems()
	return m, m.reloadCmd()
}

// sandboxName resolves a display name for status lines.
func (m Model) sandboxName(id string) string {
	for _, row := range m.allRows() {
		if row.sb.GetId() == id && row.sb.GetDisplayName() != "" {
			return row.sb.GetDisplayName()
		}
	}
	if m.sourcesView.sandboxID == id && m.sourcesView.sandbox != "" {
		return m.sourcesView.sandbox
	}
	return short(id)
}

// confirmAddOverride mirrors the launch-time low-resource gate (FR-012f/FR-057):
// the warning is shown, and accepting re-sends the same add with
// override_resource_warning set; declining cancels with nothing copied.
func (m Model) confirmAddOverride(msg sourcesAddResultMsg, name string) (tea.Model, tea.Cmd) {
	body := []string{
		"The host is low on disk for this copy:",
		"",
	}
	for _, w := range msg.blocked.GetWarnings() {
		body = append(body, "  "+w)
	}
	body = append(body, "", "Add "+selectedStyle.Render(folderNames(msg.sources))+" to "+selectedStyle.Render(name)+" anyway?")
	id, host, sources := msg.id, msg.host, msg.sources
	returnTo := screenList
	if m.overlayFor(id) {
		returnTo = screenSources
	}
	return m.enterConfirm(confirmState{
		title:    "Low resources",
		body:     body,
		warning:  "The copy may fail partway or fill the disk.",
		returnTo: returnTo,
		onConfirm: func() tea.Msg {
			return sourcesOverrideMsg{id: id, host: host, sources: sources}
		},
	})
}

// confirmRemoveSources gates the destructive half behind the dialog (FR-060): it
// names the sandbox, every folder to be removed, and the uncommitted-work
// consequence. Marked folders are the targets; with none marked, the highlighted
// folder is. The at-least-one floor is checked here too so the developer is told
// before a round-trip (the daemon enforces it regardless, FR-061).
func (m Model) confirmRemoveSources() (tea.Model, tea.Cmd) {
	var targets []*pb.SourceRef
	for _, src := range m.sourcesView.sources {
		if m.sourcesView.marked[src.GetPath()] {
			targets = append(targets, src)
		}
	}
	if len(targets) == 0 {
		if src, ok := m.highlightedSource(); ok {
			targets = []*pb.SourceRef{src}
		}
	}
	if len(targets) == 0 {
		m.sourcesView.status = "nothing to remove — mark folders with space"
		return m, nil
	}
	if len(targets) >= len(m.sourcesView.sources) {
		m.sourcesView.status = "a sandbox must keep at least one seeded folder"
		return m, nil
	}
	name := m.sourcesView.sandbox
	body := []string{
		"Remove from " + selectedStyle.Render(name) + ":",
		"",
	}
	paths := make([]string, 0, len(targets))
	for _, src := range targets {
		paths = append(paths, src.GetPath())
		body = append(body, "  · "+filepath.Base(src.GetPath())+dimStyle.Render("  ("+src.GetPath()+")"))
	}
	body = append(body,
		"",
		"Everything inside these folders in the sandbox is deleted,",
		"including uncommitted work.",
		"",
		dimStyle.Render("The original directories on the host are untouched."),
		dimStyle.Render("The sandbox keeps running; nothing restarts."),
	)
	id, host := m.sourcesView.sandboxID, m.sourcesView.host
	return m.enterConfirm(confirmState{
		title:     "Remove seeded folders?",
		body:      body,
		verb:      "removing",
		sandboxID: id,
		returnTo:  screenSources,
		onConfirm: m.removeSourcesCmd(m.daemonForHost(host), id, paths),
	})
}

// removeSourcesCmd performs one confirmed removal. The daemon is captured as a
// parameter (Model is copied on every Update, so a closure over m would act on a
// stale copy).
func (m Model) removeSourcesCmd(d Daemon, id string, paths []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), removeSourcesTimeout)
		defer cancel()
		sb, err := d.RemoveSources(ctx, id, paths)
		return sourcesRemovedMsg{id: id, removed: paths, sb: sb, err: err}
	}
}

// handleSourcesRemoved clears the row spinner, surfaces a refusal verbatim (the
// daemon's guards name the service or the floor), or merges the updated record.
func (m Model) handleSourcesRemoved(msg sourcesRemovedMsg) (tea.Model, tea.Cmd) {
	delete(m.busy, msg.id)
	if msg.err != nil {
		m.err = msg.err
		m.status = "remove failed: " + msg.err.Error()
		if m.overlayFor(msg.id) {
			m.sourcesView.status = m.status
		}
		m.refreshListItems()
		return m, nil
	}
	names := make([]string, 0, len(msg.removed))
	for _, p := range msg.removed {
		names = append(names, filepath.Base(p))
	}
	m.err = nil
	m.status = "removed " + strings.Join(names, ", ") + " from " + m.sandboxName(msg.id)
	m.mergeSandboxUpdate(msg.sb)
	m.syncSourcesOverlay(msg.sb)
	if m.overlayFor(msg.id) {
		m.sourcesView.marked = map[string]bool{}
		m.sourcesView.status = ""
	}
	m.listLoading = true
	m.refreshListItems()
	return m, m.reloadCmd()
}

// sourcesModal renders the overlay: the recorded folders with cursor and removal
// marks (or the add browser), any in-flight add's progress, and a status line.
func (m Model) sourcesModal() string {
	v := m.sourcesView
	hostLabel := m.hostDisplayName(v.host)
	if hostLabel == "" {
		hostLabel = m.daemonForHost(v.host).HostID()
	}
	title := sectionStyle.Render("Sources · "+v.sandbox) +
		dimStyle.Render("  ·  "+swb.SeedingModeString(v.mode)+"  ·  host "+hostLabel)

	var content string
	if v.browsing {
		content = lipgloss.JoinVertical(lipgloss.Left,
			"Add folders from "+dimStyle.Render(m.launch.dir),
			"",
			m.launch.entriesView(),
			"",
			m.launch.selectionView(),
		)
	} else {
		rows := make([]string, 0, len(v.sources)+1)
		for i, src := range v.sources {
			cursor := "  "
			if i == v.cursor {
				cursor = cursorBarStyle.Render("> ")
			}
			mark := "[ ]"
			if v.marked[src.GetPath()] {
				mark = dangerStyle.Render("[x]")
			}
			name := filepath.Base(src.GetPath())
			if src.GetIsRepo() {
				name += " " + dimStyle.Render("⎇")
			}
			rows = append(rows, cursor+mark+" "+name+dimStyle.Render("  ("+src.GetPath()+")"))
		}
		if len(rows) == 0 {
			rows = append(rows, dimStyle.Render("(no seeded folders recorded)"))
		}
		content = lipgloss.JoinVertical(lipgloss.Left, rows...)
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, title, "", content)
	if add, ok := m.sourceAdds[v.sandboxID]; ok {
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", m.spinner.View()+" "+selectedStyle.Render(add.progress))
	}
	if v.status != "" {
		style := dimStyle
		if strings.Contains(v.status, "failed") {
			style = statusErrStyle
		}
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", style.Render(v.status))
	}
	help := "a add folders · space mark · d remove marked · ↑/↓ move · esc close"
	if v.browsing {
		help = "space select · →/← open/up · enter add selected · esc back"
	}
	inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", helpStyle.Render(help))
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}
