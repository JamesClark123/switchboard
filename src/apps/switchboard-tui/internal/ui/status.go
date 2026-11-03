package ui

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"
)

// The status line (SWI-24, FR-533; contracts/settings-ui.md §2–§3). The status
// wraps to the terminal's width and is cut to a per-screen limit; the lines it
// adds come out of the body (bodyHeight), and a keyed size check in the Update
// wrapper resizes the active surface when its height differs from its target.
// A `v` failure or refusal has its own error style and is held through an
// in-place attach or its Replace dialog, then released (research R9, R10).

const (
	// statusMaxLines caps a wrapped status; terminalStatusLines is the cap on
	// the embedded terminal screen, whose PTY size never depends on the status.
	statusMaxLines      = 6
	terminalStatusLines = 2
	// Column limits for text from outside the spec in a `v` message: a daemon
	// error, a host lookup error (cut in the middle), the OS reason in a
	// launch failure, and a value such as an executable or an unknown editor.
	outsideTextCols = 120
	hostErrCols     = 78
	osReasonCols    = 40
	valueCols       = 60
	// vHoldRecent is how recent a showing `v` failure must be to be held when
	// an in-place attach starts or its Replace dialog opens.
	vHoldRecent = 3 * time.Second
)

// statusLines sanitises text, wraps it at width (at spaces where it can; a
// token longer than the width is broken) and cuts it to limit lines, the last
// ending in `…`. Never below 1 line; an empty status gives no lines.
func statusLines(text string, width, limit int) []string {
	s := sanitizeLine(text, 0)
	if s == "" {
		return nil
	}
	if width < 1 {
		width = 1
	}
	if limit < 1 {
		limit = 1
	}
	lines := strings.Split(ansi.Wrap(s, width, ""), "\n")
	if len(lines) > limit {
		lines = lines[:limit]
		// Truncate adds its tail only to text that is too long, and a wrapped
		// line usually isn't, so the marker is added by hand.
		lines[limit-1] = ansi.Truncate(lines[limit-1], width-1, "") + "…"
	}
	return lines
}

// cutMiddle cuts sanitised text to cols columns in the middle, keeping the
// first cols-40 and the last 39 (one fewer if a wide character lands on the
// cut), so an executable's name or a lookup error's reason stays readable.
// Text that already fits is returned as it is.
func cutMiddle(s string, cols int) string {
	s = sanitizeLine(s, 0)
	w := ansi.StringWidth(s)
	if w <= cols {
		return s
	}
	head := ansi.Truncate(s, cols-40, "")
	tail := ansi.TruncateLeft(s, w-39, "")
	// A wide character on either cut can leave one column too many: the tail
	// is plain sanitised text, so its first character can be dropped whole.
	for ansi.StringWidth(head)+1+ansi.StringWidth(tail) > cols && tail != "" {
		_, size := utf8.DecodeRuneInString(tail)
		tail = tail[size:]
	}
	return head + "…" + tail
}

// cutEnd cuts sanitised text to cols columns at its end with `…`.
func cutEnd(s string, cols int) string { return sanitizeLine(s, cols) }

// --- the active surface and the status limit ---

// surfaceKind classifies what holds the body's size on the current screen.
type surfaceKind int

const (
	// surfaceStatic draws rows with no height to set (Daemons, the settings
	// rows, Hosts, Groups, …): the message is cut to fit around it by chrome.
	surfaceStatic surfaceKind = iota
	// surfaceList is the sandbox list (resized to mainListHeight).
	surfaceList
	// surfaceItemList is an open bubbles list (resized to bodyHeight).
	surfaceItemList
	// surfaceForm is an open huh form (WithHeight(bodyHeight)).
	surfaceForm
	// surfaceWindowed reads bodyHeight as it renders and windows its rows
	// (Jobs, the lanes list, the settings screen's list and input modes).
	surfaceWindowed
	// surfaceTerminal is the embedded terminal: never resized by the status.
	surfaceTerminal
)

// The item lists and windowed views a surface can name.
const (
	ilGroups = iota + 1
	ilHosts
	ilConfigs
	ilKits
	ilNotifications
	ilRuns
	ilServices
)

