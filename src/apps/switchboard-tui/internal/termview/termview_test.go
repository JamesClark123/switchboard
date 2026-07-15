package termview

import (
	"strings"
	"testing"
)

// stripStyle strips ANSI SGR sequences from a rendered string so tests can
// compare raw visible content without asserting exact escape strings.
func stripStyle(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b { // ESC
			// consume up to (and including) the final byte of the sequence
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) {
					b := s[j]
					j++
					if b >= 0x40 && b <= 0x7e {
						break
					}
				}
				i = j
				continue
			}
			i = j
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// firstNonEmptyLine returns the trimmed first line whose visible content isn't
// blank, so tests can locate output without indexing past the top row.
func firstNonEmptyLine(rendered string) string {
	for _, l := range strings.Split(stripStyle(rendered), "\n") {
		if strings.TrimSpace(l) != "" {
			return strings.TrimRight(l, " ")
		}
	}
	return ""
}

func TestWritePlainText(t *testing.T) {
	s := New(20, 3)
	s.Write([]byte("hello"))
	if got := firstNonEmptyLine(s.Render()); got != "hello" {
		t.Fatalf("first line = %q, want %q", got, "hello")
	}
	col, row := s.Cursor()
	if col != 5 || row != 0 {
		t.Fatalf("cursor = (%d,%d), want (5,0)", col, row)
	}
}

func TestNewlineAndCR(t *testing.T) {
	s := New(20, 3)
	s.Write([]byte("row1\r\nrow2"))
	lines := strings.Split(stripStyle(s.Render()), "\n")
	if strings.TrimRight(lines[0], " ") != "row1" {
		t.Fatalf("line0 = %q", lines[0])
	}
	if strings.TrimRight(lines[1], " ") != "row2" {
		t.Fatalf("line1 = %q", lines[1])
	}
}

func TestSnapshotThenLiveReplay(t *testing.T) {
	// Simulates a reattach: first the daemon's byte-ring snapshot is written,
	// then live bytes stream in; the composed view must reflect the full order.
	s := New(20, 4)
	s.Write([]byte("snapshot\r\n"))
	s.Write([]byte("live"))
	stripped := stripStyle(s.Render())
	if !strings.Contains(stripped, "snapshot") || !strings.Contains(stripped, "live") {
		t.Fatalf("render missing content: %q", stripped)
	}
}

func TestClearScreenCSI(t *testing.T) {
	s := New(20, 3)
	s.Write([]byte("junk\r\nmore"))
	// ESC[2J erases screen; ESC[H homes cursor.
	s.Write([]byte("\x1b[2J\x1b[Hclean"))
	if got := firstNonEmptyLine(s.Render()); got != "clean" {
		t.Fatalf("after clear+home, first line = %q, want clean", got)
	}
}

func TestCursorPositionCUP(t *testing.T) {
	s := New(10, 4)
	// CUP row=3,col=4 (1-based), then print "X".
	s.Write([]byte("\x1b[3;4HX"))
	col, row := s.Cursor()
	if col != 4 || row != 2 {
		t.Fatalf("cursor after CUP+print = (%d,%d), want (4,2)", col, row)
	}
	lines := strings.Split(stripStyle(s.Render()), "\n")
	if !strings.Contains(lines[2], "X") {
		t.Fatalf("row 2 = %q, want to contain X", lines[2])
	}
}

func TestResizePreservesContent(t *testing.T) {
	s := New(20, 3)
	s.Write([]byte("resize-me"))
	s.Resize(30, 5)
	cols, rows := s.Size()
	if cols != 30 || rows != 5 {
		t.Fatalf("size = (%d,%d)", cols, rows)
	}
	if got := firstNonEmptyLine(s.Render()); got != "resize-me" {
		t.Fatalf("content lost on resize: %q", got)
	}
}

func TestResetClearsScreen(t *testing.T) {
	s := New(10, 3)
	s.Write([]byte("dirty"))
	s.Reset()
	if got := firstNonEmptyLine(s.Render()); got != "" {
		t.Fatalf("after reset, first line = %q, want empty", got)
	}
	col, row := s.Cursor()
	if col != 0 || row != 0 {
		t.Fatalf("cursor after reset = (%d,%d)", col, row)
	}
}

