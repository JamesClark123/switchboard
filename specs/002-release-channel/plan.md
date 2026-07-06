# Implementation Plan: GitHub-Only Release Channel

**Branch**: `002-release-channel` | **Date**: 2026-07-05 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/002-release-channel/spec.md`

## Summary

Deliver a single distribution channel for the `sxb`/`sxbd` binaries: automated GitHub Releases
(cross-compiled archives + SHA-256 checksums, produced on a version tag) plus a `curl | sh`
install script that detects the platform, verifies the download, and installs both binaries.
Homebrew is explicitly removed so there is exactly one verifiable, self-update-compatible source of
truth. Most of the machinery already exists in the working tree from a prior session
(`.goreleaser.yaml`, `.github/workflows/release.yml`, `install.sh`, version ldflags, the
`switchboard-update` asset contract); this plan **reconciles that machinery against the spec and
strips the Homebrew tap** (the `brews:` block, the tap-token wiring, and brew mentions in docs).

## Technical Context

**Language/Version**: Go 1.26 (the two binaries; version stamped via `-ldflags`); POSIX `sh`
(install script); YAML (GoReleaser config + GitHub Actions workflow).

**Primary Dependencies**: GoReleaser v2 (release build/publish engine); GitHub Actions
(`goreleaser/goreleaser-action@v6`, `actions/setup-go@v5`, `actions/checkout@v4`); GitHub Releases
(artifact host); `github.com/minio/selfupdate` via `src/libs/switchboard-update` (the in-app
updater that consumes the same assets — asset naming/checksum contract is shared).

**Storage**: N/A (artifacts live on GitHub Releases; no datastore).

**Testing**: `goreleaser check` + `goreleaser release --snapshot --clean --skip=publish` (dry-run
build of all archives/checksums/version stamping); `install.sh` exercised against a local file
server via a `SWITCHBOARD_BASE_URL` override + `sh -n` syntax check (+ `shellcheck` when available
in CI); existing Go unit tests for version subcommands and `switchboard-update` (`AssetName`,
`Fetch` SHA-256 verify).

**Target Platform**: macOS and Linux, `amd64` + `arm64` (four platform archives per release).

**Project Type**: Release/distribution tooling for a Go CLI + daemon monorepo (CI config + shell
installer + build-time version wiring); not a runtime application component.

**Performance Goals**: A tagged release publishes all four verified platform archives with no manual
steps; an install (download + verify + place two binaries) completes in seconds on a normal
connection.

**Constraints**: No distribution channel other than GitHub Releases (no Homebrew, no OS package
repos). Integrity via SHA-256 checksum manifest only (no signing/notarization). Install requires
only commonly available tooling (`curl`/`wget`, `sha256sum`/`shasum`). Hermetic release builds
(each module's `go.sum` self-contained; `GOWORK=off` + `replace`).

**Scale/Scope**: Two binaries × four platforms per release; one hosted install script; one release
workflow. Frequent releases (design optimizes for one-step publishing).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution (`.claude/rules/shared/`, v2.3.1) codifies a TypeScript/pnpm/Biome/Vitest toolchain
for **workspace packages**. This feature adds no TypeScript package; it is CI/release config, a POSIX
shell installer, and Go build-time version flags. Gate-by-gate:

| Rule | Status | Notes |
|------|--------|-------|
| I Formatting (Biome) | ✅ N/A | No TS/JS/JSON source added. `.goreleaser.yaml`/workflow YAML and `install.sh` are outside Biome's scope; keep them `gofmt`-adjacent tidy and LF-only per `.editorconfig`. |
| II Linting (Biome) | ✅ N/A | No TS/JS. Shell is lint-gated by `shellcheck` (advisory) instead. |
| III Type Safety | ✅ N/A | No TS. Go stays `go vet`-clean per the 001 Go-deviation posture. |
| IV Naming & Layout | ✅ Pass | New repo-root artifacts (`.goreleaser.yaml`, `install.sh`) follow conventional ecosystem names; kebab-case elsewhere. |
| V Verification Before Merge | ✅ Pass | Adds `goreleaser check`/snapshot + `shellcheck` to the existing Go gate; no CI gate is weakened. |
| VI Testing Discipline | ⚠️ Deviation (recorded) | Vitest/Playwright/Storybook/90% coverage target TS packages. Release config + a shell installer are validated by `goreleaser` dry-run, `install.sh` self-test against a local server, and existing Go tests — consistent with the Go-toolchain deviation already ratified for feature 001. |
| VII Containerized Deployment | ⚠️ Deviation (recorded) | The daemon/TUI are host binaries, not web-deployables; feature 001 already records that **distribution is compiled binaries / release artifacts, not images**. This feature *is* the concrete realization of that decision — reinforces, not violates, the recorded stance. |
| VIII Environment Variables | ✅ N/A | Rule VIII governs per-package `env.ts`. `install.sh` reads a few documented env knobs (`SWITCHBOARD_VERSION`, `SWITCHBOARD_INSTALL_DIR`, `SWITCHBOARD_BASE_URL`); a shell installer is not a workspace package, so `env.ts` does not apply. Knobs are documented in the script header + README. |
| Governance / Tooling substitutions | ⚠️ Deviation (recorded) | GoReleaser, GitHub Actions, and a shell installer are not in the "Tooling Standards" list (which is TS-centric). They fall under the same Go-toolchain deviation umbrella as 001; see Complexity Tracking. |

**Result**: No unjustified violations. All deviations are the already-ratified Go-toolchain posture
from feature 001 extended to release tooling; recorded in Complexity Tracking below.

## Project Structure

### Documentation (this feature)

```text
specs/002-release-channel/
├── plan.md              # This file (/speckit-plan command output)
├── research.md          # Phase 0 output (/speckit-plan command)
├── data-model.md        # Phase 1 output (/speckit-plan command)
├── quickstart.md        # Phase 1 output (/speckit-plan command)
├── contracts/           # Phase 1 output — asset-layout + install-script + release-trigger contracts
│   ├── release-assets.md
│   └── install-script.md
└── tasks.md             # Phase 2 output (/speckit-tasks command - NOT created by /speckit-plan)
```

### Source Code (repository root)

Release tooling lives at the repo root (conventional for GoReleaser/CI) and reuses the shared asset
contract in `src/libs/switchboard-update/`:

```text
.goreleaser.yaml                         # release build/publish config (REMOVE brews:)
.github/workflows/release.yml            # tag-triggered release job (REMOVE HOMEBREW_TAP_GITHUB_TOKEN)
install.sh                               # curl|sh installer (exists; satisfies most install FRs)
LICENSE                                  # bundled into each archive
README.md                                # install/updating/releasing docs (REMOVE brew mentions)