const (
	wJobs = iota + 1
	wLanes
	wSettingsList
	wSettingsInput
)

// activeSurface is the surface holding the body's size: its kind and, for an
// item list, form or windowed view, which one (an il*/w* constant, or the
// form's guardSurface).
type activeSurface struct {
	kind  surfaceKind
	which int
}

// activeSurface derives the surface from the screen and its sub-state, like
// activeGuard (research R9).
func (m Model) activeSurface() activeSurface {
	return m.surfaceForScreen(m.screen)
}

func (m Model) surfaceForScreen(sc screen) activeSurface {
	form := func(g guardSurface, f *huh.Form) activeSurface {
		if f != nil {
			return activeSurface{kind: surfaceForm, which: int(g)}
		}
		return activeSurface{kind: surfaceStatic}
	}
	switch sc {
	case screenTerminal:
		return activeSurface{kind: surfaceTerminal}
	case screenList, screenLaunch, screenApproval, screenSources, screenSandboxMcp, screenAttachPick, screenOpLog:
		return activeSurface{kind: surfaceList}
	case screenConfirm:
		if m.confirm.returnTo == screenConfirm {
			return activeSurface{kind: surfaceStatic}
		}
		return m.surfaceForScreen(m.confirm.returnTo)
	case screenConfigEditor:
		return form(gConfig, m.editor.form)
	case screenConfigPicker:
		return activeSurface{kind: surfaceItemList, which: ilConfigs}
	case screenKitPicker:
		return activeSurface{kind: surfaceItemList, which: ilKits}
	case screenKitEditor:
		return form(gKitItem, m.kitEditor.form)
	case screenHosts:
		return activeSurface{kind: surfaceItemList, which: ilHosts}
	case screenNotifications:
		return activeSurface{kind: surfaceItemList, which: ilNotifications}
	case screenGroups:
		return activeSurface{kind: surfaceItemList, which: ilGroups}
	case screenRuns:
		return activeSurface{kind: surfaceItemList, which: ilRuns}
	case screenServices:
		return activeSurface{kind: surfaceItemList, which: ilServices}
	case screenMcp:
		return form(gMcp, m.mcpView.form)
	case screenSecrets:
		return form(gSecrets, m.secretsView.form)
	case screenDaemonOrchs:
		return form(gOrchAdmin, m.orch.form)
	case screenJobs:
		j := m.jobs
		switch {
		case j.prompt != nil, j.manage.open:
			return activeSurface{kind: surfaceStatic}
		case j.form != nil:
			return activeSurface{kind: surfaceForm, which: int(gOpJobs)}
		}
		return activeSurface{kind: surfaceWindowed, which: wJobs}
	case screenOrchDetail:
		d := m.orchDetail
		switch {
		case d.form != nil:
			return activeSurface{kind: surfaceForm, which: int(gOpDetail)}
		case d.lanes.form != nil || d.lanes.picker != nil || d.hostPick || d.detail == nil:
			return activeSurface{kind: surfaceStatic}
		case d.view == viewLanes:
			return activeSurface{kind: surfaceWindowed, which: wLanes}
		}
		return activeSurface{kind: surfaceStatic}
	case screenSettings:
		switch m.settingsView.mode {
		case settingsList:
			return activeSurface{kind: surfaceWindowed, which: wSettingsList}
		case settingsInput:
			return activeSurface{kind: surfaceWindowed, which: wSettingsInput}
		}
	}
	return activeSurface{kind: surfaceStatic}
}

// minBody is the active surface's smallest usable body: what the status can't
// take (contracts/settings-ui.md §2). Computed without rendering.
func (m Model) minBody() int {
	s := m.activeSurface()
	switch s.kind {
	case surfaceList:
		// The bubbles list draws at least 9 rows, under the tab bar (2) and
		// the update banner.
		n := 9 + 2
		if m.updateBanner != "" {
			n++
		}
		return n
	case surfaceItemList, surfaceForm:
		return 3 // bodyHeight's floor
	case surfaceWindowed:
		switch s.which {
		case wJobs:
			top, bottom := m.jobsFixedLines()
			return len(top) + len(bottom) + 3
		case wLanes:
			return m.lanesFixedLines(surfaceDetail) + 8 + 6
		case wSettingsList:
			return 1 + 3 // the title, then windowRange's minimum
		case wSettingsInput:
			return m.settingsInputFixedLines() + 1 // one note line stays
		}
	}
	return 0
}