func TestScrollOnWrap(t *testing.T) {
	// Writing more lines than the height must scroll — the earliest line
	// drops off and the newest appears at the bottom.
	s := New(20, 2)
	s.Write([]byte("first\r\nsecond\r\nthird"))
	stripped := stripStyle(s.Render())
	if strings.Contains(stripped, "first") {
		t.Fatalf("expected 'first' scrolled off, got %q", stripped)
	}
	if !strings.Contains(stripped, "third") {
		t.Fatalf("expected 'third' present, got %q", stripped)
	}
}

func TestBackspace(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("abc\bd"))
	if got := firstNonEmptyLine(s.Render()); got != "abd" {
		t.Fatalf("backspace result = %q, want abd", got)
	}
}

func TestEraseLine(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("keepthis"))
	// CR back to column 0, then ESC[K erases to EOL from cursor.
	s.Write([]byte("\r\x1b[K"))
	if got := firstNonEmptyLine(s.Render()); got != "" {
		t.Fatalf("after erase-line, first non-empty = %q", got)
	}
}

func TestSGRColoringSurvivesRender(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("\x1b[31mRED\x1b[0m"))
	// The visible content is still "RED"; the styling shows up as SGR bytes.
	if got := firstNonEmptyLine(s.Render()); got != "RED" {
		t.Fatalf("visible = %q, want RED", got)
	}
	if !strings.Contains(s.Render(), "\x1b[") {
		t.Fatalf("expected ANSI style bytes in rendered output")
	}
}

func TestCursorMovementCommands(t *testing.T) {
	s := New(20, 5)
	s.Write([]byte("\x1b[3;5H"))
	// CUU up 2 → row 0
	s.Write([]byte("\x1b[2A"))
	if _, row := s.Cursor(); row != 0 {
		t.Fatalf("CUU row = %d, want 0", row)
	}
	// CUD down 3 → row 3
	s.Write([]byte("\x1b[3B"))
	if _, row := s.Cursor(); row != 3 {
		t.Fatalf("CUD row = %d, want 3", row)
	}
	// CUF forward 5 → col 9 (was col 4 after CUP col=5 1-based)
	col0, _ := s.Cursor()
	s.Write([]byte("\x1b[5C"))
	col1, _ := s.Cursor()
	if col1 != col0+5 {
		t.Fatalf("CUF col = %d, want %d", col1, col0+5)
	}
	// CUB back 3 → col-3
	s.Write([]byte("\x1b[3D"))
	col2, _ := s.Cursor()
	if col2 != col1-3 {
		t.Fatalf("CUB col = %d, want %d", col2, col1-3)
	}
	// CNL next line
	s.Write([]byte("\x1b[1E"))
	c, _ := s.Cursor()
	if c != 0 {
		t.Fatalf("CNL col = %d, want 0", c)
	}
	// CPL previous line
	s.Write([]byte("\x1b[1F"))
	c, _ = s.Cursor()
	if c != 0 {
		t.Fatalf("CPL col = %d, want 0", c)
	}
	// CHA to col 8 (1-based) → 7
	s.Write([]byte("\x1b[8G"))
	c, _ = s.Cursor()
	if c != 7 {
		t.Fatalf("CHA col = %d, want 7", c)
	}
	// VPA to row 2 (1-based) → 1
	s.Write([]byte("\x1b[2d"))
	if _, row := s.Cursor(); row != 1 {
		t.Fatalf("VPA row = %d, want 1", row)
	}
}

func TestSaveRestoreCursorCSI(t *testing.T) {
	s := New(20, 5)
	s.Write([]byte("\x1b[3;5H\x1b[s")) // save at (row=2,col=4)
	s.Write([]byte("\x1b[1;1H"))       // move to origin
	s.Write([]byte("\x1b[u"))          // restore
	col, row := s.Cursor()
	if col != 4 || row != 2 {
		t.Fatalf("SCP/RCP cursor = (%d,%d), want (4,2)", col, row)
	}
}

