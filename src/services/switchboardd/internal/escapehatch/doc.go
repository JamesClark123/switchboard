// Package escapehatch runs a sandbox's small, human-authored set of whole, fixed
// commands OUTSIDE the sandbox — on the daemon's host, in the sandbox's bind-mounted
// workspace — and delivers each result back to the agent, even with no terminal
// attached. It is the daemon side of feature 005 (see specs/005-escape-hatch/).
//
// Design (research R1..R7):
//
//   - R1 Invocation is async over the existing agent-hook HTTP server: the agent runs
//     a daemon-injected wrapper that POSTs the command NAME to /escape-hatch/run
//     (http.go). The result is pushed back into the agent via agent.Registry.Prompt
//     (the detached-prompt PTY path), so delivery survives terminal detachment.
//   - R2 Commands are STRUCTURED proto on KitSpec, never in the opaque Docker
//     spec.yaml; the daemon owns enforcement + execution.
//   - R3 At bring-up the daemon injects a wrapper (.switchboard/escape-hatch) + a
//     marker-delimited rule block in the workspace CLAUDE.md (inject.go).
//   - R4 The executor runs `sh -c <exact string>` in the workspace, streamed + bounded
//     (1 MiB), with a per-command-or-30-min timeout, cancelled on sandbox stop
//     (executor.go).
//   - R5 requires-approval commands block on a 5-minute deny-by-default window
//     (consent.go), decided by the DecideEscapeHatchRun RPC.
//   - R6 Run records are in-memory, session-scoped (runs.go); observability rides the
//     existing event/notification hub.
//   - R7 Bounds are package constants — no new env vars.
//
// SECURITY INVARIANT (SC-004): the invocation endpoint accepts a command NAME only and
// resolves it against the sandbox's persisted allowlist. No code path in this package
// runs a caller-supplied command string; the agent chooses which authorized command to
// run and when, never what it is.
package escapehatch
