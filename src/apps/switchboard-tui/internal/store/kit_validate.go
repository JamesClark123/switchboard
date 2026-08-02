package store

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// kebabRe matches a kebab-case identifier (lowercase alphanumerics, single hyphens
// between segments). Escape-hatch command names must satisfy it (Rule IV) because
// the name is the wrapper argument and the allowlist key.
var kebabRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateEscapeHatch checks the kit's escape-hatch commands against the authoring
// rules (data-model §6), returning a human-readable error per violation. This is
// the client-side gate; the daemon re-checks the minimum invariants on attach, and
// re-validates working-dir containment at execution time.
func (kit *Kit) ValidateEscapeHatch() []string {
	var errs []string
	seen := make(map[string]bool, len(kit.EscapeHatch))
	for i, c := range kit.EscapeHatch {
		label := fmt.Sprintf("escape-hatch command #%d", i+1)
		if c.Name != "" {
			label = fmt.Sprintf("escape-hatch command %q", c.Name)
		}
		if strings.TrimSpace(c.Name) == "" {
			errs = append(errs, label+": name is required")
		} else if !kebabRe.MatchString(c.Name) {
			errs = append(errs, label+": name must be kebab-case (lowercase letters, digits, hyphens)")
		} else if seen[c.Name] {
			errs = append(errs, label+": duplicate name within this kit")
		}
		seen[c.Name] = true

		if strings.TrimSpace(c.Command) == "" {
			errs = append(errs, label+": command is required")
		}
		if strings.TrimSpace(c.WhenToUse) == "" {
			errs = append(errs, label+": when-to-use is required")
		}
		if c.WorkingDir != "" && !workingDirWithinWorkspace(c.WorkingDir) {
			errs = append(errs, label+": working dir must be relative and stay within the workspace")
		}

		// Argument matching: at most one of subcommands / args-pattern, and a pattern
		// must compile. A loose pattern is allowed (with a warning surfaced in the
		// editor) but an invalid one is a hard error — nothing could ever match it.
		hasSubs := len(nonBlank(c.Subcommands)) > 0
		hasPattern := strings.TrimSpace(c.ArgsPattern) != ""
		if hasSubs && hasPattern {
			errs = append(errs, label+": set either subcommands or an args pattern, not both")
		}
		if hasPattern {
			if _, err := regexp.Compile(`^(?:` + c.ArgsPattern + `)$`); err != nil {
				errs = append(errs, label+": args pattern is not a valid regular expression")
			}
		}

		// Targetable workspaces: each entry (literal path or glob) must be relative
		// and stay within the workspace.
		for _, ws := range nonBlank(c.Workspaces) {
			if !workingDirWithinWorkspace(ws) {
				errs = append(errs, label+": workspace "+ws+" must be relative and stay within the workspace")
			}
		}
	}
	return errs
}

// ValidateServices checks the kit's declared services against the authoring rules
// (feature 006, FR-043), returning a human-readable error per violation. Each
// message names the offending field, because "the kit is invalid" is useless to
// someone with six services on screen.
//
// This is the client-side gate; portforward.Resolve re-checks the minimum
// invariants daemon-side at attach, so a hand-edited services.yaml cannot bypass
// it, and the executor re-validates working-dir containment at start time.
func (kit *Kit) ValidateServices() []string {
	var errs []string
	seen := make(map[string]bool, len(kit.Services))
	for i, s := range kit.Services {
		label := fmt.Sprintf("service #%d", i+1)
		if s.Name != "" {
			label = fmt.Sprintf("service %q", s.Name)
		}
		if strings.TrimSpace(s.Name) == "" {
			errs = append(errs, label+": name is required")
		} else if !kebabRe.MatchString(s.Name) {
			errs = append(errs, label+": name must be kebab-case (lowercase letters, digits, hyphens)")
		} else if seen[s.Name] {
			errs = append(errs, label+": duplicate name within this kit")
		}
		seen[s.Name] = true

		if strings.TrimSpace(s.Command) == "" {
			errs = append(errs, label+": command is required")
		}
		if s.ListenPort == 0 || s.ListenPort > 65535 {
			errs = append(errs, label+": listen port must be between 1 and 65535")
		}
		if s.WorkingDir != "" && !workingDirWithinWorkspace(s.WorkingDir) {
			errs = append(errs, label+": working dir must be relative and stay within the workspace")
		}
		// {{port}} only means something for an on-host service: an in-sandbox service
		// has its own network namespace, so its declared port can never collide and
		// substituting a different one would just break the publish mapping.
		if !s.OnHost && strings.Contains(s.Command, "{{port}}") {
			errs = append(errs, label+": {{port}} only applies to an on-host service (an in-sandbox port cannot collide)")
		}
	}
	return errs
}

// nonBlank drops empty/whitespace-only entries from a string slice.
func nonBlank(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// workingDirWithinWorkspace reports whether a workspace-relative path stays inside
// the workspace after cleaning (no absolute path, no `..` escape).
func workingDirWithinWorkspace(dir string) bool {
	if filepath.IsAbs(dir) {
		return false
	}
	cleaned := filepath.Clean(dir)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
