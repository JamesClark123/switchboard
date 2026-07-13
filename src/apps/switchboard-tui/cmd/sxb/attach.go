package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"golang.org/x/term"
)

// detachByte is Ctrl-\ (FS): the in-band "detach and leave the session running"
// key for `sxb attach`, mirroring dtach. It is never forwarded to the PTY.
const detachByte = 0x1c

// attachArgs holds the parsed `sxb attach` flags.
type attachArgs struct {
	sandboxID string
	host      string
}

// parseAttachArgs parses `attach --sandbox <id> [--host <h>]` (also positional
// sandbox id). Returns an error describing usage on a missing sandbox id.
func parseAttachArgs(argv []string) (attachArgs, error) {
	var a attachArgs
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--sandbox", "-s":
			if i+1 < len(argv) {
				i++
				a.sandboxID = argv[i]
			}
		case "--host", "-H":
			if i+1 < len(argv) {
				i++
				a.host = argv[i]
			}
		default:
			if a.sandboxID == "" && argv[i] != "" && argv[i][0] != '-' {
				a.sandboxID = argv[i]
			}
		}
	}
	if a.sandboxID == "" {
		return a, errors.New("usage: sxb attach --sandbox <id> [--host <host>]")
	}
	return a, nil
}

// runAttach opens a full-screen attachment to a sandbox's persistent session on
// the connected daemon (US3/US4, feature 003). Snapshot + live output stream to
// the real terminal (which does the VT interpretation); stdin is forwarded raw.
// Ctrl-\ detaches, leaving the session running (FR-002). Returns when the session
// ends or the user detaches.
func runAttach(ctx context.Context, conn *client.Conn, sandboxID string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("sxb attach requires an interactive terminal")
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
		cols, rows = w, h
	}

	stream, err := conn.AttachAgent(ctx, client.AttachOptions{
		SandboxID: sandboxID,
		Kind:      client.AttachExternal,
		Cols:      uint32(cols),
		Rows:      uint32(rows),
		Label:     "sxb-attach",
		Sink:      os.Stdout,
	})
	if err != nil {
		if errors.Is(err, client.ErrExternalAlreadyOpen) {
			return errors.New("this sandbox is already open in another external terminal")
		}
		return err
	}
	defer func() { _ = stream.Close() }()

	// Live terminal resize -> daemon (reconciled to smallest-of-attached, R3).
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
				_ = stream.SendResize(uint32(w), uint32(h))
			}
		}
	}()

	// stdin -> PTY, intercepting the detach key.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				if idx := bytes.IndexByte(buf[:n], detachByte); idx >= 0 {
					if idx > 0 {
						_ = stream.SendData(buf[:idx])
					}
					_ = stream.Close()
					return
				}
				if err := stream.SendData(buf[:n]); err != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	return stream.Wait()
}
