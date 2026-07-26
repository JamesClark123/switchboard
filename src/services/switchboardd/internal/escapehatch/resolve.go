package escapehatch

import (
	"fmt"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// CommandsFromRefs extracts each kit's escape-hatch command list from a slice of
// KitRefs, in attach order. Only client-authored kits (KitRef.spec) can declare
// escape-hatch commands; external `source` kits are opaque to switchboard and
// contribute none.
func CommandsFromRefs(refs []*pb.KitRef) [][]*pb.EscapeHatchCommand {
	lists := make([][]*pb.EscapeHatchCommand, 0, len(refs))
	for _, ref := range refs {
		if spec := ref.GetSpec(); spec != nil {
			lists = append(lists, spec.GetEscapeHatch())
		}
	}
	return lists
}

// Resolve merges the escape-hatch command lists of a sandbox's attached kits, in
// attach order, into the single set the daemon persists and enforces (FR-036).
//
// On a name collision the later list wins (clarification Q1: later-attached kits
// override a same-named command). First-appearance order is preserved so the
// injected rule and wrapper stay stable across re-attaches — an overriding command
// keeps the earlier command's position but takes the later definition's value.
//
// Every surviving command is validated: a blank name or command, or an unspecified
// consent mode, is a hard error (the author must choose auto-run vs requires-approval).
func Resolve(lists ...[]*pb.EscapeHatchCommand) ([]*pb.EscapeHatchCommand, error) {
	order := make([]string, 0)
	byName := make(map[string]*pb.EscapeHatchCommand)
	for _, list := range lists {
		for _, cmd := range list {
			if cmd == nil {
				continue
			}
			name := cmd.GetName()
			if _, seen := byName[name]; !seen {
				order = append(order, name)
			}
			byName[name] = cmd // later wins
		}
	}

	out := make([]*pb.EscapeHatchCommand, 0, len(order))
	for _, name := range order {
		cmd := byName[name]
		if err := validate(cmd); err != nil {
			return nil, err
		}
		out = append(out, cmd)
	}
	return out, nil
}

// validate enforces the daemon-side invariants a resolved command must satisfy.
// Richer, developer-facing checks (kebab-case, when-to-use present, working-dir
// containment) live client-side in the editor; these are the minimum the daemon
// refuses to attach.
func validate(cmd *pb.EscapeHatchCommand) error {
	if cmd.GetName() == "" {
		return fmt.Errorf("escape-hatch command has an empty name")
	}
	if cmd.GetCommand() == "" {
		return fmt.Errorf("escape-hatch command %q has an empty command string", cmd.GetName())
	}
	if cmd.GetConsentMode() == pb.ConsentMode_CONSENT_MODE_UNSPECIFIED {
		return fmt.Errorf("escape-hatch command %q must set a consent mode (auto-run or requires-approval)", cmd.GetName())
	}
	// At most one argument-matching form: a subcommand list OR a regex, not both.
	if len(nonEmpty(cmd.GetSubcommands())) > 0 && strings.TrimSpace(cmd.GetArgsPattern()) != "" {
		return fmt.Errorf("escape-hatch command %q sets both subcommands and args_pattern (choose one)", cmd.GetName())
	}
	// A declared regex must compile, or nothing the agent sends could ever match.
	if pat := strings.TrimSpace(cmd.GetArgsPattern()); pat != "" {
		if _, err := compileAnchored(pat); err != nil {
			return fmt.Errorf("escape-hatch command %q has an invalid args_pattern: %w", cmd.GetName(), err)
		}
	}
	// Every allowed workspace entry must be workspace-relative and non-escaping.
	for _, ws := range nonEmpty(cmd.GetWorkspaces()) {
		if !workspaceRelOK(ws) {
			return fmt.Errorf("escape-hatch command %q has a workspace %q that is absolute or escapes the workspace", cmd.GetName(), ws)
		}
	}
	return nil
}
