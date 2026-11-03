package editor

// Refusal is the typed result of a check that stops `v` (data-model §4). It
// names the rule broken and never carries any part of a Custom command
// (FR-532); the TUI maps the rule to its message text.
type Refusal struct {
	// Rule is one of the rule names in contracts/editor-launch.md §4:
	// host-lookup, no-remote, empty, empty-executable, marker-in-executable,
	// assignment, shell-syntax, unbalanced-quotes, trailing-backslash,
	// control-character, no-home, target-dash, zed-target, path-unsafe,
	// path-limited.
	Rule string
	// Detail is safe detail only: the rejected character as 'x' or U+XXXX
	// (path-limited), or the host lookup error's text (host-lookup).
	Detail string
	// Where is where to fix it: the override variable's name for a built-in
	// editor's path-limited refusal, "settings screen" for the Custom
	// command's rules, "host entry" for the SSH target, "daemon" for the path.
	Where string
}

// The Where values.
const (
	WhereSettings  = "settings screen"
	WhereHostEntry = "host entry"
	WhereDaemon    = "daemon"
	WhereHostsFile = "hosts file"
	WhereEnv       = "environment"
)