func TestSaveRestoreCursorESC(t *testing.T) {
	s := New(20, 5)
	s.Write([]byte("\x1b[3;5H\x1b7")) // ESC 7 saves
	s.Write([]byte("\x1b[1;1H"))
	s.Write([]byte("\x1b8")) // ESC 8 restores
	col, row := s.Cursor()
	if col != 4 || row != 2 {
		t.Fatalf("DECSC/DECRC cursor = (%d,%d), want (4,2)", col, row)
	}
}

func TestReverseIndexScrollsAtTop(t *testing.T) {
	s := New(10, 3)
	s.Write([]byte("a\r\nb\r\nc"))
	s.Write([]byte("\x1b[1;1H"))
	// Reverse index at row 0 pushes content down.
	s.Write([]byte("\x1bM"))
	stripped := stripStyle(s.Render())
	if strings.Contains(stripped, "c") && !strings.Contains(stripped, "a") {
		t.Fatalf("reverse index did not preserve top content: %q", stripped)
	}
	// The cursor should stay on row 0.
	if _, row := s.Cursor(); row != 0 {
		t.Fatalf("row after RI = %d, want 0", row)
	}
}

func TestReverseIndexInMiddle(t *testing.T) {
	s := New(10, 3)
	s.Write([]byte("\x1b[2;1H")) // row 1
	s.Write([]byte("\x1bM"))
	if _, row := s.Cursor(); row != 0 {
		t.Fatalf("RI mid-screen row = %d, want 0", row)
	}
}

func TestIndexAndNextLineEsc(t *testing.T) {
	s := New(10, 3)
	s.Write([]byte("\x1bD")) // IND — linefeed
	if _, row := s.Cursor(); row != 1 {
		t.Fatalf("IND row = %d, want 1", row)
	}
	s.Write([]byte("\x1bE")) // NEL — next line, col 0
	c, r := s.Cursor()
	if c != 0 || r != 2 {
		t.Fatalf("NEL cursor = (%d,%d), want (0,2)", c, r)
	}
}

func TestFullResetEscC(t *testing.T) {
	s := New(10, 3)
	s.Write([]byte("dirty\r\nmore"))
	s.Write([]byte("\x1bc"))
	if got := firstNonEmptyLine(s.Render()); got != "" {
		t.Fatalf("after RIS, first line = %q, want empty", got)
	}
	c, r := s.Cursor()
	if c != 0 || r != 0 {
		t.Fatalf("cursor after RIS = (%d,%d)", c, r)
	}
}

func TestEraseDisplayAboveAndBelow(t *testing.T) {
	s := New(10, 4)
	s.Write([]byte("row0\r\nrow1\r\nrow2\r\nrow3"))
	// Move to row 2, erase from cursor downward (mode 0).
	s.Write([]byte("\x1b[3;1H\x1b[0J"))
	stripped := stripStyle(s.Render())
	if !strings.Contains(stripped, "row0") || !strings.Contains(stripped, "row1") {
		t.Fatalf("mode 0 erased above cursor: %q", stripped)
	}
	if strings.Contains(stripped, "row2") || strings.Contains(stripped, "row3") {
		t.Fatalf("mode 0 did not erase below cursor: %q", stripped)
	}
	// Repopulate, then erase upward (mode 1).
	s.Reset()
	s.Write([]byte("row0\r\nrow1\r\nrow2\r\nrow3"))
	s.Write([]byte("\x1b[3;1H\x1b[1J"))
	stripped = stripStyle(s.Render())
	if strings.Contains(stripped, "row0") || strings.Contains(stripped, "row1") {
		t.Fatalf("mode 1 did not erase above cursor: %q", stripped)
	}
}

func TestEraseLineFromBOLToCursor(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("hello world"))
	// Move to column 5 (1-based col=6) and erase BOL→cursor.
	s.Write([]byte("\x1b[1;6H\x1b[1K"))
	stripped := stripStyle(s.Render())
	line := strings.Split(stripped, "\n")[0]
	if !strings.Contains(line, "world") {
		t.Fatalf("mode 1 wiped the wrong side: %q", line)
	}
}

func TestEraseLineEntire(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("keepit"))
	s.Write([]byte("\x1b[2K"))
	if got := firstNonEmptyLine(s.Render()); got != "" {
		t.Fatalf("2K did not clear line: %q", got)
	}
}

