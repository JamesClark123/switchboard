package portforward

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// pgidWait bounds how long the launcher waits for the in-sandbox wrapper to
// announce its process-group id. The marker is the wrapper's very first write, so
// this only ever expires when the image has no `setsid` or `sbx exec` allocated a
// TTY and merged the streams.
const pgidWait = 5 * time.Second

// sandboxWrapper builds the shell program run inside the sandbox.
//
//	cd '<dir>' && if command -v setsid …; then exec setsid /bin/sh -c '<inner>'
//	                                     else exec /bin/sh -c '<inner>'; fi
//	inner: echo "swb-pgid:$$" >&2; exec <command>
//
// The wrapper exists to make the service killable from OUTSIDE the sandbox
// (research R3). `setsid` makes the shell a session leader, so its PID equals its
// process-group id; announcing that id is what later lets `kill -TERM -<pgid>`
// reach the command AND everything it spawned — which matters because the realistic
// declared command is a wrapper like `pnpm dev` whose child is the real listener.
//
// The `command -v` fallback covers images without util-linux. Without setsid the
// announced pid is not a group leader, so stopping degrades to signalling the
// single process; that is strictly better than refusing to start.
func sandboxWrapper(relDir, command string) string {
	// $$ must reach the inner shell unexpanded — it is that shell's own pid, which
	// is the whole point — so the marker is assembled there, not interpolated here.
	inner := fmt.Sprintf(`echo "%s$$" >&2; exec %s`, pgidMarker, command)
	quoted := shellQuote(inner)
	launch := fmt.Sprintf("if command -v setsid >/dev/null 2>&1; then exec setsid /bin/sh -c %s; else exec /bin/sh -c %s; fi", quoted, quoted)
	if relDir == "" {
		return launch
	}
	return fmt.Sprintf("cd %s && %s", shellQuote(relDir), launch)
}

// launchInSandbox starts a service's command inside the sandbox via `sbx exec`,
// captures its output, and waits briefly for the wrapper to announce its PGID.
// The working directory is applied RELATIVE to wherever `sbx exec` starts, which
// is assumed to be the sandbox's workspace — the daemon only knows the workspace's
// HOST path, and inventing a container path would be a guess. Containment of relDir
// was already enforced at authoring and at attach.
func (s *Supervisor) launchInSandbox(ctx context.Context, ref, relDir, command string) (*runningProc, error) {
	script := sandboxWrapper(relDir, command)
	cmd := s.runner.Exec(ctx, ref, []string{"/bin/sh", "-c", script})

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	proc := &runningProc{cmd: cmd, out: newTailBuffer(maxCapturedOutput), exited: make(chan struct{})}
	pgidCh := make(chan int, 1)

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go streamInto(&wg, proc.out, stdout)
	go streamStderrWithMarker(&wg, proc.out, stderr, pgidCh)
	proc.reapAfter(&wg)

	select {
	case pgid, ok := <-pgidCh:
		if ok {
			proc.pgid = pgid
		}
	case <-time.After(pgidWait):
		// No marker: stopping degrades to killing the `sbx exec` child, which is
		// still bounded, just less thorough. Not worth failing a start over.
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return proc, nil
}
