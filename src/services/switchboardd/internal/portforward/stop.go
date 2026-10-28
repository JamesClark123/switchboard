package portforward

import (
	"context"
	"fmt"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// portFreeWaitStep is how often the stop path re-checks that the listen port has
// actually been given up.
const portFreeWaitStep = 100 * time.Millisecond

// Stop terminates a running service and releases everything it held.
//
// The sequence is the one clarification Q4 settled on (research R6):
//
//  1. signal the whole process GROUP to shut down gracefully (SIGTERM),
//  2. wait a bounded grace period,
//  3. force-kill whatever is still alive (SIGKILL),
//  4. release the local port only once the listen port is observed free.
//
// Step 1 is what makes the guarantee true for the realistic command — `pnpm dev`,
// whose child is the actual listener. Step 4 is what makes "released only once
// nothing holds it" true rather than aspirational.
//
// Stopping an already-stopped service is idempotent.
func (s *Supervisor) Stop(sandboxID, name string) (*pb.ServiceInstance, error) {
	sb, err := s.sandboxes.Get(sandboxID)
	if err != nil || sb == nil {
		return nil, ErrUnknownSandbox
	}
	if _, ok := Lookup(sb, name); !ok {
		return nil, ErrServiceNotDeclared
	}
	inst, ok := s.insts.GetByService(sandboxID, name)
	if !ok {
		return &pb.ServiceInstance{SandboxId: sandboxID, ServiceName: name, State: pb.ServiceState_SERVICE_STATE_STOPPED}, nil
	}
	if IsTerminal(inst.GetState()) {
		return inst, nil
	}
	// A developer's stop is refused while a lifecycle operation holds the
	// sandbox's runtime lock: that operation is stopping the service anyway
	// (SWI-17, research R14).
	unlock, ok := s.lockRuntime(sandboxID)
	if !ok {
		return nil, ErrSandboxBusy
	}
	defer unlock()
	s.stopInstance(inst.GetId(), false)
	stopped, _ := s.insts.Get(inst.GetId())
	return stopped, nil
}

// stopInstance drives one instance to STOPPED, killing its tree and releasing its
// resources. Shared by Stop and the sandbox-teardown cascade; sandboxDown is the
// cascade's "the sandbox is no longer running" (see signalGroup).
func (s *Supervisor) stopInstance(instID string, sandboxDown bool) {
	h, hadHandle := s.handleFor(instID)
	if hadHandle {
		s.terminate(h, sandboxDown)
	}
	if proc := s.procFor(instID); proc != nil {
		out, truncated := proc.output()
		s.insts.Update(instID, func(i *pb.ServiceInstance) {
			i.Output, i.OutputTruncated = out, truncated
		})
	}
	s.release(instID)
	s.transition(instID, func(i *pb.ServiceInstance) {
		i.State = pb.ServiceState_SERVICE_STATE_STOPPED
		i.LocalPort = 0
		i.EndedAt = timestamppb.New(s.now())
	})
}

// terminate signals a service's process group, waits out the grace period, force-
// kills the remainder, and then waits for the listen port to actually go free.
func (s *Supervisor) terminate(h *procHandle, sandboxDown bool) {
	s.signalGroup(h, false, sandboxDown)

	deadline := time.Now().Add(s.stopGrace)
	for time.Now().Before(deadline) {
		if s.gone(h) {
			break
		}
		time.Sleep(portFreeWaitStep)
	}
	if !s.gone(h) {
		s.signalGroup(h, true, sandboxDown)
		// Confirm the force-kill actually landed. Returning while the tree is still
		// dying would let a following Start race a process that still holds the port.
		killDeadline := time.Now().Add(s.stopGrace)
		for time.Now().Before(killDeadline) && !s.gone(h) {
			time.Sleep(portFreeWaitStep)
		}
	}

	// The port is only really released once nothing holds it. Bounded by the same
	// grace period so a wedged kernel socket cannot hang the stop forever.
	if h.sbxPort != 0 {
		portDeadline := time.Now().Add(s.stopGrace)
		for time.Now().Before(portDeadline) && !portFree(h.hostPortOrListen()) {
			time.Sleep(portFreeWaitStep)
		}
	}
}

// signalGroup sends SIGTERM (or SIGKILL) to the service's process tree, by the
// route appropriate to where it runs. With sandboxDown (the sandbox stopped or
// went away) an in-sandbox tree is not signalled: it went down with the
// container, and `sbx exec` would start a stopped sandbox again (SWI-13). Only
// the host-side `sbx exec` child is dropped.
func (s *Supervisor) signalGroup(h *procHandle, force, sandboxDown bool) {
	if h.inSbx {
		// The host-side `sbx exec` child is not the service; the tree lives inside
		// the sandbox and has to be signalled there, using the PGID the setsid
		// wrapper announced (research R3).
		if h.pgid > 0 && h.ref != "" && !sandboxDown {
			sig := "TERM"
			if force {
				sig = "KILL"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cmd := s.runner.Exec(ctx, h.ref, []string{"/bin/sh", "-c", fmt.Sprintf("kill -%s -%d 2>/dev/null || true", sig, h.pgid)})
			_ = cmd.Run()
			cancel()
		}
		if (force || sandboxDown) && h.cancel != nil {
			h.cancel() // also drop the host-side sbx exec child
		}
		return
	}
	killPGID(h.pgid, force)
}

// gone reports whether the service's process has exited.
func (s *Supervisor) gone(h *procHandle) bool {
	if h.proc == nil {
		return true
	}
	return h.proc.hasExited()
}

// hostPortOrListen is the port to watch for release: the published host port when
// there is one, otherwise the port the command bound directly.
func (h *procHandle) hostPortOrListen() uint32 {
	if h.hostPort != 0 {
		return h.hostPort
	}
	return h.sbxPort
}

// setProc attaches the running process to an instance's handle.
func (s *Supervisor) setProc(instID string, proc *runningProc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.handles[instID]; ok {
		h.proc = proc
	}
}

// procFor returns the running process for an instance, if any.
func (s *Supervisor) procFor(instID string) *runningProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.handles[instID]; ok {
		return h.proc
	}
	return nil
}

// stopProc kills a process that never made it to RUNNING (a failed start). It is
// the abbreviated form of terminate: there is no instance state to keep in step,
// only a tree that must not be left behind.
func (s *Supervisor) stopProc(proc *runningProc, ref string, inSbx bool) {
	if proc == nil {
		return
	}
	h := &procHandle{proc: proc, pgid: proc.pgid, inSbx: inSbx, ref: ref}
	s.signalGroup(h, false, false)
	deadline := time.Now().Add(s.stopGrace)
	for time.Now().Before(deadline) && !proc.hasExited() {
		time.Sleep(portFreeWaitStep)
	}
	if !proc.hasExited() {
		s.signalGroup(h, true, false)
	}
}

// Group is an in-sandbox process group a stop's drain signals (research R14).
type Group struct {
	Ref  string
	PGID int
}

// startInterruptedDetail is the FAILED detail of a start a sandbox stop
// interrupted: the stop may fail, leaving its process running.
const startInterruptedDetail = "start interrupted by a sandbox stop; if the stop failed it may still be running in the sandbox"

// InterruptStarting is a sandbox stop's drain of its still-starting services
// (SWI-17, research R14, best effort): each one's in-sandbox group (ref,
// pgid) is read from its handle first, because release deletes the handle;
// then it is recorded FAILED (LAUNCH_FAILED, the may-still-be-running detail),
// which cancels its context (its launch client and readiness execs end, and no
// new one starts) and unpublishes its port; then every collected group is sent
// TERM through `sbx exec`. RUNNING services are left alone: their clients are
// up and end with the container. The groups are returned for KillGroups.
func (s *Supervisor) InterruptStarting(sandboxID string) []Group {
	var groups []Group
	for _, inst := range s.insts.ActiveBySandbox(sandboxID) {
		if inst.GetState() != pb.ServiceState_SERVICE_STATE_STARTING {
			continue
		}
		s.mu.Lock()
		if h := s.handles[inst.GetId()]; h != nil && h.inSbx && h.pgid > 0 && h.ref != "" {
			groups = append(groups, Group{Ref: h.ref, PGID: h.pgid})
		}
		s.mu.Unlock()
		s.failStarting(inst.GetId(), pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED, startInterruptedDetail)
	}
	s.signalGroups(groups, false)
	return groups
}

// KillGroups sends KILL to every group InterruptStarting collected, whatever
// its host client did: the client's exit says nothing about the group.
func (s *Supervisor) KillGroups(groups []Group) { s.signalGroups(groups, true) }

func (s *Supervisor) signalGroups(groups []Group, force bool) {
	for _, g := range groups {
		s.signalGroup(&procHandle{inSbx: true, ref: g.Ref, pgid: g.PGID}, force, false)
	}
}

// StopAll stops every non-terminal service of a sandbox. It is the teardown hook,
// run once the sandbox has left RUNNING: stopping, destroying, or refreshing a
// sandbox must leave no orphaned process and no held port (FR-048, US4-5).
// sandboxDown says the container is down: its in-sandbox services went with it
// and are not signalled through `sbx exec`, which would start it again (SWI-13,
// signalGroup). A sandbox that left RUNNING with its container still up (a kit
// being attached, an operation that failed) has them signalled as usual.
func (s *Supervisor) StopAll(sandboxID string, sandboxDown bool) {
	for _, inst := range s.insts.ActiveBySandbox(sandboxID) {
		s.stopInstance(inst.GetId(), sandboxDown)
	}
}
