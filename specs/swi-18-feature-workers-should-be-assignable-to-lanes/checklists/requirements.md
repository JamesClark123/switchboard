# Specification Quality Checklist: Worker Lanes

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-10-28
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Notes

- FR numbers start at FR-301 so they don't collide with feature 009 (FR-1xx) or SWI-17 (FR-2xx, PR #20).
- Four clarifications were settled with the developer during specify (spec § Clarifications, Session 2026-10-28). Lanes reserve workers, so un-laned actions run on open-lane workers only. Existing worker tags are dropped, not converted. Workers are assigned from both the Workers and Lanes views. Deleting a used lane confirms and then removes it everywhere.
- Informed defaults recorded in Assumptions: the lane-name rule, inclusive runs preferring lane workers (FR-314), runs no worker can take staying queued with a reason (FR-315), Manage-lanes changes saving immediately, and detach clearing lanes.
- FR-323's unsaved-changes guard depends on SWI-9 (PR #19).
- Pre-PR spec review, two rounds against the code. Fixes:
  - Lanes show on the sandbox list (the Orchestrator Sandboxes tab and surfaced rows); the app has no sandbox overview screen.
  - A worker's lanes are pushed to the daemon that runs it (FR-309a).
  - Lane changes start a dispatch pass and refresh briefings (FR-314a, FR-321).
  - Runs of deleted actions keep their lanes (FR-314b).
  - Saves that take the last open-lane worker warn first (FR-308a).
  - Stale membership saves are refused (FR-307a).
  - The waiting reason is new and is also shown to agents (FR-315).
  - Unmarked action saves from older clients are refused (FR-318a).
- PR #22 review (run-552), resolved in run-555 (spec § Clarifications):
  - An open-lane worker excludes the reasoning sandbox (FR-308).
  - Pushes are targeted, run in the background and are coalesced. Briefings refresh only when what they describe changes (FR-309a, FR-321).
  - The FR-315 waiting reason covers inclusive runs, is derived when runs are read and is never stored.
  - Kept runs keep the mode and follow lane renames (FR-314b).
  - The roster is the only source of lanes (FR-309b).
  - Action saves name lanes by identity and refuse stale ones (FR-316a).
  - FR-308a is daemon-enforced and also covers bulk edits, detach and un-laned saves.
  - Inclusive runs give way to later exclusive runs (FR-314, SC-002).
  - Dropped worker tags get a notice (FR-318b).
  - Lanes can be picked at attach (FR-306a).
  - Description cap; names only to agents; remote names are validated and sanitised. Lanes are not access control.
  - FR-307a is narrowed.
  - Nits: `open` reserved, the worker-tag field retired, reverse compatibility, orchestrator removal.
  - Consistency pass across FRs, edge cases, Key Entities and SCs.
- PR #22 re-review (run-558), resolved in run-561 with one model rather than per-finding patches (spec § Clarifications, run-558 session):
  - The **live roster** (FR-308) decides which workers count.
  - **Lanes before eligibility** (FR-306a/b): attach writes the roster entry first; launch uses an orchestrator-issued attach ticket.
  - **Two-tier open-lane safeguard** (FR-308a/b): changes through the orchestrator's daemon are guarded with one refusal shape and one confirm; anything else is made visible through FR-315.
  - **Reservation dispatch** (FR-314): O(runs + workers), and no run waits for a later one.
  - Minors and nits: FR-321 keeps today's triggers; FR-315 adds a reason for stopped or unreachable workers; FR-309b drops bad names instead of refusing; 32-lane cap; FR-318a refuses saves on an old daemon; FR-318b is persisted; FR-314b keeps the setting on the orchestrator record, written atomically.
  - Consistency pass: US2 #4, US4 #8, edge cases, Key Entities (live roster, attach ticket, kept lane setting, dropped worker tags), SC-002, SC-006 and Assumptions.
- PR #22 re-review (run-564), resolved in run-567. The lifecycle was written out end to end first and is now the spec's **Lifecycle** section (L1–L8). Every FR was checked against it, and an independent consistency check was run before committing. Changes:
  - The live roster is based on evidence (FR-308).
  - FR-314's fallback chain covers steals and refusals, with defined precondition refusals and a reservation preference.
  - Attach keeps link-then-commit, and the ticket closes the gap (FR-306a/b).
  - Adoption and drops happen on events as well as reconcile.
  - *Lanes pending* covers expired or mismatched tickets.
  - The check/confirm handshake is serialised by a lock (FR-308a).
  - Kept settings move onto runs atomically (FR-314b).
  - Reason runs are scoped out of lanes.
- PR #22 re-review (run-571), resolved in run-574 with new FR-308c:
  - Per-sandbox revisions, so only fresh records change the roster, and listing absences are ordered.
  - Every roster change is one atomic record update, with no lock held across network calls.
  - Links subscribe before listing, and a reported event-stream gap triggers a new reconcile.
  - Calling and deleting an action are serialised (FR-314b).
  - Detach is guard-and-mark, then unlink with no lock held, then settle (FR-308a, L5).
  - Lifecycle L1–L8 re-checked.
