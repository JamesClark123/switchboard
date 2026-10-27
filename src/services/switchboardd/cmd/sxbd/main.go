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
	"runtime"
	"strings"
	"syscall"
	"time"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/config"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/daemonctl"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/escapehatch"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/orchestrator"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/portforward"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxkit"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/secret"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
)

// version/commit/date are overridable at build time via -ldflags (set by
// GoReleaser: -X main.version=... -X main.commit=... -X main.date=...).
var (
	version = "0.1.0-dev"
	commit  = "none"
	date    = "unknown"
)

const usage = "usage: sxbd <serve [--watch|-w] [--boot] [--debug] | status | stop | version | dial-stdio>"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	// `version` must work even when config is broken, so handle it before Load.
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("sxbd %s (commit %s, built %s)\n", version, commit, date)
		return
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
	// A daemon started at login can come up before the sbx runtime does; an
	// unreadable runtime changes nothing (SWI-13), so it is retried a few times
	// in the background once the server is wired (below).
	readoptErr := mgr.Readopt(ctx)
	if readoptErr != nil {
		fmt.Fprintln(os.Stderr, "warning: re-adoption error:", readoptErr)
	}
	// Collect any `.switchboard/staging` debris an add interrupted by a daemon
	// crash left behind (feature 007, research R2). Best-effort, never fatal.
	if err := mgr.PurgeStaging(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: staging purge:", err)
	}

	// Introspect the host's sbx option surface (FR-014). Non-fatal: if sbx is
	// unavailable the manifest is empty and option validation/editing degrades
	// gracefully rather than blocking the daemon.
	manifest, err := (&sbxkit.Builder{Bin: cfg.SbxBin}).Build(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not introspect sbx options:", err)
		manifest = nil
	}

	// Feature 008: probe the host's runtime baseline and MCP gateway availability
	// once (FR-096/FR-079). Advertised in DaemonInfo; never fatal — kit-less
	// sandbox operation keeps working on a host that fails the probe.
	mcpAvail := mcp.Probe(ctx, cfg.SbxBin)
	if !mcpAvail.Available {
		fmt.Fprintln(os.Stderr, "note: MCP gateway unavailable on this host:", mcpAvail.Reason)
	}

	// US4: event hub, agent PTY registry, and the hook callback server.
	hub := agent.NewHub(cfg.HostID)
	// The PTY factory targets sbx by the sandbox's container_ref (the --name sbx
	// knows it by), resolved from the uuid registry key; sbx cannot address a
	// sandbox by uuid, so exec'ing with the uuid would EOF the terminal on attach.
	// It also carries the sandbox's workspace path so the agent opens in the code
	// directory (equivalent to `sbx run <agent>` there), not the container root.
	sbxTarget := func(sandboxID string) agent.Target {
		sb, err := mgr.Get(sandboxID)
		if err != nil {
			return agent.Target{}
		}
		ref := sb.GetContainerRef()
		if ref == "" {
			ref = sb.GetDisplayName()
		}
		return agent.Target{Ref: ref, Workdir: sb.GetWorkspacePath()}
	}
	agents := agent.NewRegistry(agent.PTYFactory(cfg.SbxBin, sbxTarget))
	// Feature 009 (research R7): an agent that exits on its own is reported as
	// AGENT_STATUS_EXITED so the orchestrator can fail the run it was on.
	agents.SetStatusUpdater(mgr)
	agents.SetLogger(func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	hookServer := agent.NewHookServer(hub, mgr)

	// Sandboxes reach the daemon's hook endpoint via host.docker.internal:<port>.
	_, port, _ := net.SplitHostPort(cfg.HookAddr)

	// Both the agent hooks (003) and the escape hatch (005) POST from inside a
	// sandbox to host.docker.internal:<port>, which sbx's default deny-by-default
	// network policy blocks. Allow the callback port up front so those callbacks
	// are reachable without the user hand-running `sbx policy allow`. Non-fatal:
	// sbx may be unavailable in dev, and a pre-existing allow makes this a no-op.
	if err := runner.AllowNetwork(ctx, "localhost:"+port); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not allow the hook callback port in the sbx network policy:", err)
	}

	callbackURL := fmt.Sprintf("http://host.docker.internal:%s/hook", port)
	mgr.SetHookInjector(func(sandboxID, workspacePath string) error {
		return agent.InjectHooks(workspacePath, sandboxID, callbackURL)
	})
	// Sandboxes launched by an older daemon carry its hook command; rewrite them
	// so this daemon's mapping (e.g. Claude Code's idle nudge is IDLE, not
	// NEEDS_INPUT) applies once their agent restarts.
	mgr.RefreshHooks(func(msg string) { fmt.Fprintln(os.Stderr, msg) })

	// Escape hatch (feature 005): the service runs allowlisted commands on the host
	// and delivers results back to the agent via the PTY-injection Prompt path. The
	// wrapper POSTs to /escape-hatch/run (host.docker.internal), a sibling of /hook.
	ehCallbackURL := fmt.Sprintf("http://host.docker.internal:%s/escape-hatch/run", port)
	// Feature 009 (research R7): a result pushed into a detached agent first
	// ensures its persistent terminal session exists, so the PTY is drained and a
	// long unattended turn never stalls. The registry is the gRPC server's;
	// it is resolved at call time.
	var srv *sbgrpc.Server
	terms := func() *terminal.Registry {
		if srv == nil {
			return nil
		}
		return srv.Terminals()
	}
	ehService := escapehatch.New(mgr, hub, worker.EnsureDrained(terms, agents.Prompt))
	mgr.SetEscapeHatchInjector(func(sandboxID, workspacePath string, commands []*pb.EscapeHatchCommand) error {
		return escapehatch.Inject(workspacePath, sandboxID, ehCallbackURL, commands)
	})

	// Port forwarding (feature 006): the supervisor owns service lifecycle on this
	// host. It reaches sandboxes through the same `sbx` runner the manager uses, and
	// publishes state changes on the hub the client is already subscribed to.
	services := portforward.NewSupervisor(mgr, runner, hub)

	mux := http.NewServeMux()
	mux.HandleFunc("/hook", hookServer.Handle)
	mux.HandleFunc("/escape-hatch/run", ehService.HandleRun)
	hookHTTP := &http.Server{Addr: cfg.HookAddr, Handler: mux}
	go func() {
		if err := hookHTTP.ListenAndServe(); err != nil && !strings.Contains(err.Error(), "Server closed") {
			fmt.Fprintln(os.Stderr, "warning: hook server stopped:", err)
		}
	}()
	go func() { <-ctx.Done(); _ = hookHTTP.Close() }()

	// Feature 008: the gateway manager drives `sbx mcp` on this host and owns the
	// attach-by-default marks; its change events ride the same hub.
	mcpManager := mcp.NewManager(&mcp.CLI{Bin: cfg.SbxBin}, reg, cfg.HostID, hub.Publish)
	// The secret manager drives `sbx secret` on this host (FR-104); its change
	// events ride the same hub.
	secretManager := secret.NewManager(&secret.CLI{Bin: cfg.SbxBin}, cfg.HostID, hub.Publish)

	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

	// Feature 009: the worker side every daemon runs for the sandboxes it owns
	// (research R1/R3/R4): the controller record, the per-worker gateway
	// registration + token, the link relay, guarded delivery and the briefing.
	workerSvc := worker.NewService(worker.Deps{
		Manager: mgr, Registry: reg, Agents: agents, Hub: hub,
		CLI: &mcp.CLI{Bin: cfg.SbxBin}, HookPort: port, HostID: cfg.HostID, Version: version, Logf: logf,
	})
	mgr.SetOnDestroy(workerSvc.OnDestroy)
	mgr.SetOnBringUp(workerSvc.OnBringUp)
	mgr.SetControllerApplier(workerSvc.Apply)
	// The per-worker MCP endpoint (research R5) rides the hook HTTP server at
	// /orchestrator/w/<token>/mcp. The gateway reaches it on 127.0.0.1:<port>
	// from the host, so the `localhost:<port>` policy allow above already
	// covers it; ServeMux registration is safe while the server is serving.
	mux.Handle(worker.EndpointPrefix, workerSvc.Handler())

	// The orchestrator side this daemon hosts (research R1). It dials worker
	// machines over SSH exactly as the TUI does (dial.Batch: BatchMode, keys or
	// agent only — FR-106), reaches its own host in-process, and runs one
	// runtime per record.
	orch := orchestrator.NewService(orchestrator.Deps{
		Store:         reg,
		Hub:           hub,
		HostID:        cfg.HostID,
		Dial:          dial.Batch,
		Local:         worker.Local(workerSvc, hub),
		Sandboxes:     mgr,
		DaemonVersion: version,
		Logf:          logf,
		// The reasoning sandbox is created through the gRPC server's launch
		// path (kits, MCP, controller) — resolved at call time, as the server
		// is built below; Create only runs once it serves.
		Launch: func(ctx context.Context, req orchestrator.ReasoningLaunch, onLog func(string)) (*pb.Sandbox, error) {
			if srv == nil {
				return nil, fmt.Errorf("daemon still starting")
			}
			return srv.LaunchReasoning(ctx, req, onLog)
		},
	})
	if err := orch.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "warning: orchestrators not started:", err)
	}
	defer orch.Stop()

	srv = sbgrpc.NewServer(sbgrpc.Config{
		Manager:       mgr,
		HostID:        cfg.HostID,
		DaemonVersion: version,
		// The parsed sbx version when the probe could read one ("0.47.0"); the
		// manifest's raw `sbx version` line otherwise.
		SbxVersion:      mcpAvail.SbxVersion,
		WorkspaceRoot:   cfg.WorkspaceRoot,
		KitRoot:         cfg.KitRoot,
		PidFile:         cfg.PidFile,
		Manifest:        manifest,
		Hub:             hub,
		Agents:          agents,
		EscapeHatch:     ehService,
		Services:        services,
		McpAvailability: mcpAvail,
		Mcp:             mcpManager,
		Secrets:         secretManager,
		Worker:          workerSvc,
		Orchestrator:    orch,
		Debug:           debug,
	})
	workerSvc.SetTerminals(srv.Terminals())
	if readoptErr != nil {
		// Started only now: the manager's change and bring-up observers are set,
		// so what the retry moves reaches clients (and is never a data race).
		go retryReconcile(ctx, mgr, readoptRetries)
	}
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

