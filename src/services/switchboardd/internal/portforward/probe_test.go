package portforward

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func listenLoopback(t *testing.T) (net.Listener, uint32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, uint32(ln.Addr().(*net.TCPAddr).Port)
}

func TestAwaitReadySucceedsOnceSomethingListens(t *testing.T) {
	_, port := listenLoopback(t)
	if err := awaitReady(context.Background(), port, time.Second, make(chan struct{})); err != nil {
		t.Errorf("a live listener must be reported ready, got %v", err)
	}
}

func TestAwaitReadyWaitsForALateListener(t *testing.T) {
	// A cold dev server binds after a delay; readiness must poll, not sample once.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	go func() {
		time.Sleep(300 * time.Millisecond)
		late, err := net.Listen("tcp", "127.0.0.1:"+itoa(port))
		if err == nil {
			time.Sleep(2 * time.Second)
			_ = late.Close()
		}
	}()

	if err := awaitReady(context.Background(), port, 3*time.Second, make(chan struct{})); err != nil {
		t.Errorf("a listener that appears mid-window must be caught, got %v", err)
	}
}

// SC-007: nothing listening means FAILED, never RUNNING.
func TestAwaitReadyReportsWindowElapsed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close() // nothing is listening now

	start := time.Now()
	err = awaitReady(context.Background(), port, 400*time.Millisecond, make(chan struct{}))
	if !errors.Is(err, errWindowElapsed) {
		t.Fatalf("err = %v, want errWindowElapsed", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("gave up after %v, well before the window", elapsed)
	}
}

func TestAwaitReadyReportsProcessExit(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	err := awaitReady(context.Background(), 1, time.Second, exited)
	if !errors.Is(err, errProcessExited) {
		t.Errorf("err = %v, want errProcessExited", err)
	}
}

// A process can exit right as its port becomes reachable; the port wins, because
// reachability is what RUNNING actually claims.
func TestAwaitReadyPrefersAReachablePortOverAnExitedProcess(t *testing.T) {
	_, port := listenLoopback(t)
	exited := make(chan struct{})
	close(exited)
	if err := awaitReady(context.Background(), port, time.Second, exited); err != nil {
		t.Errorf("a reachable port must win over an exited process, got %v", err)
	}
}

func TestAwaitReadyHonoursCancellation(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()

	err := awaitReady(ctx, port, 10*time.Second, make(chan struct{}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// --- /proc/net/tcp parsing (research R5) -------------------------------------

// Real-shape rows: sl, local_address, rem_address, st, …
const (
	procLoopback4 = "  0: 0100007F:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 12345 1\n"
	procAny4      = "  1: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 12346 1\n"
	procLoopback6 = "  0: 00000000000000000000000001000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 12347 1\n"
	procAny6      = "  1: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 12348 1\n"
	procEstab4    = "  2: 0100007F:0BB8 0100007F:9999 01 00000000:00000000 00:00000000 00000000  1000 0 12349 1\n"
	procHeader    = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
)

const port3000 = 3000 // 0x0BB8

func TestParseListenStateClassifiesEachBind(t *testing.T) {
	tests := []struct {
		name string
		proc string
		want listenState
	}{
		{"nothing", procHeader, listenNone},
		{"loopback v4", procHeader + procLoopback4, listenLoopbackOnly},
		{"loopback v6", procHeader + procLoopback6, listenLoopbackOnly},
		{"all interfaces v4", procHeader + procAny4, listenAll},
		{"all interfaces v6", procHeader + procAny6, listenAll},
		{"dual bind", procHeader + procLoopback4 + procAny6, listenAll},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseListenState(tc.proc, port3000); got != tc.want {
				t.Errorf("parseListenState = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseLoopbackOnlyDetectsIPv4Loopback(t *testing.T) {
	if !parseLoopbackOnly(procHeader+procLoopback4, port3000) {
		t.Error("127.0.0.1:3000 in LISTEN must be diagnosed as loopback-only")
	}
}

func TestParseLoopbackOnlyDetectsIPv6Loopback(t *testing.T) {
	if !parseLoopbackOnly(procHeader+procLoopback6, port3000) {
		t.Error("[::1]:3000 in LISTEN must be diagnosed as loopback-only")
	}
}

func TestParseLoopbackOnlyIgnoresAnAllInterfacesBind(t *testing.T) {
	if parseLoopbackOnly(procHeader+procAny4, port3000) {
		t.Error("0.0.0.0:3000 is reachable — not a loopback problem")
	}
	if parseLoopbackOnly(procHeader+procAny6, port3000) {
		t.Error(":::3000 is reachable — not a loopback problem")
	}
}

// A dual-stack server often binds both; the all-interfaces entry means it is fine.
func TestParseLoopbackOnlyRequiresEveryBindToBeLoopback(t *testing.T) {
	if parseLoopbackOnly(procHeader+procLoopback4+procAny6, port3000) {
		t.Error("a service bound to both loopback and all interfaces is reachable")
	}
}

func TestParseLoopbackOnlyWithNothingListening(t *testing.T) {
	if parseLoopbackOnly(procHeader, port3000) {
		t.Error("no LISTEN row means 'never listened', not 'loopback only'")
	}
	// An ESTABLISHED row on the same port is not a listener.
	if parseLoopbackOnly(procHeader+procEstab4, port3000) {
		t.Error("only LISTEN (state 0A) rows count")
	}
}

func TestParseLoopbackOnlyIgnoresOtherPorts(t *testing.T) {
	if parseLoopbackOnly(procHeader+procLoopback4, 9999) {
		t.Error("a row for a different port must not match")
	}
}

func TestParseLoopbackOnlyToleratesGarbage(t *testing.T) {
	for _, junk := range []string{"", "not a table", "  0: nonsense 0A\n", "  0: ZZZZ:ZZZZ x 0A\n"} {
		if parseLoopbackOnly(junk, port3000) {
			t.Errorf("unparseable input %q must not produce a diagnosis", junk)
		}
	}
}

func TestSandboxListenStateRunsTheProcReadInTheSandbox(t *testing.T) {
	s, _, runner, _ := newTestSupervisor(t)
	// The fake Runner executes argv on the host; `cat` of a missing path yields
	// nothing, which must read as "nothing listening" rather than a false positive.
	if got := s.sandboxListenState(context.Background(), "sb1-ref", 3000); got != listenNone {
		t.Errorf("an empty /proc read must report listenNone, got %v", got)
	}
	argv := runner.lastExec()
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "/proc/net/tcp") || !strings.Contains(joined, "/proc/net/tcp6") {
		t.Errorf("probe argv = %v, want it to read both /proc/net/tcp and /proc/net/tcp6", argv)
	}
}

func TestV4PartOfHandlesMappedAddresses(t *testing.T) {
	if got := v4PartOf("0000000000000000FFFF00000100007F"); got != "0100007F" {
		t.Errorf("v4-mapped ::ffff:127.0.0.1 unwrapped to %q", got)
	}
	if got := v4PartOf(strings.Repeat("0", 32)); got != "00000000" {
		t.Errorf(":: must read as the wildcard, got %q", got)
	}
	if got := v4PartOf("00000000000000000000000001000000"); got != "" {
		t.Errorf("::1 is a genuine IPv6 address, got v4 part %q", got)
	}
}
