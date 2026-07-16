# Feature Specification: Sandbox Refresh & Agent Kits

**Feature Branch**: `004-sandbox-refresh-and-kits`

**Created**: 2026-07-16

**Status**: Implemented

**Input**: User description: "We need to add a refresh sandbox command for users on the sandbox list page. This command, when run, should stop the sandbox, re-copy the selected repositories anew and then restart the sandbox. This is obviously a destructive action so there needs to be a warning dialog before the action processes. We need to be able to create and update docker agent kits directly from the sandbox list page. Most importantly we need to be able to specify the kit commands section. These kits need to be attachable to a sandbox during the creation step (with `sbx run --kit <kit-source> claude`) OR attachable while running (with `sbx kit add <sandbox-name> --kit <kit-source>`)."

## User Scenarios & Testing *(mandatory)*

A developer's sandbox workspace drifts: the agent has churned through the repo, left build
artifacts and half-finished edits behind, and the upstream sources have moved on. Rather than
destroying the sandbox and paying the full setup cost again, they **refresh** it — the workspace is
rebuilt from the recorded sources while the container's installed packages and agent history
survive. Separately, the setup a sandbox needs (tools, startup daemons, config files, network
rules) stops being something each developer redoes by hand: they author an **agent kit** once in
the TUI and attach it to sandboxes, either at creation or to one already running.

### User Story 1 - Refresh a drifted sandbox (Priority: P1)

A developer's sandbox workspace has diverged from its sources. They press `F` on the sandbox list,
read a dialog naming the sandbox and the exact repositories that will be re-copied, and confirm.
The sandbox stops, its workspace is deleted and rebuilt from the recorded sources, and it comes
back up on the same container with its installed packages intact.

**Why this priority**: This is the headline capability. It is also irreversible, so the
confirmation is part of the story, not a nicety.

### User Story 2 - Author a kit and attach it at creation (Priority: P1)

A developer presses `K`, creates a kit, fills in its install commands (and, as needed, startup
commands, init files, network rules, environment, credentials), and saves. Launching a new
sandbox, they press `K` in the wizard, select the kit, and launch. The sandbox comes up with the
kit applied.

### User Story 3 - Attach a kit to a running sandbox (Priority: P2)

A developer realises a running sandbox is missing a tool. They press `A` on its row, pick a kit,
and confirm a dialog explaining that the sandbox will restart (preserving VM state) and that the
kit cannot later be removed. The kit is applied.

## Requirements *(mandatory)*

### Refresh

- **FR-030**: The client MUST offer a per-sandbox refresh that deletes the sandbox's retained
  workspace copy, re-seeds it from the sandbox's recorded sources using its recorded seeding mode,
  and returns the sandbox to running on the SAME container, so installed packages, images and agent
  history survive. It MUST stream copy progress and sbx output. The workspace MUST be deleted
  rather than copied over: the duplicator creates symlinks with `os.Symlink` (which fails `EEXIST`
  on a second pass) and never deletes, so a copy-over would yield a union of the old and new trees
  rather than the fresh copy requested. The workspace marker and agent hooks MUST be re-injected,
  since the wipe destroys them. Refresh MUST refuse a sandbox whose workspace lies outside the
  controlled folder, or which has no recorded sources. A failure MUST leave the sandbox in ERROR
  with the cause recorded, never silently RUNNING over a half-built workspace.
- **FR-031**: The client MUST gate refresh behind an explicit confirmation naming the sandbox, the
  repositories to be re-copied, and the consequence (uncommitted work in the workspace is lost).
  Cancel MUST be the default: unrecognised keys MUST NOT be read as consent. The destructive
  refresh MUST NOT share a key with the non-destructive list reload.

### Agent kits

- **FR-032**: Kits MUST be attachable at sandbox creation, rendered as one `--kit <source>` per
  kit on the create invocation, in the author's selection order (sbx composes stacked kits in the
  order given). Kit sources MUST support both client-authored kits and external references (local
  path, `.zip`, `git+` URL, OCI ref). Attached kit sources MUST be persisted on the sandbox and
  replayed if the container is later recreated — `--kit` is honoured only at creation, so a
  relaunch that dropped them would silently return a differently-provisioned sandbox.