// installBootService installs a boot-autostart integration for the current OS so
// the daemon is (re)started automatically and restarted whenever it exits — it
// "always runs unless explicitly stopped" (via `sxbd stop`). Linux uses a systemd
// user unit; macOS uses a launchd LaunchAgent.
func installBootService(cfg *config.Config, debug bool) error {
	switch runtime.GOOS {
	case "linux":
		return installSystemdService(cfg, debug)
	case "darwin":
		return installLaunchdService(cfg, debug)
	default:
		return fmt.Errorf("--boot is not supported on %s; start the daemon another way (e.g. `sxbd serve --watch`)", runtime.GOOS)
	}
}

// installSystemdService writes and enables a systemd user unit (Linux).
func installSystemdService(cfg *config.Config, debug bool) error {
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

// installLaunchdService writes and loads a launchd LaunchAgent (macOS). A
// per-user LaunchAgent starts when the user logs in and is kept alive; a
// boot-without-login daemon would require a root-owned LaunchDaemon, which is out
// of scope for this per-user tool.
func installLaunchdService(cfg *config.Config, debug bool) error {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return fmt.Errorf("--boot requires launchd (launchctl not found on PATH); start the daemon another way (e.g. `sxbd serve --watch`)")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	plistPath, err := daemonctl.LaunchAgentPath(os.Getenv)
	if err != nil {
		return err
	}
	args := []string{"serve"}
	if debug {
		args = append(args, "--debug")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(cfg.DataDir, "switchboard.log")
	plist := daemonctl.RenderLaunchAgent(daemonctl.UnitOptions{
		ExecStart:   exe,
		Args:        args,
		Environment: daemonctl.CurrentEnv(os.Environ()),
		LogPath:     logPath,
	})
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}

	// Reload cleanly: unload a prior instance if present (ignore "not loaded"),
	// then load with -w so launchd persists it across logins.
	_ = run("launchctl", "unload", plistPath)
	if err := run("launchctl", "load", "-w", plistPath); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}

	fmt.Printf("installed %s\n", plistPath)
	fmt.Println("sxbd will now start when you log in and restart if it exits.")
	fmt.Println("check with: sxbd status   ·   stop with: sxbd stop")
	return nil
}

