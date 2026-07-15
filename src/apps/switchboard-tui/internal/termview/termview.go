// Package termview is the client-side VT renderer for the persistent terminal
// session (feature 003, T012). It maintains a bounded screen grid, feeds raw
// PTY bytes (snapshot + live frames from the daemon) through the ANSI parser,
// and produces a drawable string sized to the client's viewport so the in-TUI
// terminal view (US2) and the full-screen `sxb attach` mode (US3) can share
// one renderer.
//
// The daemon does NOT run a VT emulator: it fans raw PTY bytes to attached
// clients (research.md R1, task T001 note). All screen interpretation happens
// here, on the client, and every attached client renders identically because
// every attached client sees the same byte stream.
package termview

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/cellbuf"
)

// Screen is a minimal xterm-compatible emulator: it consumes PTY bytes via
// Write, maintains a rows×cols grid of styled cells, and renders that grid as
// a lipgloss-friendly string with each row separated by "\n".
//
// It is safe for concurrent use by one writer (the attach-stream reader) and
// one reader (the Bubble Tea render loop) via an internal mutex.
type Screen struct {
	mu           sync.Mutex
	buf          *cellbuf.Buffer
	pen          cellbuf.Style
	cx           int
	cy           int
	sx           int // saved cursor x
	sy           int // saved cursor y
	rows         int
	cols         int
	pars         *ansi.Parser
	cursorHidden bool // DECTCEM (CSI ?25 l/h): the PTY app hid the cursor

	// scrollback history + viewport offset. Lines that scroll off the top of
	// the grid are rendered once and retained here (bounded) so the client can
	// scroll back through prior output. viewOffset is how many lines the view is
	// scrolled up from the live screen (0 == following live output).
	scroll     []string
	viewOffset int
}

// maxScrollback bounds the client-side history retained for scroll-back. Older
// lines are dropped once the cap is exceeded.
const maxScrollback = 5000

// New returns a Screen sized to cols×rows. cols/rows below 1 are clamped to 1.
func New(cols, rows int) *Screen {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	s := &Screen{
		buf:  cellbuf.NewBuffer(cols, rows),
		cols: cols,
		rows: rows,
		pars: ansi.NewParser(),
	}
	s.pars.SetHandler(ansi.Handler{
		Print:     s.print,
		Execute:   s.execute,
		HandleCsi: s.csi,
		HandleEsc: s.esc,
	})
	return s
}

// Resize adjusts the screen to a new geometry, preserving content that still
// fits. Bubble Tea calls this from WindowSizeMsg; the attach helper also
// forwards the new size to the daemon so the PTY resize propagates.
func (s *Screen) Resize(cols, rows int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols == s.cols && rows == s.rows {
		return
	}
	s.buf.Resize(cols, rows)
	s.cols, s.rows = cols, rows
	s.clampCursor()
}

// Size returns the current screen geometry.
func (s *Screen) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// Write feeds PTY bytes into the emulator. It never returns an error and never
// blocks longer than the parser needs — the daemon protects itself with a
// bounded fan-out queue on the other side (broadcaster.go).
func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pars.Parse(p)
	return len(p), nil
}

// Reset clears the screen, resets the cursor to the origin, and drops any
// pending SGR state. Called on reattach before replaying the daemon snapshot
// so a stale prior frame does not bleed into the new one.
func (s *Screen) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Clear()
	s.cx, s.cy = 0, 0
	s.sx, s.sy = 0, 0
	s.pen = cellbuf.Style{}
	s.cursorHidden = false
	s.scroll = nil
	s.viewOffset = 0
}

// ScrollUp moves the viewport up by n lines into scrollback history, returning
// the number of lines actually scrolled (0 when already at the oldest line).
func (s *Screen) ScrollUp(n int) int {
	if n <= 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.viewOffset
	s.viewOffset += n
	if s.viewOffset > len(s.scroll) {
		s.viewOffset = len(s.scroll)
	}
	return s.viewOffset - before
}

// ScrollDown moves the viewport back down toward the live screen by n lines,
// returning the number of lines actually scrolled (0 when already at the bottom).
func (s *Screen) ScrollDown(n int) int {
	if n <= 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.viewOffset
	s.viewOffset -= n
	if s.viewOffset < 0 {
		s.viewOffset = 0
	}
	return before - s.viewOffset
}

