package portforward

import (
	"context"
	"fmt"
	"net"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// allocRetries bounds the publish retry loop. Allocation is inherently TOCTOU —
// the OS hands out a free port, we close the probe listener, and only then publish
// — so a racing process can take it in between. Three attempts is plenty for a race
// this narrow, and failing after them beats spinning (research R2).
const allocRetries = 3

// freeLoopbackPort asks the OS for a free port on the loopback interface by binding
// :0 and immediately releasing it.
//
// The returned port is free as of this instant, not reserved: the caller must use
// it promptly and be able to retry. Scanning a fixed range instead was rejected —
// it collides with unrelated host software and needs a persisted cursor.
func freeLoopbackPort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", l.Addr())
	}
	return uint32(addr.Port), nil
}

// portFree reports whether nothing is listening on 127.0.0.1:port right now.
func portFree(port uint32) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), dialTimeout)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

// publishSandboxPort allocates a free port on the daemon host and publishes the
// sandbox's listen port onto it, returning the host port that ForwardPort will dial.
//
// Retries cover the allocation race described above: if `sbx ports` rejects the
// port because something grabbed it first, a fresh port is drawn rather than
// failing the whole start.
func (s *Supervisor) publishSandboxPort(ctx context.Context, ref string, listenPort uint32) (uint32, error) {
	var lastErr error
	for attempt := 0; attempt < allocRetries; attempt++ {
		hostPort, err := freeLoopbackPort()
		if err != nil {
			lastErr = err
			continue
		}
		if err := s.runner.PublishPort(ctx, ref, hostPort, listenPort); err != nil {
			lastErr = err
			continue
		}
		return hostPort, nil
	}
	return 0, fmt.Errorf("could not publish port %d after %d attempts: %w", listenPort, allocRetries, lastErr)
}

// effectivePort decides what port a service's command will actually bind, and
// returns the command with any {{port}} token substituted (research R4).
//
//   - In-sandbox: always the declared port. The sandbox has its own network
//     namespace, so the declared port cannot collide with anything, and the publish
//     mapping is built from it.
//   - On-host with {{port}}: a freshly allocated host port, so two sandboxes running
//     the same service coexist (US3-3).
//   - On-host without {{port}}: the declared port. A second concurrent instance will
//     be refused with PORT_IN_USE (US3-4).
//
// Which of the last two a developer gets is the author's explicit, visible choice.
func (s *Supervisor) effectivePort(svc *pb.KitService) (port uint32, command string, err error) {
	command = svc.GetCommand()
	if svc.GetLocation() != pb.ServiceLocation_SERVICE_LOCATION_ON_HOST ||
		!strings.Contains(command, PortPlaceholder) {
		return svc.GetListenPort(), command, nil
	}
	allocated, err := freeLoopbackPort()
	if err != nil {
		return 0, "", err
	}
	return allocated, strings.ReplaceAll(command, PortPlaceholder, fmt.Sprint(allocated)), nil
}
