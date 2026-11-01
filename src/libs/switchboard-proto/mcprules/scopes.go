package mcprules

import "strings"

// ScopeCommaAllowed lifts the comma refusal (FR-417) once the real-host run
// shows the scope flags take a comma literally. A constant shared by both
// binaries, so the TUI and the daemon always agree (research R1).
const ScopeCommaAllowed = false

// Scope bounds (switchboard's own, FR-417).
const (
	MaxScopes   = 32
	MaxScopeLen = 256
)

// ValidScopes applies the OAuth scope-token rule (FR-417).
func ValidScopes(scopes []string) error {
	if len(scopes) > MaxScopes {
		return invalid("at most %d scopes are allowed", MaxScopes)
	}
	seen := map[string]bool{}
	for _, s := range scopes {
		switch {
		case s == "":
			return invalid("a scope cannot be empty")
		case len(s) > MaxScopeLen:
			return invalid("scope %q is longer than %d characters", s[:32]+"…", MaxScopeLen)
		case s[0] == '-':
			return invalid("scope %q must not start with \"-\"", s)
		case seen[s]:
			return invalid("scope %q is given more than once", s)
		}
		for _, r := range s {
			if r < 0x21 || r > 0x7e || r == '"' || r == '\\' {
				return invalid("scope %q must be printable ASCII without spaces, quotes or backslashes", s)
			}
			if r == ',' && !ScopeCommaAllowed {
				return invalid("scope %q contains a comma, which the host CLI may split on", s)
			}
		}
		seen[s] = true
	}
	return nil
}

// ParseScopes splits a space-separated scope line.
func ParseScopes(line string) []string { return strings.Fields(line) }
