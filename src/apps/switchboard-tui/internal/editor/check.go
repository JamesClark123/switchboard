package editor

import (
	"regexp"
	"strings"
)

// The Custom command's markers (FR-515).
const (
	PathMarker = "{path}"
	HostMarker = "{host}"
)

// assignmentRe matches a first word that would be a shell variable assignment,
// which nothing sets here (FR-506).
var assignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// Check applies FR-506's rules to a split command: no words (empty), an empty
// first word (empty-executable), a marker in the first word
// (marker-in-executable), and a first word that is NAME=… (assignment). The
// check runs on the split word, so quoting doesn't matter.
func Check(words []Word) error {
	if len(words) == 0 {
		return &CheckError{Rule: RuleEmpty}
	}
	first := words[0].Text
	switch {
	case first == "":
		return &CheckError{Rule: RuleEmptyExecutable}
	case strings.Contains(first, PathMarker) || strings.Contains(first, HostMarker):
		return &CheckError{Rule: RuleMarkerInExecutable}
	case assignmentRe.MatchString(first):
		return &CheckError{Rule: RuleAssignment}
	}
	return nil
}

// Expand applies the `~` expansion (FR-531): a word with TildeOK becomes home
// or home + the rest. With no home directory and such a word it refuses
// (no-home). Nothing else expands.
func Expand(words []Word, home string) ([]string, error) {
	out := make([]string, len(words))
	for i, w := range words {
		if w.TildeOK {
			if home == "" {
				return nil, &CheckError{Rule: RuleNoHome}
			}
			out[i] = home + w.Text[1:]
			continue
		}
		out[i] = w.Text
	}
	return out, nil
}

// Substitute replaces the markers in one pass over the original args (FR-515,
// FR-516): in every arg {path} becomes path and {host} becomes host (empty
// when local); an arg that is exactly {host} on a local sandbox is dropped;
// when no arg contained {path}, the path is appended as the last argument. A
// marker inside a substituted value is never expanded again, because each
// arg is scanned once and the replaced text is skipped.
func Substitute(args []string, path, host string) (argv []string, hadPath bool) {
	argv = make([]string, 0, len(args)+1)
	for _, a := range args {
		if a == HostMarker && host == "" {
			continue
		}
		s, p := substituteWord(a, path, host)
		hadPath = hadPath || p
		argv = append(argv, s)
	}
	if !hadPath {
		argv = append(argv, path)
	}
	return argv, hadPath
}

// HasHostMarker reports whether any arg contains {host}.
func HasHostMarker(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, HostMarker) {
			return true
		}
	}
	return false
}

// substituteWord replaces the markers inside one arg in a single pass.
func substituteWord(arg, path, host string) (string, bool) {
	if !strings.Contains(arg, "{") {
		return arg, false
	}
	var b strings.Builder
	hadPath := false
	for i := 0; i < len(arg); {
		switch {
		case strings.HasPrefix(arg[i:], PathMarker):
			b.WriteString(path)
			i += len(PathMarker)
			hadPath = true
		case strings.HasPrefix(arg[i:], HostMarker):
			b.WriteString(host)
			i += len(HostMarker)
		default:
			b.WriteByte(arg[i])
			i++
		}
	}
	return b.String(), hadPath
}
