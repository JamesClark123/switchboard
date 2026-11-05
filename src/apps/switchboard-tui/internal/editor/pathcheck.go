package editor

import (
	"fmt"
	"path/filepath"
	"unicode"
	"unicode/utf8"
)

// The path rules (FR-517).
const (
	RulePathUnsafe  = "path-unsafe"
	RulePathLimited = "path-limited"
)

// checkPath applies FR-517 to the workspace folder the daemon returned: it
// must be absolute with no control character (path-unsafe). When limited — a
// remote sandbox with Custom, or with the selected editor's own variable set
// to something other than its default — the program may re-parse the path, so
// it must also be valid UTF-8 and hold only Unicode letters, marks and digits,
// the ASCII space and / . _ - + = @ , : (path-limited, naming the first
// rejected character, as 'x' or U+XXXX when it can't be shown).
func checkPath(path string, limited bool) *Refusal {
	if !filepath.IsAbs(path) {
		return &Refusal{Rule: RulePathUnsafe, Where: WhereDaemon}
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return &Refusal{Rule: RulePathUnsafe, Where: WhereDaemon}
		}
	}
	if !limited {
		return nil
	}
	for i := 0; i < len(path); {
		r, size := utf8.DecodeRuneInString(path[i:])
		if r == utf8.RuneError && size == 1 {
			return &Refusal{Rule: RulePathLimited, Detail: "invalid UTF-8", Where: WhereDaemon}
		}
		i += size
		if pathRuneOK(r) {
			continue
		}
		return &Refusal{Rule: RulePathLimited, Detail: describeRune(r), Where: WhereDaemon}
	}
	return nil
}

// pathRuneOK reports a rune FR-517's limit allows.
func pathRuneOK(r rune) bool {
	if r == ' ' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '/', '.', '_', '-', '+', '=', '@', ',', ':':
		return true
	}
	return false
}

// describeRune names a rejected character safely: quoted when printable,
// else its code point.
func describeRune(r rune) string {
	if unicode.IsPrint(r) {
		return "'" + string(r) + "'"
	}
	return fmt.Sprintf("U+%04X", r)
}
