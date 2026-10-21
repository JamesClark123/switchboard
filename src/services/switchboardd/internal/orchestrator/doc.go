// Package orchestrator is the ORCHESTRATOR SIDE of feature 009 (research R1):
// the persisted records (orchestrators + receipt-ordered runs, R9), one runtime
// per orchestrator (host links over the shared dial library, R2/R6; the
// dispatch pass, R9; the timed-event scheduler, R10; status folding, review and
// reconcile, R7), action validation and prompt composition (R8), the four tool
// semantics behind link call frames (R5) and the briefing renderer (R12). It is
// hosted in-process by the daemon of the machine the developer placed the
// orchestrator on — no new binary, process or port (FR-105).
package orchestrator