src/libs/switchboard-update/             # asset-naming + checksum contract shared with the installer
└── update.go                            # AssetName(), FetchBinary() (SHA-256 verify) — keep in lockstep

src/services/switchboardd/cmd/sxbd/main.go   # version/commit/date ldflags + `version` subcommand (exists)
src/apps/switchboard-tui/cmd/sxb/main.go     # version/commit/date ldflags + `version` subcommand (exists)
```

**Structure Decision**: Repo-root release artifacts (matching ecosystem convention for GoReleaser +
GitHub Actions) plus the existing shared `switchboard-update` module as the single source of the
asset-naming/checksum contract that both the installer and the in-app updater must honor. No new
Go module or workspace package is introduced. The primary work is **reconciliation + Homebrew
removal**, not greenfield construction.

## Complexity Tracking

> Deviations from the TS-centric constitution, carried over from the ratified feature-001 posture.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| Release tooling is GoReleaser + GitHub Actions + POSIX shell (not Biome/Vitest/pnpm scripts) | The product is Go binaries, not a TS package; cross-compiling darwin/linux×amd64/arm64, generating checksums, and publishing a GitHub Release is GoReleaser's native job, triggered by CI on a tag | A pnpm/Biome/Vitest pipeline cannot cross-compile or publish Go binaries; hand-rolled `gh release upload` scripts reintroduce the manual, error-prone steps FR-002/FR-008 exist to eliminate |
| Distribution is release artifacts, not containers (Rule VII) | `sxb` is a local terminal binary and `sxbd` is a host-level daemon (owns the host's Docker/FS/ssh); neither is web-deployable | Containerizing a host-control daemon is self-defeating; recorded and accepted in feature 001 |
| Shell installer validated by `shellcheck` + self-test, not Biome/Vitest (Rule VI) | A `curl | sh` bootstrap must be a dependency-free POSIX script; it cannot be a Vitest-tested TS module | A TS installer would require Node present before install — defeating the one-command, no-toolchain goal (FR-009) |
