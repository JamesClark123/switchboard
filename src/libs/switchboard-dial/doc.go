// Package dial is the transport every switchboard client shares to reach a
// daemon (feature 009, research R2): a local Unix socket, or `ssh <target> sxbd
// dial-stdio` bridged through a child process's stdio (the Docker-CLI pattern).
// The TUI's internal/client package and the daemon's orchestrator runtime both
// dial through it, so the stdio bridge, the SSH hardening flags and the
// stderr-based failure classification are single-sourced.
//
// Conn is the handshake-verified connection: it carries the daemon's advertised
// DaemonInfo and exposes the raw generated stub. Typed RPC wrappers live with
// their callers (the TUI's client package), not here.
package dial
