package sbxkit

import (
	"regexp"
	"strconv"
	"strings"
)

// MinSbxVersion is the minimum host sandbox CLI release switchboard supports: the
// first release that accepts kit schema version 2 (feature 008, FR-096). Kit and
// MCP-gateway operations are refused on hosts below it; kit-less sandbox
// operations keep working.
const MinSbxVersion = "0.36.0"

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// ParseVersion extracts the first MAJOR.MINOR.PATCH from `sbx --version` output
// ("sbx version 0.46.0", "sbx 0.46.0 (abc123)"). ok is false when there is none,
// which callers treat as "below baseline" rather than guessing.
func ParseVersion(output string) (version string, ok bool) {
	v := versionRe.FindString(output)
	return v, v != ""
}

// AtLeast reports whether have >= min, comparing numerically per component. An
// unparseable side is never "at least" anything.
func AtLeast(have, min string) bool {
	h, ok1 := parts(have)
	m, ok2 := parts(min)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if h[i] != m[i] {
			return h[i] > m[i]
		}
	}
	return true
}

func parts(v string) ([3]int, bool) {
	var out [3]int
	v, ok := ParseVersion(v)
	if !ok {
		return out, false
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
