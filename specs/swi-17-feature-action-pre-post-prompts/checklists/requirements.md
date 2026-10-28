# Specification Quality Checklist: Action Pre & Post Prompts

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

- The literal command text (`/model <choice>`, `/effort <level>`, `/clear`) appears in the spec because it is the user-visible behaviour the ticket asks for, not an implementation choice.
- Clarifications: 4 decided during specify (existing actions, post-prompt trigger, action types, spec folder) and 2 in the clarify pass (placeholders allowed, no step timeout). See spec § Clarifications.
- PR #20 review (run-481) settled step completion in the spec: FR-206a–c define *finished* as evidence after the submit (model turn: working then turn-end; agent-local step: empty ready input box plus the setting read back), and a step that cannot be confirmed halts as *not accepted* (FR-222: retry, skip or fail). The same review added FR-223 (no task-complete notifications for setup/cleanup turns), the leading-placeholder refusal (FR-204), body-only appending (FR-207), sandbox-wide cleanup holds (FR-215/216) and the manual-free rule; see spec § Clarifications, Session 2026-10-28.
- Planning gate (spec § Assumptions): for each built-in, establish on a real worker how FR-206b's checks are made; a built-in with no confirmable signal is applied another way, never by a timed wait.
