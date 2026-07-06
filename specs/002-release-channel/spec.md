# Feature Specification: GitHub-Only Release Channel

**Feature Branch**: `002-release-channel`

**Created**: 2026-07-05

**Status**: Draft

**Input**: User description: "We want to build a release channel as described in previous conversations using only github + install.sh. Build a spec document based around this"

## Overview

Switchboard ships two binaries — the TUI client (`sxb`) and the per-host daemon (`sxbd`) — that
must reach every machine a developer uses, including remote hosts the daemon runs on. This feature
defines **one** distribution channel: published GitHub Releases plus a `curl | sh` install script.
It is deliberately the *only* channel — no Homebrew tap, no OS package managers — so there is a
single, verifiable, self-update-compatible way to get and refresh the binaries on macOS and Linux.

The existing in-app updater (the TUI's "update client and all hosts" flow) consumes the assets this
channel produces; this spec covers the **channel** (how releases are produced, named, verified, and
installed), not the update orchestration itself.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Install with a single command (Priority: P1)

A developer on macOS or Linux installs both binaries by running one command. The command fetches
the latest published release for their operating system and processor architecture, confirms the
download is intact, and places `sxb` and `sxbd` on their system so both can be run by name.

**Why this priority**: Without a first-run install path there is no product in users' hands. This
is the minimum viable slice — everything else builds on releases existing and being installable.

**Independent Test**: On a clean macOS and a clean Linux machine (both amd64 and arm64), run the
install command and confirm both `sxb` and `sxbd` are present, runnable, and report a version.

**Acceptance Scenarios**:

1. **Given** a supported machine with no prior install, **When** the user runs the install command,
   **Then** the latest release's binaries for that OS/architecture are installed and both report
   their version when asked.
2. **Given** the install command is run, **When** the downloaded archive does not match the
   published checksum, **Then** the install aborts with a clear error and installs nothing.
3. **Given** the chosen install location is not writable, **When** the user runs the install
   command, **Then** it either elevates with permission or falls back to a per-user location, and
   tells the user if that location is not on their PATH.
4. **Given** an unsupported operating system or architecture, **When** the user runs the install
   command, **Then** it stops with a message naming what is supported.

---

### User Story 2 - Publish a release by tagging (Priority: P1)

A maintainer publishes a new version by marking a release point in the project's history. From that
single action, ready-to-install artifacts for every supported platform appear on the project's
GitHub Releases page automatically, with no manual building, uploading, or checksum bookkeeping.

**Why this priority**: This is a frequently-updated product; releasing must be a one-step,
repeatable action or new versions will not ship reliably. The install and update stories are
worthless without a dependable supply of releases.

**Independent Test**: Create a version tag on a test repository and confirm that, with no further
manual steps, a GitHub Release appears containing per-platform archives (each holding both
binaries), a checksum manifest, and release notes.

**Acceptance Scenarios**:

1. **Given** the maintainer creates a version tag, **When** the release process runs, **Then** a
   GitHub Release is published containing an archive for each supported OS/architecture, each
   archive containing both `sxb` and `sxbd`.
2. **Given** a release is published, **When** a user inspects an installed binary's version,
   **Then** it reports the released version (not a placeholder development version).
3. **Given** a release is published, **When** any archive is downloaded, **Then** a companion
   checksum manifest lets the downloader verify integrity before use.
4. **Given** the release process fails partway, **When** the maintainer inspects the result,
   **Then** no partial or unverifiable release is presented to users as complete.

---

### User Story 3 - Update existing installs to the latest release (Priority: P2)

An existing user (and each host their daemon runs on) moves to the newest version. Because there is
a single channel, updating uses the same source of truth as installing: re-running the install
command fetches and installs the latest release over the current one, and the in-app updater draws
from the same published assets.

**Why this priority**: The product is updated frequently, so a low-friction path to the latest
version — for the client and every connected host — is essential, but it depends on install and
publish already working.

**Independent Test**: With an older version installed, publish a newer release, re-run the install
command, and confirm the binaries are now the newer version; confirm the in-app updater installs
the same version from the same assets.

**Acceptance Scenarios**:

1. **Given** an older version is installed, **When** the user re-runs the install command, **Then**
   the binaries are replaced with the latest released version.
2. **Given** a specific version is requested, **When** the user runs the install command pinned to
   that version, **Then** exactly that version is installed.
3. **Given** the in-app updater runs, **When** it fetches release assets, **Then** it uses the same
   GitHub Release archives and checksums this channel publishes (no separate source).
4. **Given** a remote host the user has never connected to, **When** it needs updating, **Then** the
   documented path is to run the same install command on that host.

---

### Edge Cases

- **Checksum mismatch or truncated download**: installation must abort and leave any existing
  install untouched — never install an unverified binary.
- **No published release yet**: the install command must fail with an actionable message rather than
  installing nothing silently or erroring cryptically.
- **Missing download tooling** (no `curl`/`wget`) or **missing checksum tooling**: fail with a clear
  message naming what is required.
- **Partial platform matrix**: if a release is missing the archive for the user's platform, the
  install command must say so clearly rather than downloading the wrong one.
- **Manual browser download on macOS**: a `.tar.gz` downloaded through a browser may be quarantined
  by the OS; the documentation must tell users how to clear that (installs via the script are not
  affected).
- **Re-running install repeatedly**: must be safe and idempotent (an over-install, not a duplicate).

## Requirements *(mandatory)*

### Functional Requirements

#### Release production

- **FR-001**: The project MUST publish releases as GitHub Releases; no other distribution channel
  (Homebrew tap, OS package repositories) is part of this feature.
- **FR-002**: Publishing a release MUST be triggered by a single maintainer action (creating a
  version tag) and complete without further manual build, upload, or checksum steps.
- **FR-003**: Each release MUST include a downloadable archive for every supported platform, and
  each archive MUST contain BOTH `sxb` and `sxbd`.
- **FR-004**: Supported platforms MUST be macOS and Linux on both 64-bit Intel/AMD and 64-bit ARM
  architectures (four platform archives per release).
- **FR-005**: Each release MUST include a checksum manifest covering every archive so downloads can
  be integrity-verified.
- **FR-006**: Release archive filenames MUST follow a stable, predictable pattern keyed on operating
  system and architecture so a script can construct the download name without scraping the page.
- **FR-007**: Installed binaries MUST report the released version (and MUST NOT report a placeholder
  development version) when queried.
- **FR-008**: A release that fails to produce the complete, verifiable set of artifacts MUST NOT be
  presented to users as a finished release.

#### Installation

- **FR-009**: Users MUST be able to install both binaries with a single copy-paste command that
  requires no pre-installed toolchain beyond commonly available download and checksum utilities.
- **FR-010**: The install command MUST detect the user's operating system and architecture and
  select the matching release archive automatically.
- **FR-011**: The install command MUST verify the downloaded archive against the release's checksum
  manifest BEFORE installing, and MUST abort without installing on any mismatch.
- **FR-012**: The install command MUST install to a location on the user's PATH when possible,
  elevate privileges only when necessary, otherwise fall back to a per-user location, and warn when
  the chosen location is not on the PATH.
- **FR-013**: The install command MUST default to the latest published release and MUST allow the
  user to request a specific version instead.
- **FR-014**: Re-running the install command MUST upgrade an existing install in place (idempotent
  over-install), providing the update path for both the client and any host.
- **FR-015**: The install command MUST fail with a clear, actionable message on unsupported
  platforms, missing releases, missing required tooling, or a missing platform archive — never
  installing an incorrect or unverified binary.
- **FR-016**: After a successful install, the command MUST tell the user how to start the daemon and
  launch the client, and MUST note that each remote host running the daemon must be installed
  separately.

#### Channel consistency & documentation

- **FR-017**: The in-app updater MUST consume the same GitHub Release archives and checksum manifest
  that this channel publishes; there MUST be exactly one source of truth for release assets.
- **FR-018**: Project documentation MUST present this channel as the single install/update method,
  document the pin-a-version and custom-location options, describe the remote-host install path, and
  explain the macOS manual-download quarantine caveat.
- **FR-019**: Documentation MUST NOT instruct users to install via Homebrew or an OS package
  manager, and MUST NOT reference obtaining binaries from any source other than GitHub Releases.

### Key Entities *(include if feature involves data)*

- **Release**: A published version identified by a version tag, containing the full set of platform
  archives, a checksum manifest, and human-readable release notes.
- **Platform Archive**: A single downloadable bundle for one operating-system/architecture pair,
  containing both binaries and named by a stable OS/architecture pattern.
- **Checksum Manifest**: A file listing the integrity hash of every archive in a release, used by
  the installer and the in-app updater to verify a download before use.
- **Install Script**: The single hosted command that detects the platform, downloads and verifies
  the matching archive, and installs both binaries.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A new user can go from nothing installed to both binaries runnable with **one command
  and no manual steps** on any of the four supported platforms.
- **SC-002**: Publishing a new version requires exactly **one maintainer action** (tagging), after
  which installable, verified artifacts for all four platforms are available with no manual work.
- **SC-003**: 100% of installs verify integrity before installing; a corrupted or tampered download
  results in **zero** binaries installed.
- **SC-004**: Updating to the latest version is the **same single command** as the initial install
  (re-run), and the in-app updater installs the **identical version** from the identical assets.
- **SC-005**: The channel depends on **no third-party package manager or hosting** beyond GitHub;
  removing Homebrew from the project introduces no gap in install or update coverage.
- **SC-006**: A maintainer can pin and install any previously published version by supplying its
  version identifier to the install command.
- **SC-007**: Every supported platform is covered by exactly one archive per release, and each
  archive contains both binaries (verified by inspecting any published release).

## Assumptions

- **Target platforms**: macOS and Linux on 64-bit Intel/AMD and 64-bit ARM. Windows and 32-bit
  architectures are out of scope for this feature.
- **Hosting**: The project is hosted on GitHub and GitHub Releases is available as the artifact host;
  no separate download server or CDN is introduced.
- **Integrity model**: Integrity is provided by a SHA-256 checksum manifest published with each
  release. Cryptographic signing/notarization (e.g., signed manifests, Apple notarization) is out of
  scope for this feature; the macOS quarantine caveat is handled by documentation only.
- **Client environment**: Users have commonly available command-line download tooling (`curl` or
  `wget`) and a checksum utility (`sha256sum` or `shasum`) — standard on macOS and mainstream Linux.
- **Homebrew removed**: Any previously planned Homebrew tap (and its one-time tap-repo/token setup)
  is explicitly out of scope and is removed from documentation; this is the single channel.
- **Consumer already exists**: The in-app self-update flow already exists and only needs the assets
  this channel publishes; re-specifying update orchestration is out of scope here.
- **Version identity**: Releases are identified by semantic-version tags, and the released version is
  stamped into the binaries at build time.
