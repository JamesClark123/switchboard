package ui

import (
	"context"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// refreshTimeout bounds a re-seed. Duplicating a multi-GB workspace is
// minutes-long work, so this is deliberately far above ctxTimeout's 60s.
const refreshTimeout = 30 * time.Minute

// confirmRefresh gates the destructive re-seed behind the yes/no dialog (feature
// 004, FR-030). It names the sandbox and the exact repositories that will be
// re-copied, because "refresh" alone reads as harmless — the operation deletes
// everything in the workspace, including uncommitted agent work.
func (m Model) confirmRefresh(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	if len(sb.GetSources()) == 0 {
		m.status = "cannot refresh: no sources recorded for this sandbox"
		return m, nil
	}
	body := []string{
		"Re-copy the repositories for " + selectedStyle.Render(sb.GetDisplayName()) + " from source,",
		"then restart it.",
		"",
		dimStyle.Render("Repositories:"),
	}
	for _, src := range sb.GetSources() {
		body = append(body, "  · "+filepath.Base(src.GetPath())+dimStyle.Render("  ("+src.GetPath()+")"))
	}
	body = append(body,
		"",
		"Everything else in the sandbox workspace is deleted,",
		"including uncommitted changes.",
		"",
		dimStyle.Render("Installed packages and agent history are kept."),
	)
	return m.enterConfirm(confirmState{
		title:     "Refresh sandbox?",
		body:      body,
		verb:      "refresh",
		sandboxID: sb.GetId(),
		onConfirm: m.refreshCmdFor(m.daemonForHost(host), sb.GetId()),
	})
}

// refreshCmdFor re-seeds one sandbox. Named to avoid colliding with refreshCmd,
// which reloads the sandbox list.
//
// The daemon is captured as a parameter rather than read off m inside the closure:
// Model is copied on every Update, so a closure over m would act on a stale copy.
func (m Model) refreshCmdFor(d Daemon, id string) tea.Cmd {
	return func() tea.Msg {
		// A refresh re-copies the sources from scratch, which for a large repo can
		// take far longer than a normal RPC — hence its own generous timeout rather
		// than the shared 60s one.
		ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()
		sb, err := d.Refresh(ctx, id, nil)
		if err != nil {
			return errMsg{err}
		}
		return statusMsg("refreshed " + sb.GetDisplayName())
	}
}
