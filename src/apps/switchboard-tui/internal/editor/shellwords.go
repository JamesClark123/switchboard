package editor

import (
	"strings"
	"unicode"
)

// Word is one argument of a split Custom command (FR-531).
type Word struct {
	Text string
	// TildeOK: the word began with an unquoted, unescaped `~` and is exactly
	// `~` or starts with `~/`, so Expand replaces that `~` with the home
	// directory. Nothing else expands: not `~user`, not `--dir=~/x`, and not
	// a quoted or escaped `~`.
	TildeOK bool
}

// CheckError is a rejected Custom command. It names only the rule broken and
// never includes the input (FR-532).
type CheckError struct {
	Rule string
}

func (e *CheckError) Error() string { return "custom command: " + e.Rule }

// The rules Split and Check report.
const (
	RuleEmpty              = "empty"
	RuleEmptyExecutable    = "empty-executable"
	RuleMarkerInExecutable = "marker-in-executable"
	RuleAssignment         = "assignment"
	RuleShellSyntax        = "shell-syntax"
	RuleUnbalancedQuotes   = "unbalanced-quotes"
	RuleTrailingBackslash  = "trailing-backslash"
	RuleControlCharacter   = "control-character"
	RuleNoHome             = "no-home"
)

// Split divides a Custom command line into words by POSIX-style rules with no
// expansion (contracts/editor-launch.md §2, FR-531):
//
//   - spaces and tabs outside quotes separate words; adjacent quoted and
//     unquoted pieces join; a pair of double or single quotes is an empty word;
//   - inside single quotes every character is literal;
//   - inside double quotes a backslash before ", \, $ or a backtick yields that
//     character and any other backslash stays literal;
//   - outside quotes a backslash makes the next character literal.
//
// It rejects, as *CheckError: an unbalanced quote, a trailing unquoted
// backslash, a control character other than tab, and shell syntax nothing
// would interpret (FR-506): an unquoted or double-quoted $ or backtick, and an
// unquoted |, ;, &, < or >.
func Split(text string) ([]Word, error) {
	const (
		unquoted = iota
		single
		double
	)
	var (
		words   []Word
		cur     strings.Builder
		inWord  bool
		tilde   bool // the current word began with an unquoted, unescaped ~
		state   = unquoted
		escaped bool
	)
	finish := func() {
		if !inWord {
			return
		}
		t := cur.String()
		words = append(words, Word{Text: t, TildeOK: tilde && (t == "~" || strings.HasPrefix(t, "~/"))})
		cur.Reset()
		inWord, tilde = false, false
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\t' {
			return nil, &CheckError{Rule: RuleControlCharacter}
		}
		if escaped {
			// After a backslash: outside quotes every character is literal;
			// inside double quotes only ", \, $ and ` are, and any other
			// backslash stays as it was.
			escaped = false
			if state == double && r != '"' && r != '\\' && r != '$' && r != '`' {
				cur.WriteByte('\\')
			}
			cur.WriteRune(r)
			inWord = true
			continue
		}
		switch state {
		case single:
			if r == '\'' {
				state = unquoted
				continue
			}
			cur.WriteRune(r)
		case double:
			switch r {
			case '"':
				state = unquoted
			case '\\':
				escaped = true
			case '$', '`':
				return nil, &CheckError{Rule: RuleShellSyntax}
			default:
				cur.WriteRune(r)
			}
		default: // unquoted
			switch r {
			case ' ', '\t':
				finish()
			case '\'':
				state, inWord = single, true
			case '"':
				state, inWord = double, true
			case '\\':
				escaped, inWord = true, true
			case '$', '`', '|', ';', '&', '<', '>':
				return nil, &CheckError{Rule: RuleShellSyntax}
			default:
				if !inWord && r == '~' {
					tilde = true
				}
				cur.WriteRune(r)
				inWord = true
			}
		}
	}
	if escaped {
		return nil, &CheckError{Rule: RuleTrailingBackslash}
	}
	if state != unquoted {
		return nil, &CheckError{Rule: RuleUnbalancedQuotes}
	}
	finish()
	return words, nil
}