// runStatus prints whether the daemon is running and whether boot-autostart is
// enabled. It exits non-zero (3, matching `systemctl status`) when stopped.
func runStatus(cfg *config.Config) int {
	pid, running := daemonctl.Running(cfg.PidFile)
	reachable := daemonctl.SocketReachable(cfg.Socket, 500*time.Millisecond)
	boot := daemonctl.BootInstalled(runtime.GOOS, os.Getenv)

	switch {
	case running:
		fmt.Printf("● sxbd is running (pid %d)\n", pid)
	case reachable:
		fmt.Println("● sxbd is running (socket reachable; no pid file)")
	default:
		fmt.Println("○ sxbd is not running")
	}
	backend := daemonctl.BootBackendName(runtime.GOOS)
	if backend == "" {
		backend = "boot service"
	}
	fmt.Printf("  socket:  %s (%s)\n", cfg.Socket, yn(reachable, "reachable", "unreachable"))
	fmt.Printf("  on boot: %s\n", yn(boot, "enabled ("+backend+")", "disabled"))

	if running || reachable {
		return 0
	}
	return 3
}

// runStop stops the running daemon: the systemd service if one is installed
// (a clean stop, which Restart=always does not undo), otherwise SIGTERM to the
// process recorded in the pid file.
func runStop(cfg *config.Config) error {
	// A boot-managed daemon restarts itself (systemd Restart=always / launchd
	// KeepAlive), so a plain SIGTERM would just respawn — stop it through the
	// init system instead.
	if daemonctl.BootInstalled(runtime.GOOS, os.Getenv) {
		switch runtime.GOOS {
		case "linux":
			if _, err := exec.LookPath("systemctl"); err == nil {
				fmt.Println("stopping systemd service…")
				return run("systemctl", "--user", "stop", daemonctl.UnitName)
			}
		case "darwin":
			if plistPath, err := daemonctl.LaunchAgentPath(os.Getenv); err == nil {
				if _, lerr := exec.LookPath("launchctl"); lerr == nil {
					fmt.Println("stopping launchd service…")
					return run("launchctl", "unload", plistPath)
				}
			}
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

// readoptRetries are the delays (from daemon start) of the background
// reconciles after a startup re-adoption could not read the runtime (SWI-13).
// A few, not a poll loop: every ListSandboxes reconciles as well.
var readoptRetries = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

// retryReconcile reconciles at each delay until one reads the runtime, the
// daemon stops, or the delays run out.
func retryReconcile(ctx context.Context, mgr *sandbox.Manager, delays []time.Duration) {
	start := time.Now()
	for _, d := range delays {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(start.Add(d))):
		}
		if _, err := mgr.Reconcile(ctx); err == nil {
			return
		}
	}
}
