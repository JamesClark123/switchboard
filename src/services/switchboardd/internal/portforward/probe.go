package portforward

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	// dialTimeout bounds a single readiness dial. Short: the endpoint is on this
	// host (loopback, or a published sandbox port), so a slow connect means "not
	// ready", not "far away".
	dialTimeout = 500 * time.Millisecond
	// probeInitialBackoff / probeMaxBackoff shape the retry cadence: quick early
	// polls so a fast service is reported ready promptly, backing off so a slow one
	// does not spin the CPU for a minute.
	probeInitialBackoff = 250 * time.Millisecond
	probeMaxBackoff     = 2 * time.Second
	// tcpStateListen is the /proc/net/tcp state code for LISTEN.
	tcpStateListen = "0A"
)

// errWindowElapsed means the readiness window passed with nothing listening.
var errWindowElapsed = errors.New("readiness window elapsed")

// errProcessExited means the service's process died before it became reachable.
var errProcessExited = errors.New("process exited before becoming ready")

// awaitReady polls the host endpoint until something answers, the process dies, the
// readiness window elapses, or the context is cancelled.
//
// A successful dial is the ONLY thing that makes a service RUNNING (FR-047, Key
// Decision 6). "The process started" is not evidence of reachability, and a
// displayed address that does not work is the one failure this feature cannot
// tolerate.
func awaitReady(ctx context.Context, hostPort uint32, window time.Duration, exited <-chan struct{}) error {
	deadline := time.Now().Add(window)
	backoff := probeInitialBackoff
	for {
		if dialOK(hostPort) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			// One last look: a service can legitimately exit right after something
			// else started answering on its behalf (a supervisor handing off), and
			// more commonly the process dies AND the port is dead — this ordering
			// makes the check race-free either way.
			if dialOK(hostPort) {
				return nil
			}
			return errProcessExited
		case <-time.After(backoff):
		}
		if time.Now().After(deadline) {
			if dialOK(hostPort) {
				return nil
			}
			return errWindowElapsed
		}
		if backoff *= 2; backoff > probeMaxBackoff {
			backoff = probeMaxBackoff
		}
	}
}

// dialOK reports whether something accepts a TCP connection on the loopback port.
func dialOK(port uint32) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), dialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// listenState is what /proc says about a port inside the sandbox.
type listenState int

const (
	listenNone         listenState = iota // nothing is listening
	listenLoopbackOnly                    // bound to 127.0.0.1 / ::1 only — unreachable from outside
	listenAll                             // bound to all interfaces — reachable
)

// awaitSandboxListening polls /proc inside the sandbox until the service is bound
// to all interfaces, the process dies, or the window elapses.
//
// Dialling the PUBLISHED host port cannot serve as the readiness test for an
// in-sandbox service: the publish itself binds that port on the host and accepts
// connections whether or not anything is listening on the other side, so a dial
// would report "ready" for a service that never started. Asking the sandbox
// directly is the only answer that means what it says — and it distinguishes the
// three outcomes FR-047 needs to tell apart (research R5).
func (s *Supervisor) awaitSandboxListening(ctx context.Context, ref string, port uint32, window time.Duration, exited <-chan struct{}) (listenState, error) {
	deadline := time.Now().Add(window)
	backoff := probeInitialBackoff
	last := listenNone
	for {
		if st := s.sandboxListenState(ctx, ref, port); st == listenAll {
			return st, nil
		} else if st != listenNone {
			last = st
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-exited:
			if st := s.sandboxListenState(ctx, ref, port); st == listenAll {
				return st, nil
			}
			return last, errProcessExited
		case <-time.After(backoff):
		}
		if time.Now().After(deadline) {
			if st := s.sandboxListenState(ctx, ref, port); st == listenAll {
				return st, nil
			} else if st != listenNone {
				last = st
			}
			return last, errWindowElapsed
		}
		if backoff *= 2; backoff > probeMaxBackoff {
			backoff = probeMaxBackoff
		}
	}
}

// sandboxListenState reads /proc/net/tcp and /proc/net/tcp6 IN the sandbox.
//
// /proc needs no tooling in the image: `ss`, `netstat`, `lsof`, and `nc` can none
// of them be assumed present, but /proc is always there on Linux.
func (s *Supervisor) sandboxListenState(ctx context.Context, ref string, port uint32) listenState {
	cmd := s.runner.Exec(ctx, ref, []string{"/bin/sh", "-c", "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null"})
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return listenNone
	}
	return parseListenState(string(out), port)
}

// parseListenState scans /proc/net/tcp{,6} content for LISTEN rows on port.
//
// An all-interfaces bind anywhere wins: a dual-stack server often binds loopback
// AND the wildcard, and that service is perfectly reachable.
func parseListenState(procNetTCP string, port uint32) listenState {
	state := listenNone
	for _, line := range strings.Split(procNetTCP, "\n") {
		fields := strings.Fields(line)
		// sl local_address rem_address st ...
		if len(fields) < 4 || fields[3] != tcpStateListen {
			continue
		}
		addrHex, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(portHex, 16, 32)
		if err != nil || uint32(p) != port {
			continue
		}
		if isAnyAddrHex(addrHex) {
			return listenAll
		}
		if isLoopbackAddrHex(addrHex) {
			state = listenLoopbackOnly
		}
	}
	return state
}

// parseLoopbackOnly reports whether a port is bound to loopback and nothing else.
func parseLoopbackOnly(procNetTCP string, port uint32) bool {
	return parseListenState(procNetTCP, port) == listenLoopbackOnly
}

// isAnyAddrHex reports whether a /proc local_address is the wildcard (0.0.0.0, ::,
// or the v4-mapped ::ffff:0.0.0.0).
func isAnyAddrHex(hex string) bool {
	return v4PartOf(hex) == "00000000"
}

// isLoopbackAddrHex reports whether a /proc local_address is a loopback address.
// /proc renders each 32-bit word little-endian, so 127.0.0.1 is "0100007F" and ::1
// is "00000000000000000000000001000000".
func isLoopbackAddrHex(hex string) bool {
	if v4 := v4PartOf(hex); v4 != "" {
		// 127.0.0.0/8 — little-endian, so the last byte of the word is the first
		// octet of the address.
		return strings.EqualFold(v4[6:8], "7F")
	}
	return strings.EqualFold(hex, "00000000000000000000000001000000")
}

// v4PartOf returns the IPv4 word of a /proc address, unwrapping a v4-mapped IPv6
// address, or "" when the address is a genuine IPv6 one.
func v4PartOf(hex string) string {
	switch len(hex) {
	case 8:
		return hex
	case 32:
		// ::ffff:a.b.c.d renders as 16 zeros, then FFFF0000, then the v4 word.
		if strings.EqualFold(hex[16:24], "FFFF0000") {
			return hex[24:]
		}
		if hex == strings.Repeat("0", 32) {
			return "00000000" // :: is the wildcard, same as 0.0.0.0
		}
	}
	return ""
}
