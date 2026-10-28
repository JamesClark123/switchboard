package terminal

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// fastNudges shortens the repaint debounce and gap for one test.
func fastNudges(t *testing.T) {
	t.Helper()
	every, gap := repaintEvery, nudgeGap
	repaintEvery, nudgeGap = 0, time.Millisecond
	t.Cleanup(func() { repaintEvery, nudgeGap = every, gap })
}

// screenText joins a snapshot's rows for substring checks.
func screenText(lines []termview.Line) string {
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// resizesOf returns a copy of the PTY's recorded resizes.
func resizesOf(p *fakePTY) [][2]uint16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][2]uint16(nil), p.resizes...)
}

// waitFresh nudges and feeds a repaint until the screen reads fresh.
func waitFresh(t *testing.T, b *Broadcaster, p *fakePTY, repaint string) {
	t.Helper()
	waitFor(t, func() bool {
		if _, stale := b.Screen(); !stale {
			return true
		}
		b.RequestRepaint()
		time.Sleep(5 * time.Millisecond)
		p.feed([]byte(repaint))
		return false
	})
}

func TestScreenShowsWrittenBytes(t *testing.T) {
	fastNudges(t)
	p := newFakePTY()
	b := newBroadcaster(p, 0, nil, true)
	defer func() { _ = b.Close() }()

	waitFresh(t, b, p, "\x1b[H\x1b[2J❯ hello")
	lines, stale := b.Screen()
	if stale || !strings.Contains(screenText(lines), "❯ hello") {
		t.Fatalf("Screen() = %q stale=%v, want the written bytes", screenText(lines), stale)
	}
	// The copy is a snapshot: later output does not change it.
	p.feed([]byte("\x1b[H\x1b[2Jlater"))
	waitFor(t, func() bool { l, _ := b.Screen(); return strings.Contains(screenText(l), "later") })
	if !strings.Contains(screenText(lines), "hello") {
		t.Fatal("an earlier snapshot changed after later output")
	}
}

func TestClientlessScreenGetsStepSizeAndNudge(t *testing.T) {
	fastNudges(t)
	p := newFakePTY()
	b := newBroadcaster(p, 0, nil, true)
	defer func() { _ = b.Close() }()

	full := [2]uint16{stepScreenCols, stepScreenRows}
	small := [2]uint16{stepScreenCols, stepScreenRows - 1}
	waitFor(t, func() bool { return len(resizesOf(p)) >= 3 })
	got := resizesOf(p)[:3]
	if got[0] != full || got[1] != small || got[2] != full {
		t.Fatalf("resizes = %v, want %v, %v (nudge), %v", got, full, small, full)
	}
	lines, _ := b.Screen()
	if len(lines) != stepScreenRows {
		t.Fatalf("screen has %d rows, want %d", len(lines), stepScreenRows)
	}
}

