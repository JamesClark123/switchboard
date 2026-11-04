// Package editor opens a sandbox's controlled workspace folder — the retained
// copy of the seeded files on the daemon's host — in the editor the developer
// chose on the settings screen (SWI-24, FR-501..FR-523). It holds the catalogue
// of built-in editors, the Custom command grammar and checks, the workspace
// path checks, Zed's ssh:// address and a detached launcher. It imports only
// the standard library (research R1): the workspace path arrives as a string,
// and the message texts are assembled by the TUI from the typed results.
package editor

import "strings"

// RemoteKind is how an editor opens a folder on a remote host (FR-513, FR-514).
type RemoteKind int

const (
	// RemoteSSHOption: `<bin> --remote ssh-remote+<target> <path>` (VS Code
	// and its forks).
	RemoteSSHOption RemoteKind = iota
	// RemoteSSHAddress: one `ssh://[user@]host[:port]/<path>` argument (Zed).
	RemoteSSHAddress
	// RemoteNone: the editor can't open remote folders; `v` refuses.
	RemoteNone
	// RemoteCustom: the Custom command's {host} marker.
	RemoteCustom
)

// Editor is one catalogue entry (data-model §1).
type Editor struct {
	// ID is the FR-501 identifier: what settings.toml stores and the footer shows.
	ID string
	// Name is the display name.
	Name string
	// Command is the default command; empty for Custom.
	Command string
	// EnvVar is the override variable; empty for Custom.
	EnvVar string
	// Remote is how the editor opens a remote folder.
	Remote RemoteKind
}

// Default is the editor used when nothing is saved; Custom is the entry that
// runs the developer's own command line.
const (
	Default = "vscode"
	Custom  = "custom"
)

// catalogue holds the FR-501 rows in order.
var catalogue = []Editor{
	{ID: "vscode", Name: "VS Code", Command: "code", EnvVar: "SWITCHBOARD_CODE_BIN", Remote: RemoteSSHOption},
	{ID: "code-insiders", Name: "VS Code Insiders", Command: "code-insiders", EnvVar: "SWITCHBOARD_CODE_INSIDERS_BIN", Remote: RemoteSSHOption},
	{ID: "cursor", Name: "Cursor", Command: "cursor", EnvVar: "SWITCHBOARD_CURSOR_BIN", Remote: RemoteSSHOption},
	{ID: "windsurf", Name: "Windsurf", Command: "windsurf", EnvVar: "SWITCHBOARD_WINDSURF_BIN", Remote: RemoteSSHOption},
	{ID: "zed", Name: "Zed", Command: "zed", EnvVar: "SWITCHBOARD_ZED_BIN", Remote: RemoteSSHAddress},
	{ID: "webstorm", Name: "WebStorm", Command: "webstorm", EnvVar: "SWITCHBOARD_WEBSTORM_BIN", Remote: RemoteNone},
	{ID: "idea", Name: "IntelliJ IDEA", Command: "idea", EnvVar: "SWITCHBOARD_IDEA_BIN", Remote: RemoteNone},
	{ID: "pycharm", Name: "PyCharm", Command: "pycharm", EnvVar: "SWITCHBOARD_PYCHARM_BIN", Remote: RemoteNone},
	{ID: "goland", Name: "GoLand", Command: "goland", EnvVar: "SWITCHBOARD_GOLAND_BIN", Remote: RemoteNone},
	{ID: "sublime", Name: "Sublime Text", Command: "subl", EnvVar: "SWITCHBOARD_SUBLIME_BIN", Remote: RemoteNone},
	{ID: Custom, Name: "Custom", Remote: RemoteCustom},
}

// Catalogue returns the editors in FR-501's order (a copy).
func Catalogue() []Editor {
	out := make([]Editor, len(catalogue))
	copy(out, catalogue)
	return out
}

// Lookup finds an editor by identifier.
func Lookup(id string) (Editor, bool) {
	for _, e := range catalogue {
		if e.ID == id {
			return e, true
		}
	}
	return Editor{}, false
}

// Command is the executable to start for e given the raw override values by
// editor ID (FR-502): a blank value (after trimming) means unset and the
// default command is used; a set value is returned exactly as given, as one
// executable. overridden is true only for a set value that differs from the
// default command, because a value equal to it starts the same program
// (FR-517): the remote path limit turns on only then.
func Command(e Editor, bins map[string]string) (bin string, overridden bool) {
	v := bins[e.ID]
	if strings.TrimSpace(v) == "" {
		return e.Command, false
	}
	return v, v != e.Command
}
