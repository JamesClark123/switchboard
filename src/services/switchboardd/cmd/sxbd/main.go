// Command sxbd is the per-host Switchboard daemon. It manages docker
// sandbox lifecycle and serves the gRPC contract over a Unix socket.
//
//	sxbd serve                 # listen on the Unix socket (foreground)
//	sxbd serve --watch         # …but detach and run in the background (-w)
//	sxbd serve --boot          # install a systemd user service (restart on boot)
//	sxbd serve --debug         # …also log every RPC action and error to stderr
//	sxbd status                # report whether the daemon is running
//	sxbd stop                  # stop the running daemon
//	sxbd dial-stdio            # bridge stdin/stdout <-> the local socket (SSH remoting)
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/config"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/daemonctl"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxkit"
)

// version is overridable at build time via -ldflags.
var version = "0.1.0-dev"

const usage = "usage: sxbd <serve [--watch|-w] [--boot] [--debug] | status | stop | dial-stdio>"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		debug := fs.Bool("debug", false, "log every RPC action and error to stderr")
		var watch bool
		fs.BoolVar(&watch, "watch", false, "detach and run the daemon in the background")
		fs.BoolVar(&watch, "w", false, "detach and run the daemon in the background (shorthand)")
		boot := fs.Bool("boot", false, "install a systemd user service that restarts the daemon on every boot")
		_ = fs.Parse(os.Args[2:])

		switch {
		case *boot:
			err = installBootService(cfg, *debug)
		case watch:
			err = startBackground(cfg, *debug)
		default:
			err = runServe(cfg, *debug)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve error:", err)
			os.Exit(1)
		}
	case "status":
		os.Exit(runStatus(cfg))
	case "stop":
		if err := runStop(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "stop error:", err)
			os.Exit(1)
		}
	case "dial-stdio":
		if err := dialStdio(cfg.Socket); err != nil {
			fmt.Fprintln(os.Stderr, "dial-stdio error:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func runServe(cfg *config.Config, debug bool) error {
	// Refuse to start a second daemon on top of a live one — they would fight
	// over the socket. A stale pid file (dead process) is cleared by Running.
	if pid, ok := daemonctl.Running(cfg.PidFile); ok {
		return fmt.Errorf("daemon already running (pid %d); use `sxbd stop` first", pid)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
		return err
	}

	if err := daemonctl.WritePID(cfg.PidFile, os.Getpid()); err != nil {
		return err
	}
	defer func() { _ = daemonctl.Clear(cfg.PidFile) }()

	reg, err := registry.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = reg.Close() }()

	runner := &sandbox.SbxRunner{Bin: cfg.SbxBin}
	mgr := sandbox.NewManager(reg, runner, cfg.WorkspaceRoot, cfg.HostID)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Re-adopt still-running sandboxes after a daemon restart (FR-002a, SC-012).
	if err := mgr.Readopt(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "warning: re-adoption error:", err)
	}

	// Introspect the host's sbx option surface (FR-014). Non-fatal: if sbx is
	// unavailable the manifest is empty and option validation/editing degrades
	// gracefully rather than blocking the daemon.
	manifest, err := (&sbxkit.Builder{Bin: cfg.SbxBin}).Build(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not introspect sbx options:", err)
		manifest = nil
	}

	// US4: event hub, agent PTY registry, and the hook callback server.
	hub := agent.NewHub(cfg.HostID)
	agents := agent.NewRegistry(agent.PTYFactory(cfg.SbxBin))
	hookServer := agent.NewHookServer(hub, mgr)

	// Sandboxes reach the daemon's hook endpoint via host.docker.internal:<port>.
	_, port, _ := net.SplitHostPort(cfg.HookAddr)
	callbackURL := fmt.Sprintf("http://host.docker.internal:%s/hook", port)
	mgr.SetHookInjector(func(sandboxID, workspacePath string) error {
		return agent.InjectHooks(workspacePath, sandboxID, callbackURL)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/hook", hookServer.Handle)
	hookHTTP := &http.Server{Addr: cfg.HookAddr, Handler: mux}
	go func() {
		if err := hookHTTP.ListenAndServe(); err != nil && !strings.Contains(err.Error(), "Server closed") {
			fmt.Fprintln(os.Stderr, "warning: hook server stopped:", err)
		}
	}()
	go func() { <-ctx.Done(); _ = hookHTTP.Close() }()

	srv := sbgrpc.NewServer(sbgrpc.Config{
		Manager:       mgr,
		HostID:        cfg.HostID,
		DaemonVersion: version,
		WorkspaceRoot: cfg.WorkspaceRoot,
		Manifest:      manifest,
		Hub:           hub,
		Agents:        agents,
		Debug:         debug,
	})
	fmt.Fprintf(os.Stderr, "sxbd %s serving on %s (workspace %s, hooks %s)\n", version, cfg.Socket, cfg.WorkspaceRoot, cfg.HookAddr)
	if debug {
		fmt.Fprintln(os.Stderr, "sxbd debug logging enabled: every RPC action and error will be logged")
	}
	return srv.Serve(ctx, cfg.Socket)
}

// startBackground re-execs `sxbd serve` detached from the controlling terminal
// (new session, output redirected to a log file) so `serve --watch` returns to
// the shell while the daemon keeps running.
func startBackground(cfg *config.Config, debug bool) error {
	if pid, ok := daemonctl.Running(cfg.PidFile); ok {
		return fmt.Errorf("daemon already running (pid %d)", pid)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(cfg.DataDir, "switchboard.log")
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = logF.Close() }()

	args := []string{"serve"}
	if debug {
		args = append(args, "--debug")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach: new session, no controlling tty
	if err := cmd.Start(); err != nil {
		return err
	}
	fmt.Printf("sxbd running in the background (pid %d)\nlogs: %s\nstop with: sxbd stop\n", cmd.Process.Pid, logPath)
	return nil
}

// installBootService writes and enables a systemd user unit so the daemon is
// (re)started on every boot and restarted whenever it exits — it "always runs
// unless explicitly stopped" (via `sxbd stop`).
func installBootService(cfg *config.Config, debug bool) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("--boot requires systemd (systemctl not found on PATH); on this OS start the daemon another way (e.g. `sxbd serve --watch`)")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	unitPath, err := daemonctl.UserUnitPath(os.Getenv)
	if err != nil {
		return err
	}
	args := []string{"serve"}
	if debug {
		args = append(args, "--debug")
	}
	unit := daemonctl.RenderUnit(daemonctl.UnitOptions{
		ExecStart:   exe,
		Args:        args,
		Environment: daemonctl.CurrentEnv(os.Environ()),
	})
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}

	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if err := run("systemctl", "--user", "enable", "--now", daemonctl.UnitName); err != nil {
		return fmt.Errorf("systemctl enable --now: %w", err)
	}
	// Best-effort: linger lets the user service start at boot without an
	// interactive login. It may require privileges; failure is non-fatal.
	if user := os.Getenv("USER"); user != "" {
		_ = run("loginctl", "enable-linger", user)
	}

	fmt.Printf("installed %s\n", unitPath)
	fmt.Println("sxbd will now start on every boot and restart if it exits.")
	fmt.Println("check with: sxbd status   ·   stop with: sxbd stop")
	return nil
}

