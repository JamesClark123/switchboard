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
