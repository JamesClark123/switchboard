# Specification Quality Checklist: MCP Page Quality of Life

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-10-30
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

- Host-runtime vocabulary (secret names such as `mcp:<server>:client_secret`, the replace semantics) is
  kept on purpose: it is the user-facing contract the developer types and sees, as in 008's spec.
- Clarified 2026-10-30: the fixed OAuth callback port (ticket item 5) is deferred to a follow-up ticket, and an edit never touches live sandboxes (FR-419).
- Review round 3 (pr-review run run-c275c8f2-938): scope cut rather than extended — Restore folded into Retry, *client secret mismatch* and *not a credential* removed, pending replace persisted with no time limit, no new notifications.
- Review round 4 (pr-review run run-c275c8f2-942): fixes kept to rule changes, no new mechanism — a retry is compared with the previous definition (FR-406), a resolved template counts as *not reported* (FR-423), a failed add re-reads the server list before choosing the outcome (FR-406), a host-CLI ending clears skipped entries (FR-425), and the fingerprint is a keyed MAC (FR-404).
- Review round 5 (pr-review run run-c275c8f2-946): rule clarifications only — a failed remove or add is re-read only after its process has exited (FR-406), every overwritten secret counts as stored by a retry (FR-406), the resolved-template rule follows a pinned host CLI behaviour flag with templates as the default (FR-422, FR-423), and a daemon restart is refused as *stale* with the form reloading in place and keeping edits (FR-404).
- Review round 6 (pr-review run run-c275c8f2-950): rule changes only — a timed-out add keeps the secrets it created and names them with the overwritten ones, and whether a killed add/rm can still complete is documentation-derived (FR-406, FR-412, FR-422); a reload after a refused save keeps edited fields over host changes, flags conflicts, and drops entered secrets when it replaces the URL or client (FR-404); the *templates* branch of FR-423 applies only to credential headers.
- Review round 7 (pr-review run run-c275c8f2-954): scope cut — the round-6 *keep created secrets after a timed-out add* rule is reverted; a timed-out add is handled as any failed add and a late finish on the host is accepted as *registered by another path* (FR-406, FR-412); a Retry form reloads from the pending replace or closes if it ended, *edited* means differs from the opened value, and a reload drops entered secrets with no placeholder or client ID left (FR-404).
- Review round 9 (pr-review run run-c275c8f2-962): scope cut — FR-406's failed-add handling collapsed into one rule: a failed or timed-out add (or a store after the remove) is never re-checked or counted as succeeded; the host state is treated as unknown, every secret the save stored (created and overwritten) is removed, and a retry starts from scratch. Dropped: the listed-server match check, the retry guard against the held definition, the overwritten-names list and its union rule. When a pending replace ends as *registered by another path*, switchboard's stored definition is dropped too, so Edit never pre-fills stale URL/client values. Remaining narrowing deferred to *Out of scope* (FR-406, FR-409, FR-412, SC-004).
- Review round 15 (pr-review run run-c275c8f2-986): scope cut — the round-14 fail-closed rule for a failed identity clear is reverted to the round-13 list re-check (a missing `:identity` record counts as already cleared), so no re-bind mark is kept on a failed clear and no FR-422 list flag is needed; a failed clear that the list cannot see is a known limitation. US1 #5 discard confirmation names only *removal failed* secrets still listed (FR-406).
- Review round 16 (pr-review run run-c275c8f2-990): wording only — FR-415's re-check is a fresh read of the secret list (not FR-404's snapshot) and fails toward not-done when the record is still listed or the list cannot be read; in that case the failure names the `:identity` record to remove and the server keeps (or gets) *client re-bind pending* (the not-listed case still counts as cleared, no mark). FR-422 lists whether the secret list shows the `:identity` record as a plain contract entry (no behaviour flag). SC-004 is qualified by the round-15 known limitation. A US2 test scenario for the unreadable-list case is carried into the plan phase as an open item.
- Review round 17 (pr-review run run-c275c8f2-994): wording only — FR-415's failure message leads with the in-product retry (re-enter the client secret and save), names the `:identity` record as the manual alternative and says the mark clears only on the next save that stores the client secret; the form's warning and pending text are worded to hold for both causes of the mark. A still-listed record shows the removal failure (*not cleared*); an unreadable list shows both failures (*may still be in place*), and FR-404's outcome reports cleared / not cleared / unconfirmed. SC-004 is qualified for the reported failed clear too. Whether any later save of a marked server retries the clear (and whether the mark records its cause) is a round-17 known limitation carried into the plan phase as an open item.
