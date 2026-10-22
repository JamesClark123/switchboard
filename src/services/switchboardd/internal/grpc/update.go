package grpc

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"syscall"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	upd "github.com/jamesclark123/switchboard/libs/switchboard-update"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/daemonctl"
)

// The self-update steps are indirected through package vars so the RPC handler
// is unit-testable without touching the network, replacing the real binary, or
// actually re-execing the test process.
var (
	// selfUpdateFetch resolves the target release and returns the verified sxbd
	// binary bytes for this platform (empty target = latest).
	selfUpdateFetch = func(ctx context.Context, targetVersion string) (newVersion string, binary []byte, err error) {
		return upd.FetchBinary(ctx, targetVersion, runtime.GOOS, runtime.GOARCH, "sxbd")
	}
	selfUpdateApply    = upd.ApplyToPath
	selfUpdateExecPath = upd.SelfPath
	selfUpdateIsBrew   = upd.IsBrewManaged
	selfUpdateRestart  = defaultRestart

	// execSelf and restartDelay are indirected so defaultRestart's bookkeeping
	// (clear pid, build argv) is testable without the process actually re-execing.
	execSelf     = syscall.Exec
	restartDelay = 250 * time.Millisecond
)

// UpdateHooks overrides the self-update steps; used by tests to exercise the
// UpdateDaemon handler without network access, binary replacement, or re-exec.
type UpdateHooks struct {
	Fetch    func(ctx context.Context, targetVersion string) (newVersion string, binary []byte, err error)
	Apply    func(binary []byte, execPath string) error
	ExecPath func() (string, error)
	IsBrew   func(execPath string) bool
	Restart  func(pidFile, execPath string) error
}

// SetUpdateHooks installs h and returns a function that restores the defaults.
func SetUpdateHooks(h UpdateHooks) func() {
	prevF, prevA, prevE, prevB, prevR := selfUpdateFetch, selfUpdateApply, selfUpdateExecPath, selfUpdateIsBrew, selfUpdateRestart
	if h.Fetch != nil {
		selfUpdateFetch = h.Fetch
	}
	if h.Apply != nil {
		selfUpdateApply = h.Apply
	}
	if h.ExecPath != nil {
		selfUpdateExecPath = h.ExecPath
	}
	if h.IsBrew != nil {
		selfUpdateIsBrew = h.IsBrew
	}
	if h.Restart != nil {
		selfUpdateRestart = h.Restart
	}
	return func() {
		selfUpdateFetch, selfUpdateApply, selfUpdateExecPath, selfUpdateIsBrew, selfUpdateRestart = prevF, prevA, prevE, prevB, prevR
	}
}

// UpdateDaemon self-updates this daemon's binary to a target release and
// restarts on the new binary (FR: frequent-update distribution). Progress is
// streamed; nothing is swapped unless the download verifies against the
// release's SHA-256 checksums.
func (s *Server) UpdateDaemon(req *pb.UpdateDaemonRequest, stream pb.Switchboard_UpdateDaemonServer) error {
	send := func(stage, msg string) {
		_ = stream.Send(&pb.UpdateProgress{Stage: stage, Message: msg})
	}
	fail := func(format string, args ...any) error {
		return stream.Send(&pb.UpdateProgress{Stage: "error", Error: fmt.Sprintf(format, args...)})
	}

	// The installed path is resolved once, here, before anything is swapped:
	// on Linux /proc/self/exe follows the rename ApplyToPath performs, so a
	// path looked up afterwards names the (deleted) ".sxbd.old" backup — a
	// re-exec of it fails, the old daemon lives on, and the next update tries
	// to back up the backup. The same resolved path is applied to and re-exec'd.
	exe, err := selfUpdateExecPath()
	if err != nil {
		return fail("locate executable: %v", err)
	}
	exe = upd.InstalledPath(exe)
	// Never rewrite a Homebrew-managed binary underneath brew.
	if selfUpdateIsBrew(exe) {
		return fail("daemon on %s is Homebrew-managed; update it with `brew upgrade switchboard` there", s.hostID)
	}

	send("checking", "resolving release "+targetLabel(req.GetTargetVersion()))
	newVersion, binary, err := selfUpdateFetch(stream.Context(), req.GetTargetVersion())
	if err != nil {
		return fail("%v", err)
	}
	if newVersion == s.daemonVersion {
		return stream.Send(&pb.UpdateProgress{Stage: "done", Done: true, NewVersion: newVersion, Message: "already up to date"})
	}

	send("downloading", "fetched "+newVersion)
	send("verifying", "checksum ok")
	send("applying", "installing "+newVersion)
	if err := selfUpdateApply(binary, exe); err != nil {
		return fail("apply update: %v", err)
	}

	// Emit the terminal messages before restarting — the connection drops the
	// moment we re-exec onto the new binary.
	_ = stream.Send(&pb.UpdateProgress{Stage: "restarting", Message: "restarting on " + newVersion})
	_ = stream.Send(&pb.UpdateProgress{Stage: "done", Done: true, NewVersion: newVersion})

	// Restart after the handler returns so the client receives the stream. A
	// failed re-exec leaves this (old) daemon running: say so in the log,
	// since the client was already told the update is done.
	go func() {
		if err := selfUpdateRestart(s.pidFile, exe); err != nil {
			log.Printf("[update] installed %s at %s but could not restart on it: %v — restart the daemon by hand", newVersion, exe, err)
		}
	}()
	return nil
}

func targetLabel(v string) string {
	if v == "" {
		return "latest"
	}
	return v
}

// defaultRestart re-execs the daemon in place onto the freshly-installed binary
// at exe (the path resolved before the swap — never os.Executable here, see
// UpdateDaemon). It clears the pid file first (the re-exec keeps the same PID,
// which would otherwise trip serve's already-running guard); the listen socket
// fd is close-on-exec, and serve unlinks any stale socket on startup.
func defaultRestart(pidFile, exe string) error {
	if pidFile != "" {
		_ = daemonctl.Clear(pidFile)
	}
	// Give the just-sent stream a moment to flush to the client before the
	// abrupt image replacement.
	time.Sleep(restartDelay)
	argv := append([]string{exe}, os.Args[1:]...)
	return execSelf(exe, argv, os.Environ())
}
