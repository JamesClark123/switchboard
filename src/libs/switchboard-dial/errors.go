package dial

import "strings"

// DialErrorKind classifies why an ssh-backed dial failed, from ssh's stderr.
type DialErrorKind int

const (
	DialOther       DialErrorKind = iota
	DialAuthFailed                // ssh rejected the credentials: a password may help
	DialUnreachable               // the host could not be reached at all
	DialNoDaemon                  // reached, but sxbd is not installed there
)

func (k DialErrorKind) String() string {
	switch k {
	case DialAuthFailed:
		return "authentication failed"
	case DialUnreachable:
		return "host unreachable"
	case DialNoDaemon:
		return "sxbd not found on the host"
	}
	return "connection failed"
}

// DialError wraps a failed ssh-backed dial with its classification and the tail
// of ssh's stderr, so a client can decide whether a password prompt is worth
// showing (FR-088) and tell the developer why otherwise (FR-089).
type DialError struct {
	Kind   DialErrorKind
	Err    error
	Stderr string
}

func (e *DialError) Error() string {
	msg := e.Kind.String()
	if line := lastLine(e.Stderr); line != "" {
		msg += ": " + line
	}
	return msg
}

func (e *DialError) Unwrap() error { return e.Err }

// ClassifyDialStderr maps ssh's stderr to a DialErrorKind.
func ClassifyDialStderr(stderr string) DialErrorKind {
	l := strings.ToLower(stderr)
	switch {
	case strings.Contains(l, "permission denied") || strings.Contains(l, "publickey") || strings.Contains(l, "password") || strings.Contains(l, "authentication"):
		return DialAuthFailed
	case strings.Contains(l, "sxbd: command not found") || strings.Contains(l, "sxbd: not found") || strings.Contains(l, "command not found"):
		return DialNoDaemon
	case strings.Contains(l, "connection refused") || strings.Contains(l, "timed out") || strings.Contains(l, "could not resolve") ||
		strings.Contains(l, "no route to host") || strings.Contains(l, "network is unreachable") || strings.Contains(l, "connection reset"):
		return DialUnreachable
	}
	return DialOther
}

// lastLine returns the last non-blank line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