// ScrollToBottom snaps the viewport back to the live screen (follows output).
func (s *Screen) ScrollToBottom() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewOffset = 0
}

// ScrollOffset reports how many lines the viewport is scrolled up from the live
// screen (0 while following live output).
func (s *Screen) ScrollOffset() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewOffset
}

// Cursor returns the current cursor position (0-based col, row).
func (s *Screen) Cursor() (col, row int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cx, s.cy
}

// Render returns a lipgloss-friendly string of the current screen, one line
// per row, with ANSI styling embedded. Trailing blank rows are preserved so
// the caller sees a stable-height block matching the configured geometry —
// the Bubble Tea viewport expects that. When the viewport is scrolled up into
// history, the equivalent window of scrollback + live rows is drawn instead.
func (s *Screen) Render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.viewOffset > 0 {
		return s.renderWindow()
	}
	return s.renderLive()
}

// renderLive draws the live grid with the text cursor overlaid as a reverse-
// video cell (unless the PTY app hid it via DECTCEM). Called with s.mu held.
func (s *Screen) renderLive() string {
	restore := s.overlayCursor()
	var out strings.Builder
	for y := 0; y < s.rows; y++ {
		_, line := cellbuf.RenderLine(s.buf, y)
		out.WriteString(line)
		if y < s.rows-1 {
			out.WriteByte('\n')
		}
	}
	if restore != nil {
		restore()
	}
	return out.String()
}