// statusLimitFor is the most lines the status may take with a help footer of
// helpLines: min(6, height - header (2) - blank (1) - help - smallest body),
// never below 1; the terminal screen's is the constant 2.
func (m Model) statusLimitFor(helpLines int) int {
	if m.screen == screenTerminal {
		return terminalStatusLines
	}
	room := m.height - 2 - 1 - helpLines - m.minBody()
	if room < 1 {
		room = 1
	}
	if room > statusMaxLines {
		room = statusMaxLines
	}
	return room
}

// statusLimit is statusLimitFor with the active screen's help.
func (m Model) statusLimit() int {
	return m.statusLimitFor(m.helpLineCount(m.activeHelp()))
}

// statusFitsOneLine reports a status that needs no limit: empty, or no wider
// than the body once sanitised (the text statusLines wraps, since a newline
// has no width until it becomes a space). chrome and statusLineCount share it.
func (m Model) statusFitsOneLine() bool {
	return m.status == "" || ansi.StringWidth(sanitizeLine(m.status, 0)) <= m.bodyWidth()
}

// statusLineCount is how many lines the status takes, computed without
// rendering a body. An empty or one-line status returns 1 before any limit is
// computed, so the common case never renders the help (research R9).
func (m Model) statusLineCount() int {
	if m.statusFitsOneLine() {
		return 1
	}
	return len(statusLines(m.status, m.bodyWidth(), m.statusLimit()))
}

// --- the size check (Update wrapper) ---

// sizeKey is what the size check reads; the target is recomputed only when it
// changes (research R9).
type sizeKey struct {
	status, banner string
	width, height  int
	tabs           int
	surface        activeSurface
	markFree       bool
	// enlargeHint: the help offers ctrl+e (a focused Text), which can wrap
	// the footer to another line.
	enlargeHint bool
}

// sizeChecks counts the targets computed, for tests.
var sizeChecks int

// offersEnlarge reports whether the help footer carries the enlarge hint.
func (m Model) offersEnlarge() bool {
	g, ok := m.activeGuard()
	if !ok {
		return false
	}
	_, _, ok = m.focusedText(g)
	return ok
}

// listOffersMarkFree reports whether the list's help offers mark free, which
// changes the help footer's height.
func (m Model) listOffersMarkFree() bool {
	sb := m.current()
	return sb != nil && (sb.GetController().GetRun().GetNeedsReview() || cleanupNeedsInput(sb))
}

// checkSizes resizes the active surface when its height differs from its
// target, on a key change only; skipped on the terminal screen. An open huh
// form takes a no-op Update in the same pass, because a shrink can leave the
// focused field undrawn until the form handles a message (spec Notes).
func (m *Model) checkSizes() tea.Cmd {
	if m.screen == screenTerminal {
		return nil
	}
	key := sizeKey{
		status: m.status, banner: m.updateBanner, width: m.width, height: m.height,
		tabs: len(m.tabs), surface: m.activeSurface(), markFree: m.listOffersMarkFree(),
		enlargeHint: m.offersEnlarge(),
	}
	if key == m.sizeKey {
		return nil
	}
	m.sizeKey = key
	sizeChecks++
	switch key.surface.kind {
	case surfaceList:
		if h := m.mainListHeight(); m.list.Height() != h {
			m.list.SetSize(m.bodyWidth(), h)
		}
	case surfaceItemList:
		if l := m.sizedItemList(key.surface.which); l != nil {
			if h := m.bodyHeight(); l.Height() != h {
				l.SetSize(m.bodyWidth(), h)
			}
		}
	case surfaceForm:
		f := m.guardForm(guardSurface(key.surface.which))
		if f == nil {
			return nil
		}
		h := m.bodyHeight()
		if m.formHeights == nil {
			m.formHeights = formHeights{}
		}
		// A form not seen before was built in this message, or rebuilt in
		// place (a failed save, reviveCompleted) with the height of that
		// moment; huh has no getter, so it is sized now and recorded.
		if last, known := m.formHeights[f]; known && last == h {
			return nil
		}
		m.formHeights[f] = h
		f.WithHeight(h)
		_, cmd := f.Update(nil)
		return cmd
	}
	if key.surface.kind != surfaceForm && len(m.formHeights) > 0 {
		m.formHeights = nil // the open form is gone; forget its height
	}
	return nil
}

