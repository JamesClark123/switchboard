// Package portforward owns the daemon side of feature 006 (Port Forwarding):
// resolving the services a sandbox's attached kits declare, starting and stopping
// them, deciding when one is actually reachable, and relaying bytes from the
// client's machine to the listener.
//
// # Two invariants that must never regress
//
// SECURITY INVARIANT: the start path accepts a service NAME only, and resolves it
// against the sandbox's persisted Sandbox.services allowlist. No caller — not the
// client, and certainly not the sandbox's agent, which has no route here at all —
// can supply a command string. The kit's declaration is the whole contract for
// what may run and where (FR-044, FR-045).
//
// REACHABILITY INVARIANT: a service reaches SERVICE_STATE_RUNNING only after a
// successful TCP dial of its host endpoint — never merely because the process
// started. A displayed address that does not work is the one failure this feature
// cannot tolerate, since trusting the address is the entire point (FR-047,
// SC-007, spec Key Decision 6).
//
// # Design decisions (specs/006-port-forwarding/research.md)
//
//	R1 The CLIENT owns every listener on the developer's machine and relays over a
//	   bidirectional ForwardPort stream on the existing daemon connection. One path
//	   for local and SSH hosts; one allocator, so port uniqueness is structural.
//	R2 An in-sandbox listener is reached by publishing a loopback-bound host port
//	   (`sbx ports --publish 127.0.0.1:P:L/tcp`), allocated by binding :0.
//	R3 An in-sandbox command runs under `setsid` and announces its PGID on stderr,
//	   so the tree can be killed from outside the sandbox.
//	R4 An on-host command containing {{port}} gets a freshly allocated host port, so
//	   concurrent sandboxes coexist; without it the declared port is used and a
//	   second instance fails PORT_IN_USE. The author chooses, visibly.
//	R5 Readiness is a dial with backoff. When the window elapses inside a sandbox,
//	   /proc/net/tcp is read IN the sandbox to tell a loopback-only bind (which is
//	   diagnosable, with a remedy) from nothing listening at all.
//	R6 Stopping signals the whole process GROUP, waits a grace period, force-kills,
//	   and releases the port only once the listen port is observed free.
//	R7 Declarations persist on the sandbox; instances are in-memory and
//	   session-scoped, so a daemon restart leaves nothing running and nothing
//	   claiming to be.
//	R9 Every bound is a package constant — this feature adds no environment
//	   variables.
//
// # Relationship to internal/escapehatch
//
// The two packages are deliberately similar in shape (structured kit sidecar,
// later-kit-wins resolution, in-memory session-scoped records, one publish
// choke-point for events) and differ in three ways worth remembering:
//
//   - escape hatch runs to completion; a service runs until stopped.
//   - escape hatch is invoked by the AI; a service is started only by the human.
//   - escape hatch keeps the HEAD of captured output, because a short command's
//     failure is at the start. This package keeps the TAIL, because a long-running
//     service's diagnostic bytes are the last ones before it died.
package portforward
