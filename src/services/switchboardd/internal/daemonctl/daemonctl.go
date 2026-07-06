// Package daemonctl holds the process-control primitives behind the daemon's
// lifecycle subcommands: the PID file that `serve` maintains, the liveness and
// socket-reachability probes `status` reports, and the systemd user-unit that
// `serve --boot` installs so the daemon is restarted on every boot.
//
// Everything here is OS-effecting but self-contained and unit-tested; the thin
// glue that shells out to `systemctl`/re-execs the daemon lives in the (test-
// excluded) cmd entrypoint.
package daemonctl

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// UnitName is the systemd user unit installed by `serve --boot` on Linux.
const UnitName = "switchboard.service"

// LaunchdLabel is the macOS launchd LaunchAgent label installed by
// `serve --boot`; the plist is written as "<LaunchdLabel>.plist".
const LaunchdLabel = "com.switchboard.sxbd"

// WritePID atomically records pid at path, creating the parent directory.
func WritePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadPID reads and parses the pid recorded at path.
func ReadPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("malformed pid file %s: %w", path, err)
	}
	return pid, nil
}

// Clear removes the pid file, ignoring a missing file.
func Clear(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Alive reports whether a process with pid exists and is signalable by this
// user (signal 0 performs error checking without delivering a signal).
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but is owned by another user.
	return err == syscall.EPERM
}

// Running returns the recorded pid and whether that process is alive. A stale
// pid file (recorded process gone) is removed as a side effect so a fresh
// `serve` is not blocked by a leftover file.
func Running(pidPath string) (int, bool) {
	pid, err := ReadPID(pidPath)
	if err != nil {
		return 0, false
	}
	if Alive(pid) {
		return pid, true
	}
	_ = Clear(pidPath)
	return pid, false
}

// SocketReachable reports whether something is accepting connections on the
// Unix socket within timeout — a cheap "is the daemon actually listening" probe.
func SocketReachable(socket string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// UnitOptions parameterizes the rendered boot service (systemd unit or launchd
// plist).
type UnitOptions struct {
	ExecStart   string   // absolute path to the daemon binary
	Args        []string // subcommand + flags (defaults to ["serve"])
	Environment []string // "KEY=value" lines carried into the service
	LogPath     string   // stdout/stderr log file (launchd only; systemd uses journald)
}

// RenderUnit produces a systemd user unit that runs the daemon on boot and
// restarts it whenever it exits — so it "always runs unless explicitly stopped".
func RenderUnit(o UnitOptions) string {
	args := o.Args
	if len(args) == 0 {
		args = []string{"serve"}
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Switchboard sandbox daemon\n")
	b.WriteString("After=network-online.target docker.service\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s %s\n", o.ExecStart, strings.Join(args, " "))
	for _, e := range o.Environment {
		fmt.Fprintf(&b, "Environment=%s\n", e)
	}
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=2\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// UserUnitPath returns the on-disk path of the installed user unit, honoring
// $XDG_CONFIG_HOME and falling back to ~/.config.
func UserUnitPath(getenv func(string) string) (string, error) {
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("cannot locate systemd user dir: neither XDG_CONFIG_HOME nor HOME is set")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", UnitName), nil
}

// UnitInstalled reports whether the systemd user unit file exists on disk.
func UnitInstalled(getenv func(string) string) bool {
	path, err := UserUnitPath(getenv)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// LaunchAgentPath returns the on-disk path of the macOS launchd LaunchAgent
// plist, under ~/Library/LaunchAgents (the per-user agent directory launchd
// loads at login).
func LaunchAgentPath(getenv func(string) string) (string, error) {
	home := getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("cannot locate LaunchAgents dir: HOME is not set")
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist"), nil
}

// RenderLaunchAgent produces a macOS launchd LaunchAgent plist equivalent to the
// systemd user unit: it starts the daemon when the agent loads at login
// (RunAtLoad) and restarts it whenever it exits (KeepAlive) — so it "always runs
// unless explicitly stopped". launchd has no per-user journald, so stdout/stderr
// are directed to LogPath when set.
func RenderLaunchAgent(o UnitOptions) string {
	args := o.Args
	if len(args) == 0 {
		args = []string{"serve"}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n", plistEscape(LaunchdLabel))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	fmt.Fprintf(&b, "    <string>%s</string>\n", plistEscape(o.ExecStart))
	for _, a := range args {
		fmt.Fprintf(&b, "    <string>%s</string>\n", plistEscape(a))
	}
	b.WriteString("  </array>\n")
	if len(o.Environment) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, e := range o.Environment {
			k, v, _ := strings.Cut(e, "=")
			fmt.Fprintf(&b, "    <key>%s</key>\n    <string>%s</string>\n", plistEscape(k), plistEscape(v))
		}
		b.WriteString("  </dict>\n")
	}
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	b.WriteString("  <key>KeepAlive</key>\n  <true/>\n")
	if o.LogPath != "" {
		fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n", plistEscape(o.LogPath))
		fmt.Fprintf(&b, "  <key>StandardErrorPath</key>\n  <string>%s</string>\n", plistEscape(o.LogPath))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// plistEscape escapes the XML metacharacters that may appear in a path or env
// value embedded in a plist <string>.
func plistEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// BootUnitPath returns where the boot integration for goos writes its unit file:
// the systemd user unit on Linux, the launchd LaunchAgent plist on macOS.
func BootUnitPath(goos string, getenv func(string) string) (string, error) {
	switch goos {
	case "darwin":
		return LaunchAgentPath(getenv)
	case "linux":
		return UserUnitPath(getenv)
	default:
		return "", fmt.Errorf("no boot integration for %s", goos)
	}
}

// BootInstalled reports whether the boot service for goos is installed on disk
// (systemd unit on Linux, launchd agent on macOS).
func BootInstalled(goos string, getenv func(string) string) bool {
	p, err := BootUnitPath(goos, getenv)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// BootBackendName is a human-readable name for the boot integration on goos,
// used in `sxbd status` output. Empty when the OS has no supported integration.
func BootBackendName(goos string) string {
	switch goos {
	case "darwin":
		return "launchd agent"
	case "linux":
		return "systemd user service"
	default:
		return ""
	}
}

// CurrentEnv collects the SWITCHBOARDD_-prefixed variables from environ (each a
// "KEY=value" entry, as from os.Environ) so an installed unit reproduces the
// configuration the daemon was installed with.
func CurrentEnv(environ []string) []string {
	var out []string
	for _, e := range environ {
		if strings.HasPrefix(e, "SWITCHBOARDD_") {
			out = append(out, e)
		}
	}
	return out
}
