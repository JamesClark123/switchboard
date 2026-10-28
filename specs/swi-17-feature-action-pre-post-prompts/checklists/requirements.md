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
- Risk carried to planning (spec § Assumptions): how the agent's built-in slash commands are detected as finished — confirm on a real worker before designing the step tracker.
