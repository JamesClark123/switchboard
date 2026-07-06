# Contract: Release Assets & Trigger

The stable interface between the release producer (GoReleaser on a tag) and both consumers
(the install script and the in-app updater). Breaking any invariant here breaks installs and
in-app updates simultaneously.

## Trigger

- **Input**: a pushed git tag matching `v*` (semantic version, e.g. `v0.1.0`).
- **Effect**: exactly one GitHub Release is created for that tag, containing the full asset set
  below. No other trigger publishes a release; no manual upload step is required (FR-002).
- **Atomicity**: if the build fails before all archives + the manifest exist, no release is
  presented to users as complete (FR-008).

## Asset set (per release)

For every platform in the matrix `{darwin, linux} × {amd64, arm64}`:

| Asset | Name pattern | Contents |
|-------|--------------|----------|
| Platform archive | `switchboard_<os>_<arch>.tar.gz` | Both `sxb` and `sxbd`, plus `LICENSE`, `README.md` |

Plus exactly one integrity manifest for the release:

| Asset | Name | Format |
|-------|------|--------|
| Checksum manifest | `checksums.txt` | One line per archive: `<sha256-hex><two spaces><archive-name>` |

**Invariants**
- `<os>` ∈ {`darwin`, `linux`}; `<arch>` ∈ {`amd64`, `arm64`} — lowercase, exactly these tokens.
- The name pattern is **stable** and script-constructible (no version, date, or commit in the
  archive filename) so `…/releases/latest/download/switchboard_<os>_<arch>.tar.gz` resolves.
- Every archive has exactly one matching line in `checksums.txt`.
- Each binary, when run with `version`, reports the release tag (not `*-dev`).

## Stable download URLs

- Latest: `https://github.com/<owner>/<repo>/releases/latest/download/<asset>`
- Pinned: `https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>`

Both `install.sh` and `switchboard-update` MUST rely only on these URL shapes and the name pattern
above — never on scraping the Releases HTML (FR-006, FR-017).

## Consumers that MUST stay in lockstep

1. `install.sh` — builds `switchboard_${os}_${arch}.tar.gz`, downloads it + `checksums.txt`,
   verifies SHA-256, extracts both binaries.
2. `src/libs/switchboard-update/update.go` — `AssetName(goos, goarch)` MUST equal the pattern
   above; `Fetch()` MUST verify against `checksums.txt` before applying.

## Explicitly out of contract

- No Homebrew formula/cask, no apt/rpm/other package assets (FR-001, FR-019).
- No signature/notarization assets (SHA-256 manifest is the integrity mechanism; R4).
