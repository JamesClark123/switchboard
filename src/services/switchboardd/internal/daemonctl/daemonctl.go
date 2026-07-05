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

// UnitName is the systemd user unit installed by `serve --boot`.
const UnitName = "switchboard.service"

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

// UnitOptions parameterizes the rendered systemd user unit.
type UnitOptions struct {
	ExecStart   string   // absolute path to the daemon binary
	Args        []string // subcommand + flags (defaults to ["serve"])
	Environment []string // "KEY=value" lines carried into the service
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
