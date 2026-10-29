package orchestrator

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Untrusted values (research R13 §1): steps are typed as a bracketed paste, so
// a call value carrying ESC[201~ would end the paste early and a following CR
// would submit the rest as a slash command or a `!` shell line. Call values
// come from worker agents that read ticket and PR text, so every SUBSTITUTED
// value (call arguments and their defaults) is checked here. Authored action
// text and escape-hatch results are never filtered: only checkValue's callers
// (validateCall, validateAction's defaults, compose/composeStep) use it.

var (
	errControlChar = errors.New("contains a control character")
	errInvalidUTF8 = errors.New("is not valid UTF-8")
)

// checkValue normalises CRLF and lone CR to LF, then refuses invalid UTF-8 (a
// raw 0x9B byte is an 8-bit CSI) and ESC, C0 controls other than \n and \t,
// DEL and C1 controls. It returns the normalised value.
func checkValue(v string) (string, error) {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	if !utf8.ValidString(v) {
		return "", errInvalidUTF8
	}
	for _, r := range v {
		switch {
		case r == '\n' || r == '\t':
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return "", errControlChar
		}
	}
	return v, nil
}