// runStatus prints whether the daemon is running and whether boot-autostart is
// enabled. It exits non-zero (3, matching `systemctl status`) when stopped.
func runStatus(cfg *config.Config) int {
	pid, running := daemonctl.Running(cfg.PidFile)
	reachable := daemonctl.SocketReachable(cfg.Socket, 500*time.Millisecond)
	boot := daemonctl.UnitInstalled(os.Getenv)

	switch {
	case running:
		fmt.Printf("● sxbd is running (pid %d)\n", pid)
	case reachable:
		fmt.Println("● sxbd is running (socket reachable; no pid file)")
	default:
		fmt.Println("○ sxbd is not running")
	}
	fmt.Printf("  socket:  %s (%s)\n", cfg.Socket, yn(reachable, "reachable", "unreachable"))
	fmt.Printf("  on boot: %s\n", yn(boot, "enabled (systemd user service)", "disabled"))

	if running || reachable {
		return 0
	}
	return 3
}

// runStop stops the running daemon: the systemd service if one is installed
// (a clean stop, which Restart=always does not undo), otherwise SIGTERM to the
// process recorded in the pid file.
func runStop(cfg *config.Config) error {
	if daemonctl.UnitInstalled(os.Getenv) {
		if _, err := exec.LookPath("systemctl"); err == nil {
			fmt.Println("stopping systemd service…")
			return run("systemctl", "--user", "stop", daemonctl.UnitName)
		}
	}

	pid, running := daemonctl.Running(cfg.PidFile)
	if !running {
		fmt.Println("sxbd is not running")
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signalling pid %d: %w", pid, err)
	}
	// Wait up to ~5s for a graceful exit.
	for i := 0; i < 50; i++ {
		if !daemonctl.Alive(pid) {
			_ = daemonctl.Clear(cfg.PidFile)
			fmt.Printf("stopped sxbd (pid %d)\n", pid)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("sxbd (pid %d) did not exit within 5s", pid)
}

// run executes a command, forwarding its stderr for diagnostics.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func yn(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// dialStdio bridges stdio to the local daemon socket so a client can reach this
// daemon via `ssh <host> sxbd dial-stdio` (the Docker-CLI pattern, R1).
func dialStdio(socket string) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	errc := make(chan error, 2)
	go func() { _, e := io.Copy(conn, os.Stdin); errc <- e }()
	go func() { _, e := io.Copy(os.Stdout, conn); errc <- e }()
	return <-errc
}
