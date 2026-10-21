package worker

import (
	"path/filepath"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/managedblock"
)

// The briefing is a managed block in the workspace CLAUDE.md (research R12):
// written at attach and launch-attach, rewritten on UpdateWorker{briefing},
// removed at detach; user content outside the markers is preserved. The
// orchestrator renders the body (it knows the action list); the worker side
// only writes what it is given.
const (
	blockBegin      = "<!-- switchboard:orchestrator:begin -->"
	blockEnd        = "<!-- switchboard:orchestrator:end -->"
	claudeMdRelPath = "CLAUDE.md"
)

// WriteBriefing writes (or replaces) the managed block in <workspace>/CLAUDE.md.
func WriteBriefing(workspacePath, body string) error {
	if workspacePath == "" {
		return nil
	}
	return managedblock.Write(filepath.Join(workspacePath, claudeMdRelPath), blockBegin, blockEnd, body)
}

// StripBriefing removes the managed block, leaving user content.
func StripBriefing(workspacePath string) error {
	if workspacePath == "" {
		return nil
	}
	return managedblock.Strip(filepath.Join(workspacePath, claudeMdRelPath), blockBegin, blockEnd)
}
