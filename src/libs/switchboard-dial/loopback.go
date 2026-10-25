package dial

import (
	"net"
	"strings"
)

// IsLoopbackHost reports whether host (as URL.Hostname() returns it) names this
// machine's loopback: "localhost" and every "*.localhost" (RFC 6761), or any
// loopback IP literal. Case-insensitive, and one trailing dot ("localhost.") is
// the same host.
//
// It is a validation-only rule — it decides whether a redirect/paste address
// looks like a local callback, and performs no DNS. Callers that connect must use
// a fixed address and never dial the checked host. The TUI's paste check and the
// daemon's callback handling share this one definition so they cannot drift.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
