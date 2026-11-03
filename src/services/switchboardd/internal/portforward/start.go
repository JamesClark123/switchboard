package portforward

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Start starts one declared service by NAME and returns its instance as soon as it
// exists (STARTING). Readiness is reported asynchronously via the event stream.
//
// The name is resolved against the sandbox's PERSISTED service set — the allowlist
// boundary. Nothing outside a kit's declaration can be started, and no caller ever
// supplies a command.
//
// Starting a service that is already STARTING or RUNNING is idempotent: the
// existing instance and its existing local address come back unchanged, with no
// second process (FR-048).
func (s *Supervisor) Start(sandboxID, name string) (*pb.ServiceInstance, error) {
	sb, err := s.sandboxes.Get(sandboxID)
	if err != nil || sb == nil {
		return nil, ErrUnknownSandbox
	}
	declared, ok := Lookup(sb, name)
	if !ok {
		return nil, ErrServiceNotDeclared
	}

	inst, created := s.insts.Create(sandboxID, name)
	if !created {
		return inst, nil
	}
	s.publish(inst)

	// An in-sandbox service has nowhere to run when the sandbox is down. Refuse
	// before allocating anything: FR-046 requires that no port be taken.
	if declared.GetLocation() == pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX &&
		sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		return s.markFailed(inst.GetId(),
			pb.ServiceFailureReason_SERVICE_FAILURE_REASON_SANDBOX_NOT_RUNNING,
			"the sandbox must be running before an in-sandbox service can start"), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.handles[inst.GetId()] = &procHandle{
		cancel: cancel,
		inSbx:  declared.GetLocation() == pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX,
		ref:    sb.GetContainerRef(),
	}
	s.mu.Unlock()

	go s.bringUp(ctx, sb, declared, inst.GetId())
	return inst, nil
}

// bringUp runs one start attempt to a terminal outcome on its own goroutine:
// resolve the effective port, launch, publish (in-sandbox), wait for the address to
// actually work, then either report RUNNING or classify the failure.
func (s *Supervisor) bringUp(ctx context.Context, sb *pb.Sandbox, declared *pb.KitService, instID string) {
	inSandbox := declared.GetLocation() == pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX

	effPort, command, err := s.effectivePort(declared)
	if err != nil {
		s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NO_LOCAL_PORT,
			"could not allocate a port on the host: "+err.Error())
		return
	}

	// An on-host service that binds a fixed port can collide with another sandbox's
	// copy. Probing first turns "you are silently talking to someone else's process"
	// into a reported failure (US3-4).
	if !inSandbox && !portFree(effPort) {
		s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_PORT_IN_USE,
			fmt.Sprintf("port %d is already in use on this host; use %s in the command to let switchboard pick a free port", effPort, PortPlaceholder))
		return
	}

	proc, cur := s.launchExclusive(ctx, sb, declared, command, instID, inSandbox, effPort)
	if proc == nil {
		return
	}
	sb = cur

	window := time.Duration(declared.GetReadinessTimeoutSeconds()) * time.Second
	if window <= 0 {
		window = s.readinessTimeout
	}

	hostPort := effPort
	if inSandbox {
		// Wait for the service to be listening on all interfaces INSIDE the sandbox
		// before publishing. Publishing first and dialling the published port would
		// prove nothing: the publish binds that host port itself and accepts
		// connections whether or not anything is behind it (research R5).
		if _, err := s.awaitSandboxListening(ctx, sb.GetId(), sb.GetContainerRef(), effPort, window, proc.exited); err != nil {
			s.readinessFailed(ctx, sb, declared, instID, proc, err, inSandbox, effPort)
			return
		}
		// `sbx ports --publish` may start a stopped sandbox like `sbx exec`: it
		// runs under the runtime lock too, held until the handle records the
		// published port, so a stop's drain (which needs the lock) always finds
		// it to unpublish (SWI-26). Each attempt is bounded, so a wedged publish
		// never holds a waiting stop for long.
		unlock, ok := s.awaitRuntime(ctx, sb.GetId())
		if !ok {
			return // cancelled while waiting: whoever cancelled it owns the outcome
		}
		hostPort, err = s.publishSandboxPort(ctx, sb.GetContainerRef(), effPort)
		if err != nil {
			unlock()
			if ctx.Err() == nil {
				s.stopProc(ctx, proc, sb.GetId(), sb.GetContainerRef(), inSandbox)
			}
			if ctx.Err() != nil {
				return // cancelled: whoever cancelled it owns the outcome
			}
			s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED,
				"could not publish the sandbox port: "+err.Error())
			return
		}
		s.mu.Lock()
		h, live := s.handles[instID]
		if live {
			h.hostPort = hostPort
		}
		s.mu.Unlock()
		unlock()
		if !live {
			// A developer's stop released the handle during the publish, before
			// there was a port to give back: it falls to this attempt.
			s.unpublish(sb.GetContainerRef(), hostPort, effPort)
			return
		}
	}

	s.transitionStarting(instID, func(i *pb.ServiceInstance) {
		i.EffectivePort = effPort
		i.HostEndpointPort = hostPort
		i.StartedAt = timestamppb.New(s.now())
	})

	// Final confirmation that the endpoint the relay will dial actually answers.
	// For an on-host service this IS the readiness test; for an in-sandbox one it
	// verifies the publish landed.
	if err := awaitReady(ctx, hostPort, window, proc.exited); err != nil {
		s.readinessFailed(ctx, sb, declared, instID, proc, err, inSandbox, effPort)
		return
	}

	// Once cancelled (a stop's drain, a developer's stop) it defers to whoever
	// cancelled it: a readiness that succeeded as it was cancelled never
	// overwrites their outcome.
	if ctx.Err() != nil || !s.transitionStarting(instID, func(i *pb.ServiceInstance) {
		i.State = pb.ServiceState_SERVICE_STATE_RUNNING
		i.Output, i.OutputTruncated = proc.output()
	}) {
		return
	}

	// From here the only thing that ends the service is a stop or its own death.
	go s.watchExit(ctx, instID, proc)
}

