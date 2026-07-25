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
	}
	return errs
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
