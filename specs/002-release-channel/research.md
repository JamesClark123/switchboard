# Phase 0 Research: GitHub-Only Release Channel

All Technical Context items were resolvable from the spec, the prior implementation session, and
the feature-001 distribution posture. No `NEEDS CLARIFICATION` remained. Decisions below are the
consolidated rationale.

## R1 — Artifact host: GitHub Releases (only)

- **Decision**: Publish every version as a GitHub Release; that is the sole artifact host and the
  single source of truth for both `install.sh` and the in-app updater (FR-001, FR-017).
- **Rationale**: The repo already lives on GitHub; Releases give free, versioned, CDN-backed asset
  hosting with a stable `…/releases/latest/download/<asset>` URL — no server or CDN to run
  (spec Assumption "Hosting"). One host = one integrity story and no drift between installer and
  updater sources.
- **Alternatives rejected**: Self-hosted download server / S3 (operational burden, another thing to
  secure); a package-manager repo as the primary host (see R3).

## R2 — Release engine: GoReleaser v2 on a tag, via GitHub Actions

- **Decision**: A `push: tags: ['v*']` workflow runs GoReleaser, which cross-compiles both binaries
  for the four platforms, bundles each archive with both binaries, generates `checksums.txt`, and
  publishes the GitHub Release — one maintainer action, zero manual steps (FR-002, FR-003, FR-008).
- **Rationale**: GoReleaser is the de-facto standard for Go release automation; it natively handles
  cross-compilation, archive bundling, checksums, changelog, and the GitHub Release upload. The
  work already exists and dry-runs clean (`goreleaser release --snapshot`).
- **Monorepo detail**: Two binaries in separate modules under `go.work`. Each GoReleaser build sets
  `dir:` to its module and `env: [GOWORK=off]` so it resolves local libs via that module's
  `replace` directive — hermetic and module-local. This requires each module's `go.sum` to be
  self-contained (kept tidy with `GOWORK=off go mod tidy`).
- **Alternatives rejected**: Hand-rolled `go build` matrix + `gh release upload` (reinvents
  GoReleaser, more manual surface, easy to ship a partial/unverifiable release — the opposite of
  FR-008); building binaries in-workflow and skipping GoReleaser (loses checksums/archive/changelog
  wiring for little gain).

## R3 — No Homebrew, no OS package managers (single channel)

- **Decision**: Remove the Homebrew tap entirely — delete the `brews:` block from
  `.goreleaser.yaml`, drop `HOMEBREW_TAP_GITHUB_TOKEN` from the workflow, and remove brew mentions
  from docs (FR-001, FR-019). Do not add apt/rpm/etc.
- **Rationale**: The user explicitly chose a single GitHub + `install.sh` channel. Homebrew is in
  *tension* with the in-app self-updater: brew-managed binaries must not be rewritten underneath
  brew, so the updater has to special-case them — a worse update experience and a second source of
  truth. Removing brew also removes one-time setup cost (tap repo + PAT secret) with no coverage gap
  (SC-005).
- **Keep (defensive)**: The `IsBrewManaged` guard in the updater (`switchboard-update` +
  `sxbd`/`sxb`) stays as cheap insurance against overwriting a binary a user placed under a Homebrew
  prefix by other means. It implies no brew *channel* and is not user-facing documentation, so it
  does not conflict with FR-019.
- **Alternatives rejected**: Keep brew as an optional secondary channel (contradicts the spec's
  single-channel intent and the update tension above); a signed APT repo (heavy GPG/repo maintenance
  for a dev tool — out of scope).

## R4 — Integrity: SHA-256 checksum manifest, verified before install

- **Decision**: Each release ships a `checksums.txt` (SHA-256 per archive). The installer and the
  in-app updater both verify the downloaded archive against it and abort — installing nothing — on
  mismatch (FR-005, FR-011, SC-003). No cryptographic signing/notarization in this feature.
- **Rationale**: SHA-256 over HTTPS from the official repo is the standard baseline for self-updaters
  and is dependency-free to verify (`sha256sum`/`shasum`). Signing (cosign/minisign) adds key
  management + a verify dependency for marginal benefit at this stage (spec Assumption
  "Integrity model").
- **macOS quarantine**: Binaries fetched by `curl`/the updater over HTTPS are not Gatekeeper-
  quarantined; only manual browser downloads are. Handled by documentation (`xattr -d …`), not code.