func TestInsertAndDeleteCharacters(t *testing.T) {
	s := New(10, 1)
	s.Write([]byte("abcdef"))
	// Move to col 2 (1-based col=3) and delete 2 characters.
	s.Write([]byte("\x1b[1;3H\x1b[2P"))
	if got := firstNonEmptyLine(s.Render()); got != "abef" {
		t.Fatalf("after DCH, line = %q, want abef", got)
	}
	// Insert 2 blanks at cursor.
	s.Write([]byte("\x1b[2@"))
	if got := firstNonEmptyLine(s.Render()); got != "ab  ef" {
		t.Fatalf("after ICH, line = %q, want %q", got, "ab  ef")
	}
	// Erase 2 chars in place.
	s.Write([]byte("\x1b[2X"))
	if got := firstNonEmptyLine(s.Render()); got != "ab" && !strings.HasPrefix(got, "ab") {
		t.Fatalf("after ECH, line = %q", got)
	}
}

func TestInsertAndDeleteLines(t *testing.T) {
	s := New(10, 4)
	s.Write([]byte("a\r\nb\r\nc\r\nd"))
	// Move to row 2 and delete a line — 'b' should be gone, 'c' shifts up.
	s.Write([]byte("\x1b[2;1H\x1b[1M"))
	stripped := stripStyle(s.Render())
	lines := strings.Split(stripped, "\n")
	if strings.TrimSpace(lines[1]) != "c" {
		t.Fatalf("after DL, row 1 = %q, want c", lines[1])
	}
	// Insert a blank line at row 1.
	s.Write([]byte("\x1b[2;1H\x1b[1L"))
	stripped = stripStyle(s.Render())
	lines = strings.Split(stripped, "\n")
	if strings.TrimSpace(lines[1]) != "" {
		t.Fatalf("after IL, row 1 = %q, want blank", lines[1])
	}
}

func TestWrapOnRightEdge(t *testing.T) {
	// Writing past the right edge must wrap to the next row (linefeed + col=0),
	// exercising the auto-newline path in print().
	s := New(3, 3)
	s.Write([]byte("ABCD"))
	stripped := stripStyle(s.Render())
	lines := strings.Split(stripped, "\n")
	if !strings.Contains(lines[0], "ABC") {
		t.Fatalf("row 0 = %q, want ABC", lines[0])
	}
	if !strings.HasPrefix(strings.TrimLeft(lines[1], " "), "D") {
		t.Fatalf("row 1 = %q, want D...", lines[1])
	}
}

func TestBellIsIgnored(t *testing.T) {
	s := New(10, 2)
	s.Write([]byte("hi\x07"))
	if got := firstNonEmptyLine(s.Render()); got != "hi" {
		t.Fatalf("BEL affected output: %q", got)
	}
}

func TestTabAdvancesCursor(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("a\tb"))
	// The tab should have jumped to column 8; then 'b' at col 8.
	stripped := stripStyle(s.Render())
	line := strings.Split(stripped, "\n")[0]
	if !strings.HasPrefix(line, "a") || line[8] != 'b' {
		t.Fatalf("tab did not align: %q", line)
	}
}

func TestUnknownCSIIgnored(t *testing.T) {
	// Unknown / no-op codes must not crash the parser or shift content.
	s := New(10, 2)
	s.Write([]byte("\x1b[5;20r")) // DECSTBM ignored
	s.Write([]byte("\x1b[?25h"))  // DECSET cursor visible — ignored
	s.Write([]byte("\x1b[?25l"))  // DECRST
	s.Write([]byte("\x1b[10t"))   // window manipulation
	s.Write([]byte("ok"))
	if got := firstNonEmptyLine(s.Render()); got != "ok" {
		t.Fatalf("unknown CSIs disturbed output: %q", got)
	}
}

func TestScrollUpFullClears(t *testing.T) {
	// Scrolling by the whole height clears the screen — hit the fast path.
	s := New(5, 2)
	s.Write([]byte("aaaa\r\nbbbb"))
	s.Write([]byte("\r\n\r\n"))
	stripped := strings.TrimSpace(stripStyle(s.Render()))
	if stripped != "" {
		t.Fatalf("expected blank after full scroll, got %q", stripped)
	}
}