// renderWindow draws a rows-high window into the combined scrollback + live
// grid, positioned by viewOffset. No cursor is shown while scrolled back.
// Called with s.mu held.
func (s *Screen) renderWindow() string {
	top := len(s.scroll) - s.viewOffset
	var out strings.Builder
	for i := 0; i < s.rows; i++ {
		idx := top + i
		if idx < len(s.scroll) {
			out.WriteString(s.scroll[idx])
		} else {
			_, line := cellbuf.RenderLine(s.buf, idx-len(s.scroll))
			out.WriteString(line)
		}
		if i < s.rows-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// overlayCursor temporarily reverses the cell under the cursor so Render draws a
// visible block cursor, returning a func that restores the original cell (nil
// when the cursor is hidden or out of bounds). Called with s.mu held.
func (s *Screen) overlayCursor() func() {
	if s.cursorHidden || s.cx < 0 || s.cy < 0 || s.cx >= s.cols || s.cy >= s.rows {
		return nil
	}
	orig := s.buf.Cell(s.cx, s.cy) // never nil: blank cells read back as BlankCell
	cursor := orig.Clone()
	cursor.Style.Reverse(true)
	s.buf.SetCell(s.cx, s.cy, cursor)
	cx, cy := s.cx, s.cy
	return func() { s.buf.SetCell(cx, cy, orig) }
}

// --- parser callbacks (called with s.mu held) ---

func (s *Screen) print(r rune) {
	if s.cx >= s.cols {
		s.newline()
	}
	c := cellbuf.NewCell(r)
	c.Style = s.pen
	s.buf.SetCell(s.cx, s.cy, c)
	if c.Width < 1 {
		s.cx++
	} else {
		s.cx += c.Width
	}
}

func (s *Screen) execute(b byte) {
	switch b {
	case '\n':
		s.linefeed()
	case '\r':
		s.cx = 0
	case '\b':
		if s.cx > 0 {
			s.cx--
		}
	case '\t':
		next := (s.cx/8 + 1) * 8
		if next > s.cols-1 {
			next = s.cols - 1
		}
		s.cx = next
	case 0x07: // BEL — ignore
	}
}

func (s *Screen) linefeed() {
	if s.cy >= s.rows-1 {
		s.scrollUp(1)
	} else {
		s.cy++
	}
}

func (s *Screen) newline() {
	s.cx = 0
	s.linefeed()
}

// scrollUp shifts the top n rows off the screen and clears the freshly-exposed
// bottom rows. n is clamped to the screen height. The displaced rows are pushed
// into scrollback so the client can scroll back through them.
func (s *Screen) scrollUp(n int) {
	if n <= 0 {
		return
	}
	if n > s.rows {
		n = s.rows
	}
	for y := 0; y < n; y++ {
		_, line := cellbuf.RenderLine(s.buf, y)
		s.pushScrollback(line)
	}
	if n == s.rows {
		s.buf.Clear()
	} else {
		s.buf.InsertLine(s.rows, n, nil)
		s.buf.DeleteLine(0, n, nil)
	}
	// Keep the viewport visually anchored while the user is scrolled up: new
	// history pushes the live region down, so grow the offset in step.
	if s.viewOffset > 0 {
		s.viewOffset += n
		if s.viewOffset > len(s.scroll) {
			s.viewOffset = len(s.scroll)
		}
	}
}

// pushScrollback appends a displaced line to the bounded scrollback history.
func (s *Screen) pushScrollback(line string) {
	s.scroll = append(s.scroll, line)
	if over := len(s.scroll) - maxScrollback; over > 0 {
		s.scroll = s.scroll[over:]
	}
}

// scrollDown is symmetric — used by reverse-index (ESC M) at the top row.
func (s *Screen) scrollDown(n int) {
	if n <= 0 {
		return
	}
	if n >= s.rows {
		s.buf.Clear()
		return
	}
	s.buf.InsertLine(0, n, nil)
	s.buf.DeleteLine(s.rows, n, nil)
}

// csi dispatches Control Sequence Introducer commands. The subset covers what
// the common shell / TUI programs a sandbox terminal is expected to emit —
// cursor motion, erase, SGR — leaving rarer sequences as no-ops rather than
// crashing on them.
func (s *Screen) csi(cmd ansi.Cmd, params ansi.Params) {
	arg := func(i, def int) int {
		v, _, ok := params.Param(i, def)
		if !ok || v == 0 {
			return def
		}
		return v
	}
	switch cmd.Final() {
	case 'A': // CUU — cursor up
		s.cy = max0(s.cy - arg(0, 1))
	case 'B': // CUD — cursor down
		s.cy = minRow(s, s.cy+arg(0, 1))
	case 'C': // CUF — cursor forward
		s.cx = minCol(s, s.cx+arg(0, 1))
	case 'D': // CUB — cursor back
		s.cx = max0(s.cx - arg(0, 1))
	case 'E': // CNL — next line
		s.cx = 0
		s.cy = minRow(s, s.cy+arg(0, 1))
	case 'F': // CPL — previous line
		s.cx = 0
		s.cy = max0(s.cy - arg(0, 1))
	case 'G': // CHA — column absolute
		s.cx = clamp(arg(0, 1)-1, 0, s.cols-1)
	case 'd': // VPA — row absolute
		s.cy = clamp(arg(0, 1)-1, 0, s.rows-1)
	case 'H', 'f': // CUP / HVP — cursor position (1-based row,col)
		row := arg(0, 1) - 1
		col := arg(1, 1) - 1
		s.cy = clamp(row, 0, s.rows-1)
		s.cx = clamp(col, 0, s.cols-1)
	case 'J': // ED — erase display
		mode := arg(0, 0)
		if p, _, ok := params.Param(0, 0); ok {
			mode = p
		}
		s.eraseDisplay(mode)
	case 'K': // EL — erase line
		mode := arg(0, 0)
		if p, _, ok := params.Param(0, 0); ok {
			mode = p
		}
		s.eraseLine(mode)
	case 'L': // IL — insert line
		s.buf.InsertLine(s.cy, arg(0, 1), nil)
	case 'M': // DL — delete line
		s.buf.DeleteLine(s.cy, arg(0, 1), nil)
	case 'P': // DCH — delete character
		s.deleteChars(arg(0, 1))
	case '@': // ICH — insert character
		s.insertChars(arg(0, 1))
	case 'X': // ECH — erase character
		s.eraseChars(arg(0, 1))
	case 'm': // SGR — style
		cellbuf.ReadStyle(params, &s.pen)
	case 's': // SCP — save cursor position
		s.sx, s.sy = s.cx, s.cy
	case 'u': // RCP — restore cursor position
		s.cx, s.cy = s.sx, s.sy
	case 'r': // DECSTBM — set scrolling region: intentionally ignored (single region)
	case 'h', 'l': // SM / RM — set/reset mode
		// DECTCEM (CSI ?25 h/l) toggles cursor visibility; other modes ignored.
		if cmd.Prefix() == '?' {
			if p, _, ok := params.Param(0, 0); ok && p == 25 {
				s.cursorHidden = cmd.Final() == 'l'
			}
		}
	case 't': // window manipulation — ignored
	}
}

// esc dispatches non-CSI escape sequences.
func (s *Screen) esc(cmd ansi.Cmd) {
	switch cmd.Final() {
	case '7': // DECSC — save cursor
		s.sx, s.sy = s.cx, s.cy
	case '8': // DECRC — restore cursor
		s.cx, s.cy = s.sx, s.sy
	case 'D': // IND — index (line feed, keeping column)
		s.linefeed()
	case 'M': // RI — reverse index
		if s.cy == 0 {
			s.scrollDown(1)
		} else {
			s.cy--
		}
	case 'E': // NEL — next line
		s.cx = 0
		s.linefeed()
	case 'c': // RIS — full reset
		s.buf.Clear()
		s.cx, s.cy = 0, 0
		s.sx, s.sy = 0, 0
		s.pen = cellbuf.Style{}
		s.cursorHidden = false
		s.scroll = nil
		s.viewOffset = 0
	}
}

func (s *Screen) eraseDisplay(mode int) {
	switch mode {
	case 0: // from cursor to end
		s.eraseLine(0)
		for y := s.cy + 1; y < s.rows; y++ {
			s.buf.ClearRect(cellbuf.Rect(0, y, s.cols, 1))
		}
	case 1: // from start to cursor
		for y := 0; y < s.cy; y++ {
			s.buf.ClearRect(cellbuf.Rect(0, y, s.cols, 1))
		}
		s.eraseLine(1)
	case 2, 3: // entire screen (3 also clears scrollback — we have none)
		s.buf.Clear()
	}
}

func (s *Screen) eraseLine(mode int) {
	switch mode {
	case 0: // from cursor to EOL
		if s.cx < s.cols {
			s.buf.ClearRect(cellbuf.Rect(s.cx, s.cy, s.cols-s.cx, 1))
		}
	case 1: // from BOL to cursor
		end := s.cx + 1
		if end > s.cols {
			end = s.cols
		}
		s.buf.ClearRect(cellbuf.Rect(0, s.cy, end, 1))
	case 2: // entire line
		s.buf.ClearRect(cellbuf.Rect(0, s.cy, s.cols, 1))
	}
}

// deleteChars shifts the trailing cells on the current row leftward, clearing
// the rightmost n cells — the DCH sequence a shell uses when you backspace
// through a wrapped line.
func (s *Screen) deleteChars(n int) {
	if n < 1 || s.cx >= s.cols {
		return
	}
	if s.cx+n > s.cols {
		n = s.cols - s.cx
	}
	for x := s.cx; x < s.cols-n; x++ {
		s.buf.SetCell(x, s.cy, s.buf.Cell(x+n, s.cy))
	}
	for x := s.cols - n; x < s.cols; x++ {
		s.buf.SetCell(x, s.cy, nil)
	}
}

// insertChars shifts trailing cells rightward and clears n cells at the cursor.
func (s *Screen) insertChars(n int) {
	if n < 1 || s.cx >= s.cols {
		return
	}
	if s.cx+n > s.cols {
		n = s.cols - s.cx
	}
	for x := s.cols - 1; x >= s.cx+n; x-- {
		s.buf.SetCell(x, s.cy, s.buf.Cell(x-n, s.cy))
	}
	for x := s.cx; x < s.cx+n; x++ {
		s.buf.SetCell(x, s.cy, nil)
	}
}

// eraseChars clears n cells at the cursor without shifting the rest of the row.
func (s *Screen) eraseChars(n int) {
	if n < 1 || s.cx >= s.cols {
		return
	}
	if s.cx+n > s.cols {
		n = s.cols - s.cx
	}
	s.buf.ClearRect(cellbuf.Rect(s.cx, s.cy, n, 1))
}

func (s *Screen) clampCursor() {
	s.cx = clamp(s.cx, 0, s.cols-1)
	s.cy = clamp(s.cy, 0, s.rows-1)
}

// --- helpers ---

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func minRow(s *Screen, v int) int {
	if v > s.rows-1 {
		return s.rows - 1
	}
	return v
}

func minCol(s *Screen, v int) int {
	if v > s.cols-1 {
		return s.cols - 1
	}
	return v
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
