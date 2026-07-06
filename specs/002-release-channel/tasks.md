---

description: "Task list for GitHub-Only Release Channel"
---

# Tasks: GitHub-Only Release Channel

**Input**: Design documents from `/specs/002-release-channel/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: TDD was NOT requested. This feature validates via GoReleaser dry-run, an `install.sh`
self-test, and the existing Go unit tests for the shared asset contract; validation tasks are
included (not failing-first test tasks).

**Context**: Most of this channel already exists in the working tree from a prior session
(`.goreleaser.yaml`, `.github/workflows/release.yml`, `install.sh`, version ldflags, and the
`switchboard-update` asset contract). The work is **reconcile-to-spec + remove Homebrew**. Tasks are
phrased as verify/adjust against specific FRs, with explicit removals for the Homebrew tap.

**Organization**: Tasks are grouped by the three user stories so each can be implemented and
validated independently.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: US1 (install), US2 (publish), US3 (update)

## Path Conventions

Repo-root release artifacts (`.goreleaser.yaml`, `install.sh`, `.github/workflows/`, `README.md`)
plus the shared Go module `src/libs/switchboard-update/` and the two `cmd/` mains. Paths below are
repo-root-relative.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Prerequisites for hermetic release builds and local validation tooling.

- [X] T001 [P] Ensure every module's `go.sum` is self-contained for hermetic (`GOWORK=off`) builds by running `GOWORK=off go mod tidy` in `src/libs/switchboard-proto`, `src/libs/switchboard-update`, `src/services/switchboardd`, and `src/apps/switchboard-tui`; verify `GOWORK=off go list -deps ./cmd/sxb` and `./cmd/sxbd` succeed with no missing-`go.sum` errors.
- [X] T002 [P] Confirm GoReleaser v2 is installable/available (`go install github.com/goreleaser/goreleaser/v2@latest`) and record the baseline `goreleaser check` result in `specs/002-release-channel/quickstart.md` context.
- [X] T003 Confirm `LICENSE` exists at repo root (bundled into every archive per the release-assets contract) in `LICENSE`.

**Checkpoint**: Hermetic build inputs are ready; release tooling is available.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lock the single asset-naming + checksum contract that BOTH the installer and the in-app
updater depend on. Changing it breaks installs and updates simultaneously — it must be verified
before either story is trusted.

**⚠️ CRITICAL**: No user story is trustworthy until the shared contract is confirmed consistent.

- [X] T004 Verify the archive name pattern is identical across producer and both consumers: `.goreleaser.yaml` `archives.name_template` resolves to `switchboard_<os>_<arch>.tar.gz`, `install.sh` constructs the same string from `uname`, and `AssetName()` in `src/libs/switchboard-update/update.go` returns the same — per `specs/002-release-channel/contracts/release-assets.md`.
- [X] T005 Verify the checksum contract: `.goreleaser.yaml` emits `checksums.txt` (SHA-256), and both `install.sh` (grep + `sha256sum`/`shasum`) and `Fetch()` in `src/libs/switchboard-update/update.go` parse/verify that exact format before installing/applying.
- [X] T006 [P] Confirm/strengthen the lockstep unit test in `src/libs/switchboard-update/update_test.go` so it asserts `AssetName("darwin","arm64") == "switchboard_darwin_arm64.tar.gz"` and that `Fetch()` aborts on checksum mismatch (guards FR-006/FR-017 against future drift).

**Checkpoint**: One asset contract, three parties in agreement — user stories can proceed.

---

## Phase 3: User Story 1 - Install with a single command (Priority: P1) 🎯 MVP

**Goal**: A developer installs both binaries with one `curl | sh` command on any of the four
supported platforms, with the download integrity-verified before anything is written.

**Independent Test**: Serve the snapshot `dist/` via a local HTTP server and install using
`SWITCHBOARD_BASE_URL` (quickstart §2); confirm both binaries install and report a version, and that
a tampered archive aborts with nothing installed (quickstart §3).

### Implementation for User Story 1

- [X] T007 [US1] Verify platform detection in `install.sh`: `uname -s`→{darwin,linux} and `uname -m`→{amd64,arm64}, with an actionable abort for anything else (FR-010, FR-015).
- [X] T008 [US1] Verify download+verify ordering in `install.sh`: archive + `checksums.txt` are fetched (curl or wget), SHA-256 is checked, and a mismatch aborts BEFORE the install dir is touched (FR-011, SC-003).
- [X] T009 [US1] Verify install-dir logic in `install.sh`: prefer an on-`PATH` dir, elevate with `sudo` only when needed, fall back to `~/.local/bin`, and warn when the target is off `PATH` (FR-012).
- [X] T010 [US1] Verify latest-by-default plus `SWITCHBOARD_VERSION` pin and the `SWITCHBOARD_BASE_URL` test override resolve the correct download base in `install.sh` (FR-013; enables the independent test).
- [X] T011 [US1] Verify post-install output in `install.sh` prints how to start the daemon (`sxbd serve --boot`/`--watch`) and launch `sxb`, and states that each remote host running the daemon must be installed there too (FR-016).
- [X] T012 [US1] Verify missing-tooling and missing-platform-archive paths in `install.sh` abort with clear messages and install nothing (FR-015).

**Checkpoint**: `install.sh` satisfies FR-009–FR-016; MVP install works end-to-end against local artifacts.

---

## Phase 4: User Story 2 - Publish a release by tagging (Priority: P1)

**Goal**: One maintainer action (a `v*` tag) publishes a complete, verified GitHub Release — four
platform archives (each with both binaries), a checksum manifest, and stamped versions — with no
manual steps and no Homebrew.

**Independent Test**: `goreleaser release --snapshot --clean --skip=publish`, then confirm all four
archives exist, each contains both binaries, `checksums.txt` is present, `sxb version` reports the
snapshot version, and `goreleaser check` passes with no brew/token reference (quickstart §1).

### Implementation for User Story 2

- [X] T013 [US2] Remove the Homebrew tap from `.goreleaser.yaml`: delete the entire `brews:` block (and its explanatory comment) so no tap formula is produced (FR-001, FR-019, research R3).
- [X] T014 [US2] Remove `HOMEBREW_TAP_GITHUB_TOKEN` from the `env:` of the goreleaser step in `.github/workflows/release.yml`, leaving only `GITHUB_TOKEN` (FR-001, FR-019).
- [X] T015 [P] [US2] Verify the two `builds:` in `.goreleaser.yaml` cover `{darwin,linux}×{amd64,arm64}` with `GOWORK=off` + per-module `dir:`, and that `archives:` bundles BOTH `sxb` and `sxbd` per archive plus `LICENSE`/`README` (FR-003, FR-004).
- [X] T016 [P] [US2] Verify version stamping in `.goreleaser.yaml` ldflags (`-X main.version/commit/date`) and the `version` subcommands in `src/services/switchboardd/cmd/sxbd/main.go` and `src/apps/switchboard-tui/cmd/sxb/main.go` so a released binary reports the tag and a source build reports `0.1.0-dev` (FR-007).
- [X] T017 [US2] Verify `.github/workflows/release.yml` triggers on `push: tags: ['v*']`, uses `fetch-depth: 0` + `submodules: recursive`, sets up Go 1.26 with cache paths for all four `go.sum` files, and runs `goreleaser release --clean` (FR-002).
- [X] T018 [US2] Run `goreleaser check` and a snapshot build; confirm success with NO `brews`/`HOMEBREW_TAP_GITHUB_TOKEN` reference remaining, and that a failed build would not present a partial release (FR-008, SC-005).

**Checkpoint**: A tag yields a complete, verified, Homebrew-free release (validated via snapshot).

---

## Phase 5: User Story 3 - Update existing installs to the latest release (Priority: P2)

**Goal**: Updating the client and every host uses the same single channel — re-running the installer
upgrades in place, and the in-app updater draws from the identical release assets.

**Independent Test**: Re-run the installer against a newer snapshot and confirm the binaries are the
newer version; pin an exact version; and `cd src/libs/switchboard-update && go test ./...` confirms
the updater consumes the same asset names + checksum format (quickstart §4, §6).

### Implementation for User Story 3

- [X] T019 [US3] Verify re-running `install.sh` performs an idempotent in-place over-install (the update path for client and hosts), and that `SWITCHBOARD_VERSION` installs exactly the pinned tag (FR-013, FR-014, SC-004, SC-006).
- [X] T020 [US3] Verify the in-app updater consumes the SAME assets this channel publishes — `FetchBinary()`/`Fetch()` in `src/libs/switchboard-update/update.go` use `AssetName` + `checksums.txt` from the release-assets contract (single source of truth) (FR-017).
- [X] T021 [US3] Keep the `IsBrewManaged` guard as defensive-only: confirm it remains in `src/libs/switchboard-update/update.go` (and its callers in the daemon `internal/grpc/update.go` and TUI `internal/ui/update_ui.go`) but implies no Homebrew *channel* and is not surfaced as an install method (research R3, FR-019).

**Checkpoint**: One command / one asset source updates client and all hosts; no second channel.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Documentation single-channel cleanup and full end-to-end validation.

- [X] T022 [P] Update `README.md`: remove all Homebrew install/updating instructions and the tap-repo/PAT one-time-setup steps; keep the `curl | sh` one-liner, the `SWITCHBOARD_VERSION` pin + `SWITCHBOARD_INSTALL_DIR` custom-dir knobs, the remote-host install note, the macOS manual-download `xattr` caveat, and the releasing/tagging section (FR-018, FR-019).
- [X] T023 [P] Grep `README.md` (and any docs) to confirm no source other than GitHub Releases is referenced for obtaining binaries, and `brew`/`homebrew` appears only (if at all) as an explicit "not used" note (FR-019).
- [X] T024 [P] Lint and syntax-check `install.sh` (`sh -n install.sh`; `shellcheck install.sh` when available) and wire `shellcheck` into the CI verification step if not already present.
- [X] T025 Run the full quickstart validation in `specs/002-release-channel/quickstart.md` (§1–§7) end-to-end against a local snapshot + file server.
- [X] T026 Run the standard gate: `make all` (fmt-check, vet, lint, test) and `make cover` (≥90% per module) stay green after the changes.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: no dependencies — start immediately.
- **Foundational (Phase 2)**: depends on Setup; locks the shared asset contract that BLOCKS trust in all stories.
- **User Stories (Phase 3–5)**: depend on Foundational. US1 and US2 are both P1 and independently validatable (US1 against local artifacts; US2 via snapshot); US3 (P2) builds on US1's script and the shared contract.
- **Polish (Phase 6)**: depends on the stories being complete.

### User Story Dependencies

- **US1 (Install, P1)**: after Foundational; independently testable via `SWITCHBOARD_BASE_URL` + a local server (no real release needed).
- **US2 (Publish, P1)**: after Foundational; independently testable via `goreleaser --snapshot`. Contains the Homebrew removal (T013–T014).
- **US3 (Update, P2)**: after Foundational; reuses US1's `install.sh` and the Phase 2 contract. No new channel.

### Parallel Opportunities

- Setup: T001, T002 in parallel.
- Foundational: T006 in parallel with the T004/T005 verifications.
- US2: T015, T016 in parallel (different concerns) after the brew removals T013/T014.
- Polish: T022, T023, T024 in parallel (docs + lint, different files).
- With capacity, US1 and US2 can proceed in parallel once Phase 2 is done (mostly disjoint files: `install.sh` vs `.goreleaser.yaml`/workflow).

---

## Parallel Example: Phase 1 Setup

```bash
Task T001: "GOWORK=off go mod tidy across all four modules; verify hermetic go list"
Task T002: "Install goreleaser v2 and capture the goreleaser check baseline"
```

## Parallel Example: User Story 2 (after T013/T014)

```bash
Task T015: "Verify build matrix + both-binaries-per-archive in .goreleaser.yaml"
Task T016: "Verify version ldflags + version subcommands in both cmd mains"
```

---

## Implementation Strategy

### MVP First (User Story 1)

1. Phase 1 Setup → 2. Phase 2 Foundational (shared contract) → 3. Phase 3 US1 (install).
4. **STOP and VALIDATE**: install end-to-end against local snapshot artifacts (quickstart §2/§3).

Note: US1 is the MVP because it is independently demonstrable against local artifacts, but a real
end-user install requires US2 (a published release). In practice, land US2 alongside US1 since both
are P1 and their files are disjoint.

### Incremental Delivery

1. Setup + Foundational → contract locked.
2. US1 → validate install locally (MVP).
3. US2 → tag-driven publish, Homebrew removed → snapshot-validate.
4. US3 → re-run-to-update + updater lockstep.
5. Polish → docs single-channel, quickstart, full gate.

---

## Notes

- This feature is reconcile + removal, not greenfield: most tasks verify existing behavior against a
  specific FR and file; T013/T014/T022/T023 are the concrete Homebrew-removal changes.
- The single asset-naming + checksum contract (Phase 2) is the one invariant that must never drift —
  it binds the producer (`.goreleaser.yaml`) and both consumers (`install.sh`,
  `src/libs/switchboard-update`).
- Version subcommands live in coverage-excluded `cmd/`; the shared `switchboard-update` contract is
  unit-tested, so `make cover` stays ≥90% per module.
- Commit after each task or logical group; stop at any checkpoint to validate a story independently.