- **FR-033**: Kits MUST be attachable to an already-created sandbox via `sbx kit add`. Because sbx
  restarts the sandbox internally to apply the kit, the daemon MUST end the sandbox's terminal
  session and drop its cached PTY around the operation — nothing else would report that the PTY
  died, and a stale one hands the next attach an immediate EOF. The client MUST confirm first,
  explaining the restart and that kits cannot be removed from a running sandbox.
- **FR-034**: The client MUST let a developer create and update kits from the sandbox list page,
  covering the kit `commands` section (install, startup, initFiles) and the network, environment,
  credentials, identity and agentContext sections. Kits MUST be validatable against the host `sbx`
  (`sbx kit validate`), with sbx's diagnostics surfaced verbatim. An abandoned edit MUST NOT mutate
  the stored kit.

## Key decisions

1. **Refresh keeps the container** (vs. destroy + recreate). Matches the requested
   "stop → re-copy → restart" and preserves installed packages/agent history. It degrades
   gracefully: the shared bring-up path already relaunches from the retained copy when sbx cannot
   resume a container.
2. **Kits are owned client-side**, like configs. The contract header states the client is the
   source of truth for configs/groups/known-hosts; daemon-owned kits would invert that and make a
   kit authored against host A invisible on host B. The client renders `spec.yaml` and ships it;
   the daemon only materializes it into a directory the local `sbx` can read.
3. **The wire payload is the rendered `spec.yaml`, not a structured mirror** of Docker's schema.
   Docker documents the kit schema as experimental and subject to change; keeping the payload
   opaque lets it evolve without a contract change, with the host `sbx kit validate` as the
   authority.
4. **Kits are stored as real `kits/<id>/spec.yaml` directories** (not TOML like configs), so the
   stored artifact is exactly what sbx consumes — hand-editable, shareable, committable, and
   directly `sbx kit validate`-able. YAML is emitted by `yaml.v3` rather than hand-rolled:
   install commands and initFile contents are arbitrary shell and multi-line text, where quoting
   and octal-vs-string mistakes would produce a spec that parses but means something else.
5. **The editor authors `kind: mixin` only.** A mixin extends an existing agent, which is what
   switchboard's sandboxes are; a `kind: sandbox` kit replaces the agent image and entrypoint
   wholesale. Attach one of those as an external source instead.
6. **Form values are read through bound pointers, not `huh.Form.GetString`.** huh syncs its
   key/value store only when a field blurs, so applying a form with `ctrl+s` while a field is
   focused would silently discard what the user just typed.

## Risks / to confirm against a real `sbx`

`sbx` is not installed in the development environment (the same residual risk 001 recorded as R6),
so the kit CLI surface is taken from Docker's documentation rather than observed:

- `sbx kit add <sandbox> <kit-source>` takes the kit source **positionally**. The original request
  proposed `sbx kit add <sandbox-name> --kit <kit-source>`; per the docs `--kit` is creation-only
  and is rejected against an existing sandbox with *"--kit can only be used when creating a new
  sandbox"*. `sbx run --kit <source> claude` (as requested) is correct for creation; this repo's
  runner uses `sbx create`, which also accepts `--kit`.
- `sbx kit validate <path>` is assumed to exit non-zero with diagnostics on stdout/stderr.

These are isolated to `SbxRunner.AddKit` / `SbxRunner.ValidateKit` / `kitFlags`, and each is pinned
by a test asserting the exact argv, so a drift in the real CLI shows up as a focused failure.

Docker's published kit-reference page and the `docker/sbx-kits-contrib` repository also disagree on
schema details (the repo carries both `schemaVersion: "1"` kits using `kind: agent`/`agent:`/
`memory:` and `schemaVersion: "2"` kits using `kind: sandbox`/`sandbox:`/`agentContext:`; sbx
v0.32.0 renamed the former to the latter and still accepts the old names with deprecation
warnings). This is precisely why the daemon does not parse the schema and `sbx kit validate` is the
authority.

## Constitution Check

Feature 004 adds no new deviations. Like 001–003 it is implemented in **Go**, deviating from the
constitution's TypeScript/pnpm/Biome/Vitest tooling; that deviation is recorded and justified in
001's Constitution Check and Complexity Tracking. The Rule V gates are honoured via the `Makefile`
equivalents (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`, `e2e`), and Rule VI's 90%
per-package coverage floor is met by every module. Rule VIII is honoured: the new
`SWITCHBOARDD_KIT_ROOT` variable is declared in the daemon's config schema and its `.env.example`
in lockstep, enforced by `env-check`.
