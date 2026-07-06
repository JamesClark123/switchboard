# Phase 1 Data Model: GitHub-Only Release Channel

This feature has no runtime datastore. Its "entities" are release artifacts and their naming/
verification relationships — the contract that ties the producer (GoReleaser on a tag) to the two
consumers (the install script and the in-app updater). Field names below describe the *shape and
invariants* of those artifacts, not code.

## Entity: Release

A published version on GitHub Releases.

| Field | Description | Rules / Invariants |
|-------|-------------|--------------------|
| `tag` | Version identifier | Semantic version prefixed `v`, e.g. `v0.1.0` (FR-007, R7). Immutable once published. |
| `archives` | Set of platform archives | Exactly one per supported platform; the full matrix MUST be present or the release is not "finished" (FR-003, FR-004, FR-008). |
| `checksums` | The checksum manifest asset | Exactly one per release; covers every archive (FR-005). |
| `notes` | Human-readable changelog | Generated, not hand-maintained. |
| `state` | Draft vs. published | A release missing any archive or the manifest MUST NOT be presented as published (FR-008). |

**State transitions**: `tag pushed → CI builds all archives + manifest → GitHub Release published`.
A failure before all artifacts + manifest exist leaves no "complete" release visible to users.

## Entity: Platform Archive

One downloadable bundle for a single OS/architecture pair.

| Field | Description | Rules / Invariants |
|-------|-------------|--------------------|
| `os` | Operating system | ∈ {`darwin`, `linux`} (FR-004). |
| `arch` | CPU architecture | ∈ {`amd64`, `arm64`} (FR-004). |
| `filename` | Asset name | `switchboard_<os>_<arch>.tar.gz` — stable, script-constructible (FR-006, R5). |
| `contents` | Bundled files | MUST contain **both** `sxb` and `sxbd`; also bundles `LICENSE`/`README` (FR-003). |
| `binary_version` | Version stamped into binaries | Equals the release `tag` (not a dev placeholder) (FR-007). |

**Relationships**: belongs to one `Release`; referenced by exactly one line in the `Checksum
Manifest`; selected by the `Install Script` and the in-app updater via (`os`,`arch`).

## Entity: Checksum Manifest

Integrity list used before any install/replace.

| Field | Description | Rules / Invariants |
|-------|-------------|--------------------|
| `filename` | Asset name | `checksums.txt`, beside the archives in the same release (R4). |
| `algorithm` | Hash algorithm | SHA-256 (FR-005, R4). |
| `entries` | `<hex>␠␠<archive-name>` lines | One entry per archive; format consumable by `sha256sum -c` / `shasum -a 256 -c` and by the updater's parser. |

**Relationships**: one per `Release`; each entry maps 1:1 to a `Platform Archive`. Consumers MUST
verify the downloaded archive against its entry BEFORE installing and abort on mismatch (FR-011,
SC-003).

## Entity: Install Script

The single hosted `curl | sh` bootstrap.

| Field | Description | Rules / Invariants |
|-------|-------------|--------------------|
| `source_url` | Where users curl it from | The repo's raw `install.sh` on the default branch (FR-009). |
| `detected_os` / `detected_arch` | Resolved platform | Mapped from `uname` to the archive `os`/`arch` vocabulary; unsupported values abort (FR-010, FR-015). |
| `target_version` | Which release to install | Latest by default; overridable to a pinned `tag` (FR-013, SC-006). |
| `install_dir` | Where binaries land | On-`PATH` dir when writable, else elevate, else per-user fallback; warn if off `PATH` (FR-012). |
| `inputs` (env knobs) | `SWITCHBOARD_VERSION`, `SWITCHBOARD_INSTALL_DIR`, `SWITCHBOARD_BASE_URL` | Documented overrides (pin version, custom dir, mirror/test base). See install-script contract. |

**Behavioral invariants**: verify-before-install (FR-011); idempotent over-install as the update
path (FR-014); actionable failure on unsupported platform / missing release / missing tooling /
missing platform archive (FR-015); post-install guidance incl. per-host install note (FR-016).

## Shared contract: producer ↔ consumers

The **asset-naming + checksum format** is the single contract three parts must agree on:

- **Producer**: GoReleaser `archives.name_template` = `{{.ProjectName}}_{{.Os}}_{{.Arch}}` and its
  `checksum` block.
- **Consumer 1**: `install.sh` constructs `switchboard_${os}_${arch}.tar.gz` and greps
  `checksums.txt`.
- **Consumer 2**: `src/libs/switchboard-update/update.go` — `AssetName(os,arch)` and `Fetch()`
  (SHA-256 verify).

Changing the name pattern or checksum format is a breaking change to installs **and** in-app
updates simultaneously (FR-017); it MUST stay in lockstep across all three (validated in
quickstart.md).
