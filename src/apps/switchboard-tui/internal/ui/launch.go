package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
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
	name  string
	path  string
	isDir bool
	up    bool // the ".." parent-navigation row
}

// launchState backs the launch layer: a filesystem browser that supports
// selecting MULTIPLE directories/files to seed (space toggles), rendered as a
// modal overlaid on the sandbox list. Navigation is →/enter-into-dir, ←/up.
type launchState struct {
	config *store.Configuration // non-nil => launching from a saved config (T042)

	dir       string          // directory currently being browsed
	entries   []fsEntry       // rows of dir (".." first when not at root)
	cursor    int             // highlighted row
	offset    int             // scroll offset into entries
	selected  map[string]bool // chosen absolute paths (the sources to seed)
	order     []string        // selection order, so sources keep a stable order
	loadErr   string          // directory read error, if any
	cloneMode bool            // false => duplicate (default, FR-009)

	name   textinput.Model // optional per-host sandbox name (becomes the workspace dir)
	naming bool            // true while editing the name field

	inProgress bool
	progress   string
	pct        float64
	bar        progress.Model
	ch         chan tea.Msg
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

// enterLaunch opens the launch overlay rooted at the source directory.
func (m Model) enterLaunch() (tea.Model, tea.Cmd) {
	m.screen = screenLaunch
	m.launch = launchState{selected: map[string]bool{}, bar: newBar(), name: newNameInput()}
	m.launch.loadDir(m.launchRoot())
	return m, nil
}

// enterLaunchWithConfig opens the launch overlay pre-bound to a saved config; its
// frozen ConfigSnapshot (name, kit options, agent) is sent at launch (T042).
func (m Model) enterLaunchWithConfig(cfg *store.Configuration) (tea.Model, tea.Cmd) {
	m.screen = screenLaunch
	m.launch = launchState{
		config:    cfg,
		selected:  map[string]bool{},
		cloneMode: cfg.SeedingMode == "clone",
		bar:       newBar(),
		name:      newNameInput(),
	}
	m.launch.loadDir(m.launchRoot())
	return m, nil
}

func (m Model) launchRoot() string {
	if m.srcRoot != "" {
		return m.srcRoot
	}
	wd, _ := os.Getwd()
	return wd
}

func newBar() progress.Model {
	return progress.New(progress.WithGradient("#7D56F4", "#43BF6D"), progress.WithoutPercentage())
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
		for i, p := range l.order {
			if p == e.path {
				l.order = append(l.order[:i], l.order[i+1:]...)
				break
			}
		}
		return
	}
	l.selected[e.path] = true
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
	return helpBindings{
		hkey("space", "select"),
		hkey("→/←", "open/up"),
		hkey("N", "name"),
		hkey("m", "seeding mode"),
		hkey("enter", "launch"),
		hkey("esc", "cancel"),
	}
}

func (m Model) updateLaunchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.launch.inProgress {
		return m, nil // ignore input while a launch streams
	}
	if m.launch.naming {
		return m.updateLaunchNameKey(msg)
	}
	switch msg.String() {
	case "esc":
		m.screen = screenList
		return m, nil
	case "up", "k":
		m.launch.moveCursor(-1)
	case "down", "j":
		m.launch.moveCursor(1)
	case "right", "l":
		if e, ok := m.launch.current(); ok && e.isDir {
			m.launch.loadDir(e.path)
		}
	case "left", "h", "backspace":
		m.launch.loadDir(filepath.Dir(m.launch.dir))
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
		out = append(out, &pb.SourceRef{Path: p, IsRepo: isGitRepo(p)})
	}
	return out
}

// startLaunch begins a streaming launch from the selected sources. Progress
// events flow over a channel and are pumped into Bubble Tea one message at a time
// (the canonical streaming pattern), so the copy progress (FR-028) renders live.
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
	req := &pb.LaunchSandboxRequest{
		Config:      snapshot,
		Sources:     sources,
		DisplayName: strings.TrimSpace(m.launch.name.Value()),
	}

	ch := make(chan tea.Msg, 32)
	m.launch.ch = ch
	m.launch.inProgress = true
	m.launch.progress = "starting…"

	d := m.daemon
	go func() {
		sb, blocked, err := d.Launch(context.Background(), req, func(u client.LaunchUpdate) {
			ch <- launchProgressMsg(u)
		})
		ch <- launchResultMsg{sb: sb, blocked: blocked, err: err}
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

func (m Model) handleLaunchProgress(u client.LaunchUpdate) (tea.Model, tea.Cmd) {
	if u.Copy != nil {
		total := u.Copy.GetBytesTotal()
		m.launch.pct = 0
		if total > 0 {
			m.launch.pct = float64(u.Copy.GetBytesCopied()) / float64(total)
		}
		m.launch.progress = fmt.Sprintf("copying %d%% (%d/%d bytes) %s",
			int(100*m.launch.pct), u.Copy.GetBytesCopied(), total, u.Copy.GetCurrentPath())
	} else if u.LogLine != "" {
		m.launch.progress = "sbx: " + u.LogLine
	}
	return m, waitForMsg(m.launch.ch)
}

func (m Model) handleLaunchResult(msg launchResultMsg) (tea.Model, tea.Cmd) {
	m.launch.inProgress = false
	if msg.err != nil {
		m.err = msg.err
		m.launch.progress = "launch failed: " + msg.err.Error()
		return m, nil
	}
	if msg.blocked != nil {
		m.launch.progress = "blocked (low resources): " + strings.Join(msg.blocked.GetWarnings(), "; ") +
			"  — free disk and retry"
		return m, nil
	}
	// Success: close the overlay and refresh (including the tab-bar aggregate).
	m.screen = screenList
	m.status = "launched " + short(msg.sb.GetId())
	return m, m.reloadCmd()
}

// launchModal renders the launch layer's modal box (browser or live progress).
func (m Model) launchModal() string {
	title := sectionStyle.Render("Launch sandbox") + dimStyle.Render("  ·  host "+m.daemon.HostID())

	var content string
	if m.launch.inProgress {
		content = lipgloss.JoinVertical(lipgloss.Left,
			m.spinner.View()+" launching…",
			m.launch.bar.ViewAs(m.launch.pct),
		)
	} else {
		content = m.launchBrowser()
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, title, "", content)
	if m.launch.progress != "" && !m.launch.inProgress {
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", statusErrStyle.Render(m.launch.progress))
	}
	help := "space select · →/← open/up · N name · m mode · enter launch · esc cancel"
	if m.launch.naming {
		help = "type a name · enter done · esc cancel name"
	}
	inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", helpStyle.Render(help))
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
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
	head := lipgloss.JoinVertical(lipgloss.Left,
		nameLine,
		dimStyle.Render(l.dir),
		"Seeding mode: "+selectedStyle.Render(mode)+dimStyle.Render("  (m to toggle)"),
		"",
	)

	var body string
	switch {
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
