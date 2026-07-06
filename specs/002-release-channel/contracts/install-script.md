# Contract: Install Script (`install.sh`)

The single hosted `curl | sh` bootstrap. Defines its invocation, inputs, behavior, and exit
semantics. Consumes the [release-assets](./release-assets.md) contract.

## Invocation

```sh
curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/install.sh | sh
```

Must run under a POSIX `sh` (no bashisms required) and need no toolchain beyond a downloader
(`curl` or `wget`) and a checksum tool (`sha256sum` or `shasum`) (FR-009).

## Inputs (environment overrides)

| Variable | Default | Meaning |
|----------|---------|---------|
| `SWITCHBOARD_VERSION` | latest release | Pin a specific tag, e.g. `v0.1.0` (FR-013, SC-006). |
| `SWITCHBOARD_INSTALL_DIR` | `/usr/local/bin`, else `~/.local/bin` | Target directory for the binaries (FR-012). |
| `SWITCHBOARD_BASE_URL` | GitHub Releases download base | Override the download base (mirrors/testing); enables offline validation (R6, quickstart). |

## Platform detection

- `uname -s`: `Darwin`→`darwin`, `Linux`→`linux`; anything else → abort with a supported-list
  message (FR-010, FR-015).
- `uname -m`: `x86_64`/`amd64`→`amd64`, `arm64`/`aarch64`→`arm64`; anything else → abort (FR-010,
  FR-015).

## Behavior (happy path)

1. Resolve the download base (pinned tag, base override, or `…/latest/download`).
2. Download `switchboard_<os>_<arch>.tar.gz` and `checksums.txt` to a temp dir.
3. **Verify** the archive's SHA-256 against its `checksums.txt` entry; abort on mismatch **before**
   touching the install dir (FR-011, SC-003).
4. Extract and install `sxb` and `sxbd` (mode `0755`) into the install dir — elevating with `sudo`
   only if the default dir isn't writable, else falling back to the per-user dir (FR-012).
5. Warn if the install dir is not on `PATH` (FR-012).
6. Print next steps: how to start the daemon (`sxbd serve --boot`/`--watch`) and launch `sxb`, plus
   the note that each remote host running the daemon must be installed there too (FR-016).

Re-running performs an in-place over-install (idempotent) — the update path for client and hosts
(FR-014).

## Exit semantics

| Condition | Result |
|-----------|--------|
| Success | Both binaries installed and runnable; exit 0. |
| Unsupported OS/arch | Abort, message names what is supported; non-zero exit; nothing installed (FR-015). |
| Missing release / missing platform archive | Abort with actionable message; nothing installed (FR-015). |
| Checksum mismatch or truncated download | Abort; **zero** binaries installed; existing install untouched (FR-011, SC-003). |
| Missing `curl`/`wget` or `sha256sum`/`shasum` | Abort naming the required tool (FR-015). |

## Non-goals

- Does not manage a service/daemon lifecycle (that is `sxbd serve --boot/--watch`).
- Does not install from, or mention, Homebrew or any OS package manager (FR-019).
- Does not perform signature verification (SHA-256 only; R4).