func TestScreenResizeFollowsPTY(t *testing.T) {
	fastNudges(t)
	p := newFakePTY()
	b := newBroadcaster(p, 0, nil, true)
	defer func() { _ = b.Close() }()

	c, err := b.Attach(KindInTUI, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	if lines, _ := b.Screen(); len(lines) != 30 {
		t.Fatalf("screen rows after attach = %d, want 30", len(lines))
	}
	c.Resize(90, 20)
	if lines, _ := b.Screen(); len(lines) != 20 {
		t.Fatalf("screen rows after resize = %d, want 20", len(lines))
	}
	// A detach leaves the PTY (and the screen) at the last size.
	c.Close()
	if lines, _ := b.Screen(); len(lines) != 20 {
		t.Fatalf("screen rows after detach = %d, want 20", len(lines))
	}
}

func TestScreenRacesCleanlyWithReadLoop(t *testing.T) {
	fastNudges(t)
	p := newFakePTY()
	b := newBroadcaster(p, 0, nil, true)
	defer func() { _ = b.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			p.feed([]byte("line\r\n"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = b.Screen()
			b.RequestRepaint()
		}
	}()
	wg.Wait()
}

func TestSlowParserNeverBlocksFanOut(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	beforeParse = func() { <-release }
	t.Cleanup(func() { beforeParse = nil; unblock() })

	p := newFakePTY()
	b := newBroadcaster(p, 0, nil, true)
	defer func() { _ = b.Close() }()
	c, err := b.Attach(KindInTUI, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	// With the parser stalled, more chunks than the screen queue holds still
	// reach the client.
	go func() {
		for i := 0; i < screenQueue+50; i++ {
			p.feed([]byte("x"))
		}
	}()
	for i := 0; i < screenQueue+50; i++ {
		recv(t, c.Out)
	}
	if _, stale := b.Screen(); !stale {
		t.Fatal("an overflowing screen queue should mark the screen stale")
	}
	unblock()
}

func TestOverflowResetsAndRepaintRecovers(t *testing.T) {
	fastNudges(t)
	release := make(chan struct{})
	beforeParse = func() { <-release }
	t.Cleanup(func() { beforeParse = nil })

	p := newFakePTY()
	b := newBroadcaster(p, 0, nil, true)
	defer func() { _ = b.Close() }()

	p.feed([]byte("old-content"))
	for i := 0; i < screenQueue+10; i++ {
		p.feed([]byte("y"))
	}
	waitFor(t, func() bool {
		b.scr.Load().flagMu.Lock()
		defer b.scr.Load().flagMu.Unlock()
		return b.scr.Load().dropped
	})
	close(release)

	// The parser resets the screen: nothing of the dropped stream survives, and
	// it stays stale until a repaint is parsed after a nudge.
	waitFor(t, func() bool { l, _ := b.Screen(); return !strings.Contains(screenText(l), "old-content") })
	if _, stale := b.Screen(); !stale {
		t.Fatal("screen should stay stale until a repaint")
	}
	waitFresh(t, b, p, "\x1b[H\x1b[2J❯ repainted")
	lines, _ := b.Screen()
	if !strings.Contains(screenText(lines), "❯ repainted") {
		t.Fatalf("screen after recovery = %q", screenText(lines))
	}
}

func TestRepaintIsDebounced(t *testing.T) {
	p := newFakePTY()
	b := newBroadcaster(p, 0, nil, true) // the enabling nudge counts
	defer func() { _ = b.Close() }()
	if b.RequestRepaint() {
		t.Fatal("a second nudge within repaintEvery should be refused")
	}
	n := len(resizesOf(p))
	b.mu.Lock()
	b.lastNudge = time.Now().Add(-2 * repaintEvery)
	b.mu.Unlock()
	if !b.RequestRepaint() {
		t.Fatal("a nudge after repaintEvery should be issued")
	}
	if got := resizesOf(p); len(got) <= n || got[len(got)-1] != [2]uint16{stepScreenCols, stepScreenRows - 1} {
		t.Fatalf("nudge did not shrink the PTY by one row: %v", got)
	}
}

func TestNoNudgeWhenClosedOrUnsized(t *testing.T) {
	p := newFakePTY()
	b := New(p, 0, nil)
	if b.RequestRepaint() {
		t.Fatal("an unsized PTY should not be nudged")
	}
	_ = b.Close()
	b.EnableScreen()
	if b.HasScreen() {
		t.Fatal("a closed broadcaster should not get a screen")
	}
}

func TestUncontrolledBroadcasterKeepsNoScreen(t *testing.T) {
	p := newFakePTY()
	b := New(p, 0, nil)
	defer func() { _ = b.Close() }()
	p.feed([]byte("hello"))
	if lines, stale := b.Screen(); lines != nil || stale || b.HasScreen() {
		t.Fatalf("Screen() = %v, %v; want no screen", lines, stale)
	}
	if len(resizesOf(p)) != 0 {
		t.Fatal("an uncontrolled, client-less broadcaster should leave the PTY size alone")
	}

	reg := NewRegistry(func(string) (PTY, error) { return newFakePTY(), nil }, 0, nil)
	reg.SetScreenFor(func(id string) bool { return id == "worker" })
	if _, err := reg.Broadcaster("plain"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Screen("plain"); ok {
		t.Fatal("an uncontrolled sandbox should keep no screen")
	}
	if _, err := reg.Broadcaster("worker"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Screen("worker"); !ok {
		t.Fatal("a controlled sandbox's broadcaster should keep a screen")
	}
	reg.Close("plain")
	reg.Close("worker")
}

func TestEnableScreenOnExistingBroadcaster(t *testing.T) {
	fastNudges(t)
	p := newFakePTY()
	reg := NewRegistry(func(string) (PTY, error) { return p, nil }, 0, nil)
	b, err := reg.Broadcaster("sb1") // created before the sandbox became a worker
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close("sb1")
	c, err := b.Attach(KindInTUI, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	if reg.EnableScreen("nope") {
		t.Fatal("EnableScreen must not create a broadcaster")
	}
	for i := 0; i < 2; i++ { // idempotent
		if !reg.EnableScreen("sb1") {
			t.Fatalf("EnableScreen call %d should report a screen", i+1)
		}
	}
	// With a client attached the screen takes the PTY's size, and a repaint is
	// nudged.
	lines, stale := b.Screen()
	if len(lines) != 30 || !stale {
		t.Fatalf("screen rows = %d stale=%v, want 30 rows, stale until a repaint", len(lines), stale)
	}
	waitFor(t, func() bool {
		r := resizesOf(p)
		return len(r) >= 3 && r[len(r)-2] == [2]uint16{100, 29} && r[len(r)-1] == [2]uint16{100, 30}
	})
	waitFresh(t, b, p, "\x1b[H\x1b[2J❯ ")
	c.Close()

	if reg.RequestRepaint("nope") {
		t.Fatal("RequestRepaint must not create a broadcaster")
	}
	if !reg.RequestRepaint("sb1") {
		t.Fatal("RequestRepaint on a live broadcaster should nudge (debounce is 0)")
	}
}

// spawnCounter is a provider that records whether it was called.
type spawnCounter struct {
	mu    sync.Mutex
	calls int
}

func (s *spawnCounter) provider(string) (PTY, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return newFakePTY(), nil
}

func (s *spawnCounter) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestScreenAndBroadcasterExistingNeverSpawn(t *testing.T) {
	spawn := &spawnCounter{}
	reg := NewRegistry(spawn.provider, 0, nil)
	reg.SetScreenFor(func(string) bool { return true })

	// No broadcaster: Screen reports none and creates nothing.
	if _, _, ok := reg.Screen("sb1"); ok {
		t.Fatal("Screen with no broadcaster should report none")
	}
	// No existing-session provider configured.
	if _, err := reg.BroadcasterExisting("sb1"); !errors.Is(err, ErrNoExisting) {
		t.Fatalf("BroadcasterExisting without a provider err = %v", err)
	}
	// No live agent: the existing provider refuses; nothing is created.
	errNoAgent := errors.New("no live agent")
	live := map[string]PTY{}
	var mu sync.Mutex
	reg.SetExisting(func(id string) (PTY, error) {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := live[id]; ok {
			return p, nil
		}
		return nil, errNoAgent
	})
	if _, err := reg.BroadcasterExisting("sb1"); !errors.Is(err, errNoAgent) {
		t.Fatalf("BroadcasterExisting with no live agent err = %v, want %v", err, errNoAgent)
	}
	if _, _, ok := reg.Screen("sb1"); ok || spawn.n() != 0 {
		t.Fatalf("a refused BroadcasterExisting created something (spawns=%d)", spawn.n())
	}

	// A live agent: the broadcaster is built over it, with a screen.
	p := newFakePTY()
	mu.Lock()
	live["sb1"] = p
	mu.Unlock()
	b, err := reg.BroadcasterExisting("sb1")
	if err != nil || !b.HasScreen() {
		t.Fatalf("BroadcasterExisting over a live agent = %v, %v", b, err)
	}
	if again, _ := reg.BroadcasterExisting("sb1"); again != b {
		t.Fatal("BroadcasterExisting should reuse the open broadcaster")
	}

	// The agent exits mid-call: the session ends, and neither call spawns a
	// replacement.
	_ = p.Close()
	mu.Lock()
	delete(live, "sb1")
	mu.Unlock()
	waitFor(t, func() bool { return b.IsClosed() })
	if _, _, ok := reg.Screen("sb1"); ok {
		t.Fatal("Screen of an ended session should report none")
	}
	if _, err := reg.BroadcasterExisting("sb1"); !errors.Is(err, errNoAgent) {
		t.Fatalf("BroadcasterExisting after the agent exited err = %v", err)
	}
	if spawn.n() != 0 {
		t.Fatalf("the spawning provider was called %d times", spawn.n())
	}
}
