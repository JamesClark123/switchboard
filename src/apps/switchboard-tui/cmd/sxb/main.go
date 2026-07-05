// Command sxb is the Bubble Tea TUI client. On startup it connects to the
// local daemon over its Unix socket and presents the sandbox manager.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/config"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/notify"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/ui"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/vscode"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := client.DialLocal(ctx, cfg.LocalSocket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not connect to local daemon:", err)
		fmt.Fprintln(os.Stderr, "start it with: sxbd serve")
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	// Source candidates are offered from the current working directory's parent
	// by default; a future launch wizard will let the user pick a root.
	root, _ := os.Getwd()
	m := ui.New(conn, root).WithNotifier(notify.New()).WithSbx(cfg.SbxBin).WithTerminal(cfg.Terminal)

	// Attach the client-side stores (source of truth for configs, groups, and
	// known hosts — FR-002c). A failure here is non-fatal; the dependent features
	// are simply unavailable.
	if st, err := store.New(cfg.ConfigDir); err != nil {
		fmt.Fprintln(os.Stderr, "warning: client store unavailable:", err)
	} else {
		m = m.WithConfigs(st.Configs())

		// Multi-host manager seeded with the local daemon already connected.
		hostStore := st.Hosts()
		mgr := client.NewManager()
		activeHost := "local"
		if local, err := hostStore.EnsureLocal(cfg.LocalSocket); err == nil {
			activeHost = local.ID
			mgr.Upsert(client.HostEntry{ID: local.ID, DisplayName: local.DisplayName, Kind: "local", SocketPath: local.SocketPath})
			mgr.Adopt(local.ID, conn) // reuse the already-dialed local connection
		}
		m = m.WithHosts(mgr, hostStore, activeHost)

		// Groups + VS Code opener (US5).
		m = m.WithGroups(st.Groups(), vscode.NewOpener(cfg.CodeBin))
	}

	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui error:", err)
		os.Exit(1)
	}
}