// launchExclusive runs the launch and records its process on the handle, excluding
// a developer's Stop of this instance throughout (procHandle.launch). An in-sandbox
// launch is a developer request that starts a new `sbx exec` client: refused while
// a lifecycle operation holds the sandbox's runtime lock, held from the launch until
// the handle has its process (SWI-17, research R14), and refused if the sandbox
// left RUNNING since Start read it (SWI-26). No port is kept (FR-046). It returns
// the process and the record the launch ran against, or nil with the outcome
// settled.
func (s *Supervisor) launchExclusive(ctx context.Context, sb *pb.Sandbox, declared *pb.KitService, command, instID string, inSandbox bool, effPort uint32) (*runningProc, *pb.Sandbox) {
	h, ok := s.handleFor(instID)
	if !ok {
		return nil, nil // stopped before it got here
	}
	h.launch.Lock()
	defer h.launch.Unlock()
	if ctx.Err() != nil {
		return nil, nil // a stop got here first: it owns the outcome
	}
	if inSandbox {
		unlock, err := s.lockRuntime(sb.GetId())
		if err != nil {
			s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED, err.Error())
			return nil, nil
		}
		defer unlock()
		cur, err := s.sandboxes.Get(sb.GetId())
		if err != nil || cur == nil || cur.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
			s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_SANDBOX_NOT_RUNNING,
				"the sandbox must be running before an in-sandbox service can start")
			return nil, nil
		}
		sb = cur
		// A stop signals and unpublishes on the ref the launch ran against.
		s.mu.Lock()
		h.ref = cur.GetContainerRef()
		s.mu.Unlock()
	}
	proc, err := s.launch(ctx, sb, declared, command, inSandbox, effPort)
	if err != nil {
		s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED,
			"failed to launch command: "+err.Error())
		return nil, nil
	}
	s.setProc(instID, proc)
	s.mu.Lock()
	h.pgid = proc.pgid
	h.sbxPort = effPort
	s.mu.Unlock()
	return proc, sb
}

