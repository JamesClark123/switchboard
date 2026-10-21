// Package worker is the WORKER SIDE of feature 009 (research R1): what every
// daemon runs for the sandboxes it owns, whoever the controller is — the
// controller record on the sandbox (AttachController CAS / DetachController /
// UpdateWorker, R3), the per-worker gateway registration and token (R4), the
// MCP endpoint the host gateway talks to (R5, the official Go MCP SDK), the
// link acceptor that relays tool calls to the orchestrator (R6), the guarded
// prompt delivery (R7) and the managed CLAUDE.md briefing block (R12). It must
// work without any orchestrator code loaded: in the remote case the two sides
// run on different machines.
package worker