- **Alternatives rejected**: cosign-signed checksums (deferred; extra plumbing + pinned public key);
  no verification (unacceptable — violates FR-011/SC-003).

## R5 — Asset naming contract (shared installer ↔ updater)

- **Decision**: Archives are named `switchboard_<os>_<arch>.tar.gz` (os ∈ {darwin, linux}, arch ∈
  {amd64, arm64}); `checksums.txt` sits beside them. This exact pattern is produced by GoReleaser's
  `name_template` and consumed by both `install.sh` and `switchboard-update.AssetName()` (FR-006,
  FR-017).
- **Rationale**: A stable, script-constructible name lets the installer build the download URL
  without scraping the Releases page, and lets the updater request the right asset per platform. It
  is the single contract that keeps producer and both consumers in lockstep — its stability is a
  release-safety invariant (changing it breaks installs and updates).
- **Alternatives rejected**: Version-in-filename names (breaks the stable
  `…/latest/download/<asset>` URL the installer relies on); per-binary archives (FR-003 requires
  both binaries per archive so one download yields a working install).

## R6 — Installer behavior: `curl | sh`, latest-by-default, idempotent

- **Decision**: A single hosted POSIX `sh` script detects OS/arch, resolves the latest release (or a
  pinned `SWITCHBOARD_VERSION`), downloads archive + checksums, verifies SHA-256, extracts, and
  installs both binaries to `/usr/local/bin` (elevating only if needed) or `~/.local/bin`, warning
  if the target is off `PATH`. Re-running upgrades in place (FR-009–FR-016, SC-004).
- **Rationale**: `curl | sh` is the lowest-friction cross-platform bootstrap and needs no toolchain
  beyond `curl`/`wget` + `sha256sum`/`shasum` (spec Assumption "Client environment"). Latest-by-
  default with a version pin covers both first-run and reproducible installs (FR-013/SC-006).
  Idempotent over-install is the update path for the client and every host, including remote hosts
  bootstrapped by running the same command there (FR-014/US3-AS4).
- **Testability hook**: A `SWITCHBOARD_BASE_URL` override lets the script install from a local file
  server so the full download→verify→install path is testable offline (used in quickstart.md).
- **Alternatives rejected**: A compiled installer (needs a toolchain present first — defeats
  FR-009); requiring `gh` CLI (extra dependency); Homebrew as the install path (R3).

## R7 — Version identity: semver tags stamped via ldflags

- **Decision**: Releases are `vX.Y.Z` tags; GoReleaser injects `-X main.version/commit/date` so a
  released binary's `version` subcommand reports the real version, while a plain source build reports
  `0.1.0-dev` (FR-007, SC-002). The `version` subcommands already exist in both mains.
- **Rationale**: ldflags stamping is the standard Go release convention and lets the in-app updater's
  skew/latest comparison work against real versions. Confirmed working in the snapshot dry-run.
- **Alternatives rejected**: A committed version file (drifts from tags, needs a commit per release);
  no stamping (updater can't compare versions — breaks the update UX).

## Reconciliation summary (what changes vs. the working tree)

| Area | Current state | Action for this feature |
|------|---------------|-------------------------|
| `.goreleaser.yaml` | builds+archives+checksums ✅, plus a `brews:` tap block | **Remove** `brews:` block (R3) |
| `.github/workflows/release.yml` | tag-triggered GoReleaser ✅, passes `HOMEBREW_TAP_GITHUB_TOKEN` | **Remove** the tap token env (R3) |
| `install.sh` | detect/download/verify/install ✅, `SWITCHBOARD_VERSION`/`_INSTALL_DIR`/`_BASE_URL` ✅ | Keep; confirm against FR-009–FR-016 |
| `README.md` | includes Homebrew install/updating mentions | **Remove** brew sections; keep curl + releasing + remote-host + macOS-xattr docs (FR-018/019) |
| `switchboard-update` (AssetName/Fetch) | matches `switchboard_<os>_<arch>.tar.gz` + SHA-256 ✅ | Keep; it is the shared contract (R5) — no change |
| version ldflags + `version` subcommands | present in both mains ✅ | Keep (R7) |
| `IsBrewManaged` guard | present in updater/daemon | **Keep** as defensive-only (R3) |