// launch dispatches to the right launcher for the service's execution location.
func (s *Supervisor) launch(ctx context.Context, sb *pb.Sandbox, declared *pb.KitService, command string, inSandbox bool, effPort uint32) (*runningProc, error) {
	if inSandbox {
		return s.launchInSandbox(ctx, sb.GetContainerRef(), declared.GetWorkingDir(), command)
	}
	return s.launchOnHost(ctx, sb.GetWorkspacePath(), declared.GetWorkingDir(), command, effPort)
}

// readinessFailed classifies why a service never became reachable and records it
// with a reason the developer can act on (FR-051). Once the attempt is
// cancelled it signals nothing (whoever cancelled it owns the processes), and
// every write applies only while the instance is still STARTING.
func (s *Supervisor) readinessFailed(ctx context.Context, sb *pb.Sandbox, declared *pb.KitService, instID string, proc *runningProc, cause error, inSandbox bool, effPort uint32) {
	out, truncated := proc.output()
	s.insts.Update(instID, func(i *pb.ServiceInstance) {
		if i.GetState() == pb.ServiceState_SERVICE_STATE_STARTING {
			i.Output, i.OutputTruncated = out, truncated
		}
	})
	if ctx.Err() == nil {
		s.stopProc(ctx, proc, sb.GetId(), sb.GetContainerRef(), inSandbox)
	}

	// Checked on ctx, not cause: the stopProc above may have been waiting for the
	// runtime lock when the attempt was cancelled, which says nothing about why
	// readiness failed.
	switch {
	case ctx.Err() != nil:
		s.transitionStarting(instID, func(i *pb.ServiceInstance) {
			i.State = pb.ServiceState_SERVICE_STATE_STOPPED
			i.EndedAt = timestamppb.New(s.now())
		})
		s.release(instID)
	case errors.Is(cause, errProcessExited):
		s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_EARLY,
			exitDetail(proc))
	default:
		// The window elapsed with the process still alive. Inside a sandbox that is
		// most often a loopback-only bind — diagnosable, and with a concrete fix —
		// so look before reporting a bare "never listened" (research R5).
		state := listenNone
		if inSandbox {
			state = s.finalListenState(ctx, sb.GetId(), sb.GetContainerRef(), effPort)
		}
		if ctx.Err() != nil {
			return // cancelled during the diagnosis: whoever cancelled it owns the outcome
		}
		if state == listenLoopbackOnly {
			s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING_LOOPBACK,
				fmt.Sprintf("%q is listening on 127.0.0.1:%d inside the sandbox, which is unreachable from outside it — bind all interfaces instead (e.g. --host 0.0.0.0)",
					declared.GetName(), effPort))
			return
		}
		s.failStarting(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING,
			fmt.Sprintf("nothing was listening on port %d within the readiness window", effPort))
	}
}

// watchExit turns an unexpected death of a RUNNING service into a FAILED instance
// with its output retained and its port released (FR-047, US4-3).
func (s *Supervisor) watchExit(ctx context.Context, instID string, proc *runningProc) {
	select {
	case <-proc.exited:
	case <-ctx.Done():
		return // a deliberate stop; Stop owns the transition
	}
	if s.isStopping(instID) {
		return // a stop's own signal; Stop owns the transition
	}
	if inst, ok := s.insts.Get(instID); !ok || inst.GetState() != pb.ServiceState_SERVICE_STATE_RUNNING {
		return // already terminal
	}
	out, truncated := proc.output()
	s.insts.Update(instID, func(i *pb.ServiceInstance) {
		i.Output, i.OutputTruncated = out, truncated
	})
	s.markFailed(instID, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_UNEXPECTEDLY, exitDetail(proc))
}

// exitDetail renders why a process ended, preferring its own output over a bare
// exit code — "exit status 1" alone tells a developer nothing.
func exitDetail(proc *runningProc) string {
	detail := "the process exited"
	if err := proc.err(); err != nil {
		detail = err.Error()
	}
	if out, _ := proc.output(); out != "" {
		detail += ": " + lastLine(out)
	}
	return detail
}

// lastLine returns the final non-empty line of captured output — for a service that
// just died, that is the interesting one.
func lastLine(s string) string {
	end := len(s)
	for end > 0 && (s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	start := end
	for start > 0 && s[start-1] != '\n' {
		start--
	}
	return s[start:end]
}
