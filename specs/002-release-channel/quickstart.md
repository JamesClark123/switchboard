# Quickstart: Validate the GitHub-Only Release Channel

Runnable checks that prove the channel works end-to-end without publishing a real release. Maps
each check to the spec's success criteria. See [contracts/](./contracts/) and
[data-model.md](./data-model.md) for the invariants being verified.

## Prerequisites

- Go 1.26, `goreleaser` v2 on `PATH` (`go install github.com/goreleaser/goreleaser/v2@latest`).
- `shellcheck` (optional; CI installs it).
- Python 3 (only for the local file-server step) or any static HTTP server.

## 1. Release build is complete + verifiable (SC-002, SC-003, SC-007; FR-003/004/005/007)

Dry-run the whole release into `dist/` with no publish:

```sh
goreleaser check                                   # no HOMEBREW_TAP_GITHUB_TOKEN needed (brews: removed)
goreleaser release --snapshot --clean --skip=publish
```

> `goreleaser check` resolves the release repo from the `origin` git remote; in a checkout without
> one, add it (`git remote add origin https://github.com/jamesclark123/switchboard.git`) before
> running `check`. `--snapshot` builds and checksums all four archives regardless.

Expect and verify:

- `dist/switchboard_darwin_amd64.tar.gz`, `_darwin_arm64`, `_linux_amd64`, `_linux_arm64` all exist
  (four platforms — SC-007).
- Each archive contains **both** binaries:
  `tar tzf dist/switchboard_linux_amd64.tar.gz` lists `sxb` and `sxbd` (FR-003).
- `dist/checksums.txt` exists with one line per archive (FR-005).
- Version is stamped: `./dist/sxb_linux_amd64_v1/sxb version` prints the snapshot version, and a
  plain `go build ./src/apps/switchboard-tui/cmd/sxb` prints `0.1.0-dev` (FR-007, R7).

**After Homebrew removal**, `goreleaser check` must pass with **no** `brews`/token reference and no
`HOMEBREW_TAP_GITHUB_TOKEN` needed (FR-001, FR-019, SC-005).

## 2. Install end-to-end, verified (SC-001, SC-003; FR-009–FR-012)

Serve the snapshot `dist/` as if it were a release, then install from it via the base-URL override:

```sh
python3 -m http.server 8799 --directory dist &   # stand-in for the release download base
SWITCHBOARD_BASE_URL=http://127.0.0.1:8799 \
  SWITCHBOARD_INSTALL_DIR="$(mktemp -d)/bin" \
  sh install.sh
```

Expect: "verifying checksum" then both binaries installed; `sxb version` and `sxbd version` run
(SC-001). Confirm the off-`PATH` warning appears for the temp dir (FR-012).

## 3. Corrupt download is rejected (SC-003; FR-011)

Tamper with a served archive (or its checksum) and re-run step 2. Expect a checksum-mismatch abort
with a non-zero exit and **no** binaries written to the install dir.

## 4. Update = same command; pin a version (SC-004, SC-006; FR-013/014)

- Re-run step 2 against a newer snapshot and confirm the installed binaries report the newer
  version (idempotent over-install — FR-014).
- `SWITCHBOARD_VERSION=v0.0.0-SNAPSHOT-... sh install.sh` installs exactly that version (FR-013,
  SC-006). (Against real releases, use a real tag.)

## 5. Installer robustness (FR-015)

- Syntax: `sh -n install.sh`. Lint: `shellcheck install.sh` (advisory).
- Force an unsupported platform path and a missing-archive path (point `SWITCHBOARD_BASE_URL` at an
  empty server) and confirm each aborts with an actionable message and installs nothing.

## 6. Producer ↔ consumer lockstep (SC-004; FR-006, FR-017)

Confirm the in-app updater consumes the same assets the channel produces:

```sh
cd src/libs/switchboard-update && go test ./...
```

`AssetName(os,arch)` must equal `switchboard_<os>_<arch>.tar.gz` and `Fetch()` must verify against
`checksums.txt` — the same names/format from step 1 (the shared contract in
[contracts/release-assets.md](./contracts/release-assets.md)).

## 7. Docs reflect the single channel (FR-018, FR-019)

Grep the README: the curl one-liner, the version-pin + custom-dir knobs, the remote-host install
note, the macOS manual-download `xattr` caveat, and the releasing/tagging instructions are present;
**no** Homebrew install/updating instructions remain (`grep -i brew README.md` finds only, at most,
a note that brew is intentionally not used).

## Full-gate regression

`make all` (fmt-check, vet, lint, test) and `make cover` (≥90% per module) stay green — the version
subcommands live in coverage-excluded `cmd/` and the shared `switchboard-update` contract is unit-
tested.
