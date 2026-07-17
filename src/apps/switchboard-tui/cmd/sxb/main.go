// Command sxb is the Bubble Tea TUI client. On startup it connects to the
// local daemon over its Unix socket and presents the sandbox manager.
package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/config"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/notify"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/ui"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/vscode"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// resolveWorkspaceSandbox asks the daemon whether the current working directory
// belongs to a sandbox workspace (US4, FR-017/018). It returns the sandbox id
// only when one is found AND running; a stopped/unknown match falls back to the
// TUI with a hint (FR-019/020).
func resolveWorkspaceSandbox(conn *client.Conn) (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := conn.ResolveWorkspace(ctx, cwd)
	if err != nil || !res.GetFound() {
		return "", false
	}
	if res.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		fmt.Fprintf(os.Stderr, "sandbox %s is not running; opening the manager…\n", res.GetSandboxId())
		return "", false
	}
	return res.GetSandboxId(), true
}

// version/commit/date are overridable at build time via -ldflags (set by
// GoReleaser: -X main.version=... -X main.commit=... -X main.date=...).
var (
	version = "0.1.0-dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// When ssh invokes this binary as its SSH_ASKPASS helper, print the password
	// and exit before any other startup (see client.SSHCommand). This is how an
	// SSH password typed into the TUI reaches ssh without a tty prompt.
	if client.RunAskpassIfRequested() {
		return
	}

	// `sxb version` prints build info and exits; `sxb attach` opens a single
	// session full-screen; no-arg launches the TUI (auto-opening a session when
	// run inside a sandbox workspace).
	attachMode := false
	var attach attachArgs
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("sxb %s (commit %s, built %s)\n", version, commit, date)
			return
		case "attach":
			a, err := parseAttachArgs(os.Args[2:])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			attachMode, attach = true, a
		}
	}

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

	// `sxb attach`: open the requested sandbox's session full-screen and exit.
	if attachMode {
		if err := runAttach(context.Background(), conn, attach.sandboxID); err != nil {
			fmt.Fprintln(os.Stderr, "attach:", err)
			os.Exit(1)
		}
		return
	}

	// Auto-open (US4, FR-017): when run inside a sandbox workspace, jump straight
	// into that sandbox's session instead of the general TUI.
	if id, ok := resolveWorkspaceSandbox(conn); ok {
		if err := runAttach(context.Background(), conn, id); err != nil {
			fmt.Fprintln(os.Stderr, "attach:", err)
			os.Exit(1)
		}
		return
	}

	// Source candidates are offered from the current working directory's parent
	// by default; a future launch wizard will let the user pick a root.
	root, _ := os.Getwd()
	m := ui.New(conn, root).WithVersion(version).WithNotifier(notify.New()).WithSbx(cfg.SbxBin).WithTerminal(cfg.Terminal)

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

		// Agent kits (feature 004).
		m = m.WithKits(st.Kits())
	}

	// WithMouseCellMotion enables mouse-wheel events so the in-place terminal
	// view can scroll its own scrollback (and the sandbox list scrolls with the
	// wheel too) instead of the events falling through to the host terminal.
	final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tui error:", err)
		os.Exit(1)
	}

	// A completed self-update swaps the sxb binary on disk; re-exec so the user
	// lands back in the freshly-installed client.
	if fm, ok := final.(ui.Model); ok && fm.ShouldReexec() {
		if exe, err := os.Executable(); err == nil {
			_ = conn.Close()
			if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
				fmt.Fprintln(os.Stderr, "updated sxb; restart it to use the new version:", err)
			}
		}
	}
}