func TestScrollBoundaryEdgeCases(t *testing.T) {
	// scrollDown at row 0 with n >= rows takes the clear-all fast path.
	s := New(4, 2)
	s.Write([]byte("hello\r\nworld"))
	// Reverse-index enough times to trigger scrollDown at the top boundary.
	for i := 0; i < 3; i++ {
		s.Write([]byte("\x1b[1;1H\x1bM"))
	}
	// Direct call of no-op branches for coverage of guards.
	s.mu.Lock()
	s.scrollDown(0)
	s.scrollUp(0)
	s.scrollDown(999)
	s.mu.Unlock()
}

func TestClampNegativeArguments(t *testing.T) {
	// A CUP request whose values are already at 0 exercises the clamp lo-branch.
	s := New(5, 3)
	s.Write([]byte("\x1b[0;0H"))
	col, row := s.Cursor()
	if col != 0 || row != 0 {
		t.Fatalf("CUP(0,0) cursor = (%d,%d), want (0,0)", col, row)
	}
	// A huge CUP value exercises the clamp hi-branch.
	s.Write([]byte("\x1b[99;99H"))
	col, row = s.Cursor()
	if col != s.cols-1 || row != s.rows-1 {
		t.Fatalf("CUP oversize cursor = (%d,%d), want max", col, row)
	}
	// CUU past the top and CUB past the left exercise max0 clamps.
	s.Write([]byte("\x1b[1;1H\x1b[10A\x1b[10D"))
	col, row = s.Cursor()
	if col != 0 || row != 0 {
		t.Fatalf("overshoot up/left = (%d,%d), want (0,0)", col, row)
	}
}

func TestOversizeDeleteAndInsert(t *testing.T) {
	// n > columns must clamp inside deleteChars/insertChars/eraseChars.
	s := New(5, 1)
	s.Write([]byte("abcde"))
	s.Write([]byte("\x1b[1;2H\x1b[99P"))
	if got := firstNonEmptyLine(s.Render()); got != "a" {
		t.Fatalf("DCH oversize = %q, want %q", got, "a")
	}
	s.Write([]byte("\x1b[1;2H\x1b[99@"))
	// Nothing after 'a' since insert wiped the row.
	if got := firstNonEmptyLine(s.Render()); got != "a" {
		t.Fatalf("ICH oversize = %q", got)
	}
	s.Write([]byte("xyz"))
	s.Write([]byte("\x1b[1;2H\x1b[99X"))
	if got := firstNonEmptyLine(s.Render()); got != "a" {
		t.Fatalf("ECH oversize = %q", got)
	}
}

func TestClampAndDegenerateSizes(t *testing.T) {
	// Zero / negative sizes must clamp to 1 so subsequent writes don't panic.
	s := New(0, 0)
	if c, r := s.Size(); c != 1 || r != 1 {
		t.Fatalf("clamped size = (%d,%d), want (1,1)", c, r)
	}
	s.Write([]byte("x"))
	s.Resize(-3, -5)
	if c, r := s.Size(); c != 1 || r != 1 {
		t.Fatalf("resize clamp = (%d,%d)", c, r)
	}
}

// hasReverseAtVisible reports whether a rendered line has a reverse-video (SGR
// 7) run somewhere before its visible text — used to detect the block cursor.
func hasReverse(rendered string) bool {
	return strings.Contains(rendered, "\x1b[7m") || strings.Contains(rendered, ";7m") ||
		strings.Contains(rendered, "[7;") || strings.Contains(rendered, "\x1b[7;")
}

func TestCursorIsRenderedByDefault(t *testing.T) {
	// A fresh screen with output shows a visible (reverse-video) cursor cell.
	s := New(10, 2)
	s.Write([]byte("hi"))
	if !hasReverse(s.Render()) {
		t.Fatalf("expected a reverse-video cursor in %q", s.Render())
	}
}