// sizedItemList is the open bubbles list a surface names.
func (m *Model) sizedItemList(which int) *list.Model {
	switch which {
	case ilGroups:
		return &m.groups.list
	case ilHosts:
		return &m.hosts.list
	case ilConfigs:
		return &m.picker.list
	case ilKits:
		return &m.kitPicker.list
	case ilNotifications:
		return &m.notifyList
	case ilRuns:
		return &m.runsView.list
	case ilServices:
		return &m.servicesView.list
	}
	return nil
}

// --- the hold (FR-533, SC-008) ---

// vFailState is the last `v` failure or refusal and when it arrived: the
// status shows in the error style while it equals text; a zero time means it
// was released (or raised by the settings screen) and is never held.
type vFailState struct {
	text string
	at   time.Time
}

// vHoldState is a held message and the in-place attach it waits on (0 = none
// pending; a Replace dialog holds through confirm.holdsV instead).
type vHoldState struct {
	text string
	seq  uint64
}

// vHoldDialog reports a Replace dialog for an in-place attach showing: derived
// from the showing confirm, so no way of closing it can leave it stale
// (closeOrchAdminOnDrop drops a confirm unanswered).
func (m Model) vHoldDialog() bool {
	return m.screen == screenConfirm && m.confirm.holdsV
}

// showErr shows text in the error style with nothing held: a settings
// rejection, or any message that must be red without setting m.err.
func (m *Model) showErr(text string) {
	m.status = text
	m.vFail = vFailState{text: text}
}

// setVFail shows a `v` failure or refusal: in the error style wherever it's
// drawn, and held instead of shown while an in-place attach is pending, its
// Replace dialog is open, or the terminal screen shows (where it's drawn too,
// cut to 2 lines).
func (m *Model) setVFail(text string) {
	m.vFail = vFailState{text: text, at: nowFunc()}
	switch {
	case m.screen == screenTerminal:
		m.vHold.text = text
		m.status = text
	case m.vHold.seq != 0 || m.vHoldDialog():
		m.vHold.text = text
	default:
		m.status = text
	}
}

// holdRecentVFail copies a showing `v` failure that arrived in the last 3 s
// into the hold, when nothing newer is held: run as an in-place attach starts
// or its Replace dialog opens (the dialog can stay open longer than that).
func (m *Model) holdRecentVFail() {
	if m.vHold.text != "" || m.status == "" || m.status != m.vFail.text || m.vFail.at.IsZero() {
		return
	}
	if nowFunc().Sub(m.vFail.at) < vHoldRecent {
		m.vHold.text = m.status
	}
}

// releaseHeld shows a held message once its attach has ended, no Replace
// dialog is open and the terminal isn't showing: it replaces the status, or
// joins an attach/terminal error or daemon-drop message set since the hold
// began (termFail) as `<error>; <held>`, in the error style; a released
// message is never held again (zero time).
func (m *Model) releaseHeld() {
	if m.vHold.text == "" || m.vHold.seq != 0 || m.vHoldDialog() || m.screen == screenTerminal {
		return
	}
	text := m.vHold.text
	if m.status != "" && m.status == m.termFail {
		text = sanitizeLine(m.status, outsideTextCols) + "; " + text
	}
	m.status = text
	m.vFail = vFailState{text: text}
	m.vHold = vHoldState{}
}

// formHeights records the last height applied to each open huh form, which
// has no height getter; the first size check after a form opens records the
// height it was built with.
type formHeights map[*huh.Form]int
