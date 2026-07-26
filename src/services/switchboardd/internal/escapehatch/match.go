package escapehatch

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// matchArgs validates the agent-supplied argument string against the command's
// declared constraints and returns the tokens to pass as positional parameters
// (never re-parsed as shell syntax — see executor.go). The two authoring forms are
// mutually exclusive:
//
//   - subcommands: the args must equal one of the allowed entries exactly; the
//     TRUSTED entry is what gets tokenized and run (friendly form).
//   - args_pattern: the args must fully match the anchored regex; the agent's own
//     string is tokenized (power form — a loose pattern broadens which arguments
//     reach the fixed command, hence the authoring-time safety warning).
//
// With neither declared, the command takes no arguments and any supplied args are
// rejected. A rejection is the agent's fault (ErrInvalidArgs -> HTTP 400) so the
// message is returned for the agent to learn from.
func matchArgs(cmd *pb.EscapeHatchCommand, args string) ([]string, error) {
	args = strings.TrimSpace(args)
	subs := nonEmpty(cmd.GetSubcommands())
	pat := strings.TrimSpace(cmd.GetArgsPattern())

	switch {
	case len(subs) > 0:
		for _, s := range subs {
			if strings.TrimSpace(s) == args {
				return strings.Fields(s), nil // tokenize the trusted entry
			}
		}
		return nil, fmt.Errorf("%w: argument %q is not one of the allowed subcommands %v", ErrInvalidArgs, args, subs)

	case pat != "":
		re, err := compileAnchored(pat)
		if err != nil {
			// The pattern is rejected at attach (validate); reaching here means a
			// misconfigured command, not the agent's mistake.
			return nil, fmt.Errorf("%w: command's argument pattern is invalid: %v", ErrInvalidArgs, err)
		}
		if !re.MatchString(args) {
			return nil, fmt.Errorf("%w: argument %q does not match the allowed pattern %q", ErrInvalidArgs, args, pat)
		}
		return strings.Fields(args), nil // may be empty when the pattern permits it

	default:
		if args != "" {
			return nil, fmt.Errorf("%w: this command takes no arguments", ErrInvalidArgs)
		}
		return nil, nil
	}
}

// matchWorkspace validates the agent-supplied workspace selector against the
// command's allowed set and returns the workspace-relative directory to run in.
//
//   - When the command declares no workspaces, a selector is rejected and the
//     command's fixed working_dir is used (original behavior).
//   - Otherwise the selector must be relative, non-escaping, and match one of the
//     allowed entries (a literal path or a glob like "src/apps/*").
//
// Daemon-side containment is re-checked again at execution (executor.resolveWorkdir),
// defense-in-depth. A rejection is ErrInvalidWorkspace (HTTP 400).
func matchWorkspace(cmd *pb.EscapeHatchCommand, sel string) (string, error) {
	sel = strings.TrimSpace(sel)
	allowed := nonEmpty(cmd.GetWorkspaces())

	if len(allowed) == 0 {
		if sel != "" {
			return "", fmt.Errorf("%w: this command does not accept a --workspace", ErrInvalidWorkspace)
		}
		return cmd.GetWorkingDir(), nil
	}
	if sel == "" {
		return "", fmt.Errorf("%w: this command requires --workspace, one of %v", ErrInvalidWorkspace, allowed)
	}
	if !workspaceRelOK(sel) {
		return "", fmt.Errorf("%w: workspace %q must be relative and stay inside the workspace", ErrInvalidWorkspace, sel)
	}
	cleaned := filepath.Clean(sel)
	for _, entry := range allowed {
		if matchWorkspaceEntry(entry, cleaned) {
			return cleaned, nil
		}
	}
	return "", fmt.Errorf("%w: workspace %q is not allowed for this command (allowed: %v)", ErrInvalidWorkspace, sel, allowed)
}

// matchWorkspaceEntry reports whether a cleaned, workspace-relative selector matches
// an allowed entry, which may be a literal path or a glob. Globs use filepath.Match
// semantics (`*` does not cross a path separator).
func matchWorkspaceEntry(entry, sel string) bool {
	entry = filepath.Clean(strings.TrimSpace(entry))
	if entry == sel {
		return true
	}
	if ok, err := filepath.Match(entry, sel); err == nil && ok {
		return true
	}
	return false
}

// compileAnchored compiles pat as a full-string match (wrapping it so it must match
// the entire argument string regardless of whether the author anchored it).
func compileAnchored(pat string) (*regexp.Regexp, error) {
	return regexp.Compile(`^(?:` + pat + `)$`)
}

// workspaceRelOK reports whether a workspace-relative path is safe: not absolute and
// not escaping the workspace root via `..`.
func workspaceRelOK(dir string) bool {
	if filepath.IsAbs(dir) {
		return false
	}
	cleaned := filepath.Clean(dir)
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

// nonEmpty drops blank entries from a string slice.
func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