func TestCursorHiddenByDECTCEM(t *testing.T) {
	s := New(10, 2)
	s.Write([]byte("hi"))
	s.Write([]byte("\x1b[?25l")) // hide cursor
	if hasReverse(s.Render()) {
		t.Fatalf("cursor should be hidden after ?25l: %q", s.Render())
	}
	s.Write([]byte("\x1b[?25h")) // show cursor again
	if !hasReverse(s.Render()) {
		t.Fatalf("cursor should be visible after ?25h: %q", s.Render())
	}
}

func TestCursorOverlayDoesNotCorruptContent(t *testing.T) {
	// Rendering the cursor is non-destructive: the underlying glyph and later
	// renders are unchanged.
	s := New(10, 2)
	s.Write([]byte("\x1b[1;1Hab")) // cursor ends at col 2
	s.Write([]byte("\x1b[1;1H"))   // move cursor back onto 'a'
	if got := firstNonEmptyLine(s.Render()); got != "ab" {
		t.Fatalf("content with cursor over 'a' = %q, want ab", got)
	}
	// A second render must still show the same content (overlay was restored).
	if got := firstNonEmptyLine(s.Render()); got != "ab" {
		t.Fatalf("content after re-render = %q, want ab", got)
	}
}

func TestScrollbackCapturesAndOffset(t *testing.T) {
	s := New(20, 2)
	// Push 'first' off the top of the 2-row grid.
	s.Write([]byte("first\r\nsecond\r\nthird"))
	// Live view no longer shows 'first'.
	if strings.Contains(stripStyle(s.Render()), "first") {
		t.Fatalf("live view should not contain scrolled-off 'first'")
	}
	if s.ScrollOffset() != 0 {
		t.Fatalf("fresh offset = %d, want 0", s.ScrollOffset())
	}
	// Scroll up into history: 'first' reappears.
	if n := s.ScrollUp(1); n != 1 {
		t.Fatalf("ScrollUp moved %d lines, want 1", n)
	}
	if s.ScrollOffset() != 1 {
		t.Fatalf("offset after ScrollUp = %d, want 1", s.ScrollOffset())
	}
	if !strings.Contains(stripStyle(s.Render()), "first") {
		t.Fatalf("scrolled-up view should contain 'first': %q", stripStyle(s.Render()))
	}
	// Cannot scroll past the oldest line.
	if n := s.ScrollUp(50); n != 0 {
		t.Fatalf("ScrollUp past top moved %d lines, want 0", n)
	}
	// Scroll back down and snap to the live view.
	s.ScrollDown(1)
	if s.ScrollOffset() != 0 {
		t.Fatalf("offset after ScrollDown = %d, want 0", s.ScrollOffset())
	}
}

func TestScrollToBottomFollowsLive(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("a\r\nb\r\nc"))
	s.ScrollUp(1)
	if s.ScrollOffset() == 0 {
		t.Fatalf("precondition: expected scrolled up")
	}
	s.ScrollToBottom()
	if s.ScrollOffset() != 0 {
		t.Fatalf("ScrollToBottom offset = %d, want 0", s.ScrollOffset())
	}
}

func TestScrollAnchorsWhenNewOutputArrives(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("l1\r\nl2\r\nl3")) // 'l1' in scrollback
	s.ScrollUp(1)                     // viewing 'l1' at top
	view := stripStyle(s.Render())
	if !strings.Contains(view, "l1") {
		t.Fatalf("precondition: expected 'l1' in view, got %q", view)
	}
	// New output scrolls the live region; the anchored view should keep showing
	// 'l1' rather than jumping.
	s.Write([]byte("\r\nl4"))
	if got := stripStyle(s.Render()); !strings.Contains(got, "l1") {
		t.Fatalf("view drifted after new output: %q", got)
	}
}

func TestResetClearsScrollback(t *testing.T) {
	s := New(20, 2)
	s.Write([]byte("a\r\nb\r\nc"))
	s.ScrollUp(1)
	s.Reset()
	if s.ScrollOffset() != 0 {
		t.Fatalf("offset after reset = %d, want 0", s.ScrollOffset())
	}
	if n := s.ScrollUp(5); n != 0 {
		t.Fatalf("scrollback survived reset: moved %d lines", n)
	}
}
