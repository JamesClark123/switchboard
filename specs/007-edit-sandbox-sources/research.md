# Phase 0 Research: Edit Sandbox Sources (Add & Remove Folders)

Decisions R1–R8 resolve every unknown in the plan's Technical Context. The spec's option-A
premise — edit the seeded folder set **in place, on a live sandbox, with no container work** —
turns out to be implementable with **zero new `sbx` surface**: no new `Runner` methods, no new
argv to reconcile. The one load-bearing external assumption is R1, and it is an assumption this
codebase already ships on.

## R1 — Live visibility of workspace edits inside a running sandbox

**Decision**: Rely on the sandbox observing its workspace copy **live** — the daemon edits
`<workspace>/` on the host and a running container sees the new/removed folder without any
restart, because `sbx create … <workspacePath>` mounts the workspace root (direct mode) rather
than snapshotting it. Adding or removing a *child* of the mounted root never disturbs the mount
itself.

**Rationale**: This is not a new bet — it is the bet features 003 and 005 already shipped on and
that has survived contact with the real runtime:

- **Escape hatch (005)** runs whole commands **on the host, inside the bind-mounted
  `workspace_path`, while the sandbox is running**, and its entire value is that the file effects
  (a `pnpm install`'s `node_modules`, a build's artifacts) are immediately visible to the agent
  inside the sandbox.
- **Hooks and the workspace marker (003/004)** are re-injected host-side by `Manager.Refresh`
  and read by the in-sandbox agent afterwards.
- The daemon never passes `--clone`-style flags at create: `SbxRunner.Launch` maps to
  `sbx create --name <name> claude <workspacePath>` and the workspace is handed over as a
  directory, exactly once, at creation (`runner.go`).

The distinction from `Manager.Refresh` matters and is why refresh stops the container but this
feature does not: refresh `os.RemoveAll`s **the mount root itself** out from under the container,
whereas this feature only ever creates or deletes **children** of that root — the mount stays
intact throughout.

**Residual risk (reconciliation)**: `sbx` is still not installed in the dev environment (the
same R6-class residual as 001/004/005/006). If a real runtime turns out to snapshot rather than
mount, every daemon-side mechanic in this feature still works and the degradation is exactly the
spec's stated contingency — "visible after next restart". Quickstart Scenario 0 makes this the
**first** thing to verify against a real `sbx`.

**Alternatives considered**: (a) stop → mutate → `bringUp` (research option B) — pays a
container restart, kills the terminal session/PTY and the agent's momentum on every add, for a
safety property option A doesn't need (the mount is never disturbed); kept only as the documented
degradation path. (b) `sbx exec`-side manipulation (copy inside the container) — adds a second
copy of the data, depends on the **least**-verified argv in the codebase (006 R3), and breaks
"originals never modified" symmetry. Rejected.

## R2 — Add mechanics: stage inside `.switchboard`, then rename into place

**Decision**: An add never copies directly to its final path. Per operation:

1. Validate everything up front (R6), then run the resource gate (R8).
2. Copy each new source into a staging area **inside the sandbox's own workspace**:
   `<workspace>/.switchboard/staging/<basename>` — via the existing `duplicate.CopyAll`
   (duplicate mode) or `Runner.CloneRepo` (clone mode), streaming `duplicate.Progress` as today.
3. Only when **every** selected source has staged successfully, `os.Rename` each
   `staging/<basename>` to `<workspace>/<basename>` (same filesystem by construction, so this is
   the cheap atomic directory move).
4. Update the record (append `SourceRef`s), persist, emit.

On any failure — copy error, disk full, context cancelled, a rename losing a race with a
newly-appeared directory — delete the staging area (and any already-renamed folders from **this
operation**, which are fresh copies and safe to delete) and return the error with the record
untouched. On daemon startup, leftover `staging/` directories are purged best-effort (a crash
mid-add must not strand gigabytes).

**Rationale**: The agent is *live* inside this workspace (that is the whole point of option A). A
direct copy to `<workspace>/<basename>` would expose a half-copied tree to a running agent for
the entire multi-minute copy of a large repo — an agent that starts reading it mid-copy sees a
corrupt project. Staging under `.switchboard/` keeps the partial tree in the directory switchboard
already owns and agents already ignore (the marker, hooks and escape-hatch wrapper live there),
guarantees same-filesystem rename (staging *is* inside the workspace), scopes crash-cleanup to the
sandbox, and makes FR-058's atomicity trivial: the folder either appears complete or never
appears. It also composes with `duplicate`'s hard constraint — `copyTree` symlinks fail `EEXIST`
and nothing ever deletes (the 004 lesson), so the destination must be fresh; a rename target that
unexpectedly exists fails loudly instead of unioning trees.

**Alternatives considered**: (a) copy directly + `RemoveAll` on failure — simplest, but exposes
partial trees to the live agent and lets a slow copy be half-visible; rejected. (b) stage under
`<workspaceRoot>/.staging-<id>` (sibling of workspaces) — same-filesystem too, but pollutes the
controlled folder whose children are, by convention, sandbox workspaces named by sandbox name
(`Manager.List` prunes by workspace existence; `resolveName` collisions become possible against
stray staging dirs). Staging inside the sandbox's own `.switchboard/` has none of those hazards.

## R3 — Remove mechanics: guarded `RemoveAll` on the child, record after disk

**Decision**: Removal maps each selected recorded source to its workspace child
`<workspace>/<basename(path)>`, re-verifies the child is `within()` the controlled workspace
root, then `os.RemoveAll`s it and drops the `SourceRef` from the record in the same operation. A
child already missing on disk is treated as already gone: the record is still cleaned and the
operation succeeds (FR-063). The sandbox's state is never changed by a removal — no stop, no
PTY teardown, no ERROR.

Ordering within a multi-folder removal: delete + drop record per folder, folder by folder. If a
deletion fails partway through a batch (e.g. EACCES from a root-owned file a container process
created), the folders already deleted **stay dropped from the record** (record == disk is the
invariant that must hold, per FR-064), the failed folder stays *in* the record, and the error
names it. There is deliberately no attempt to "roll back" a deletion — un-deleting is impossible,
so the record follows the disk, never the wish.

**Rationale**: Deleting a child of the mounted root is mechanically safe for the container (the
mount is untouched — R1); on the host side `RemoveAll` unlinks regardless of open FDs on
Linux/macOS, so in-sandbox processes holding files see them vanish, which is exactly the
consequence the FR-060 confirmation spells out. The `within()` guard reuses the same defense
`Destroy`/`Refresh` already apply before any `RemoveAll` under the controlled folder.

**In-flight escape-hatch runs are deliberately not a removal blocker**: a run executing with its
resolved working dir inside the removed folder will fail and report through its own bounded,
observable lifecycle (005's executor caps + status) — the same treatment as any other process
working in the folder. Only **declared services** get a hard guard (R4/FR-062), because a running
service is a durable, developer-started resource with a port attached, not a bounded run.

**Alternatives considered**: trash/undo (move to `.switchboard/trash/`) — doubles disk usage for
the exact "huge folder" case that motivates removal, and the spec's confirmation flow already
owns the irreversibility; rejected. Rolling the record back on partial batch failure — would
recreate the one corruption mode the spec forbids (record claiming a folder that is gone);
rejected.

## R4 — Per-sandbox operation serialization (FR-066) and the service guard (FR-062)

**Decision**: `internal/sandbox` gains a small per-sandbox **operation latch** (mutex-guarded
`map[sandboxID]string` of in-flight operation labels; try-acquire semantics — never blocking).
`AddSources`, `RemoveSources`, **and `Refresh`** acquire it for their duration; a second
seed-mutating operation on the same sandbox is refused immediately with
`FAILED_PRECONDITION: <label> in progress for this sandbox`. `Launch` needs no latch (it mints a
fresh sandbox id and a fresh workspace); `AddKit` is untouched (it never edits the workspace).

The **service guard** runs in the gRPC layer (which already holds both the `portforward`
supervisor and the manager): before calling `RemoveSources`, it walks
`Instances().ActiveBySandbox(id)`, joins each active instance to its declaration in
`Sandbox.services`, and refuses the removal if any active instance's workspace-relative
`working_dir` equals a selected folder name or sits beneath it — naming the service and the
remedy ("stop it first"). A `working_dir` of the workspace root does **not** block (the root is
not being deleted).

**Rationale**: Today's `Manager` has no cross-operation exclusion — two concurrent `Refresh`es
interleave only by luck of timing. Edits make that gap load-bearing (an add renaming into a
workspace a refresh is simultaneously `RemoveAll`ing is exactly the interleaving FR-066 forbids),
so the latch covers refresh too. Try-acquire (not blocking) is the UX the spec asks for: the
second operation is *refused with a message*, not silently queued behind a 30-minute copy. The
guard lives grpc-side because that is where the supervisor is already wired (`server.go`), keeping
`Manager` free of instance-store knowledge — mirroring how 005/006 resolution happens at the RPC
boundary while the manager owns disk + record.

**Alternatives considered**: a global manager mutex — serializes *all* sandboxes' operations
behind one multi-GB copy; rejected. Blocking acquire — turns a user-visible "busy" into an
invisible queue with a surprise execution later; rejected. Guard inside `Manager` via an injected
checker func — workable (the injector pattern exists) but adds indirection for a check with
exactly one caller; rejected for now.

## R5 — RPC shape: streamed add, unary remove, no new event machinery

**Decision**: Two additive RPCs on the existing `Switchboard` service
(see [contracts/switchboard-edit-sources.proto](./contracts/switchboard-edit-sources.proto)):

- `AddSandboxSources(AddSandboxSourcesRequest) returns (stream LaunchProgress)` — reuses the
  existing `LaunchProgress` stream shape verbatim (`copy` for duplication progress, `log_line`
  for clone output, `blocked` for the resource gate, terminal `done` carrying the updated
  `Sandbox`), and the existing `progressSender` adapter in the gRPC layer. The request carries
  `override_resource_warning` exactly like `LaunchSandboxRequest`.
- `RemoveSandboxSources(RemoveSandboxSourcesRequest) returns (Sandbox)` — unary, like
  `StopSandbox`/`RenameSandbox`; deletion emits no incremental progress worth a stream, and the
  client applies a generous timeout for pathological trees (R8).

**No new Event arm, no new NotificationKind**: both operations end by persisting through
`store.Update`, and `Manager.emit` already fans the changed `Sandbox` out on
`Event.sandbox_changed` — which is precisely FR-067. Edits are developer-initiated actions, so
they are silent in the inbox, matching 006's "a successful start and a developer-initiated stop
are silent" precedent.

**Rationale**: `LaunchProgress` was designed as the shared shape for every seed-mutating stream
(launch, restart, refresh, kit-add all use it); an add is the same operation class, and reusing
it means the client-side stream consumption (`client.LaunchUpdate`) works unchanged. Imperative
add/remove (not a declarative `SetSandboxSources`) is the spec's Key Decision 2: distinct
confirmation semantics and per-operation partial-failure behavior (R2 rolls adds back; R3
follows the disk on removals — one declarative RPC would have to pick a single story for both).

**Alternatives considered**: declarative set-diff RPC — rejected above and in the spec. A
streamed remove — nothing to stream but a spinner; rejected for surface economy. Returning
`DestroyResponse`-style booleans from remove — the updated `Sandbox` is what every caller
actually wants (it *is* the new record); rejected.

## R6 — Validation: what an add refuses up front, and how a removal names its target

**Decision — add-side validation, all before any byte is copied** (FR-056):

| Check | Refusal |
|---|---|
| `sources` empty, or a `path` empty/relative | `INVALID_ARGUMENT` |
| source path does not exist / is not a directory (daemon-side `os.Stat`) | `INVALID_ARGUMENT` naming the path |
| basename collides with a **recorded** source's basename | `ALREADY_EXISTS` naming the folder |
| basename collides with an **on-disk** top-level workspace entry (agent-created dirs included — the rename would fail later anyway; fail it now) | `ALREADY_EXISTS` naming the folder |
| basename collides with another selection in the same request | `INVALID_ARGUMENT` |
| basename is `.switchboard` (reserved) or otherwise fails the same character/path-segment safety used elsewhere | `INVALID_ARGUMENT` |
| clone-mode sandbox and `is_repo == false` (re-verified daemon-side via the same `.git` probe `ListSourceCandidates` uses, not trusted from the client) | `FAILED_PRECONDITION` naming the folder |
| sandbox state is CREATING, DESTROYING, or ERROR | `FAILED_PRECONDITION` (ERROR sandboxes are recovered by refresh/destroy, not edited) |

**Decision — removal targeting**: the request carries **exact recorded `SourceRef.path` values**
(not basenames, not indexes). The daemon resolves each against `Sandbox.sources`; an unknown path
is `NOT_FOUND` naming it and nothing in the batch is deleted (all-or-nothing *validation*;
deletion then proceeds per R3). Removing all recorded sources is refused with
`FAILED_PRECONDITION` (FR-061) before anything is deleted.

**Rationale**: Paths are the identity the record already stores and the TUI already displays
(refresh's confirm lists `basename (path)`), and exact-match makes the RPC idempotence story
clean: retrying a partially-failed batch simply re-sends the survivors. Validating disk entries as
well as the record closes the gap between "recorded truth" and "actual truth" that an agent
creating `mylib/` by hand would otherwise open — the rename in R2 would fail anyway; failing
before a multi-GB copy is strictly better.

## R7 — Client surface: `S` on the sandbox list, one editor overlay, row-badged progress

**Decision**:

- **Key**: `S` ("sources") on the sandbox list. Verified free (taken today: `n C c h g v t T i s
  d K A E u F R r j k q # p a x space enter esc`); capital-letter management surfaces are the
  house pattern (`K` kits, `A` add kit, `E` runs, `F` refresh). It is adjacent to `s`
  (start/stop) on the keyboard only in spelling — a mis-press opens a read-only overlay, and
  every destructive path inside is separately confirmed, so the adjacency is harmless.
- **Overlay** (`ui/sources.go`, modeled on the services list + launch browser): lists the
  sandbox's recorded folders (`basename (path)`, repo marker). `a` enters the **existing launch
  filesystem browser** rooted at the owning host's launch root — same multi-select, same
  `is_repo` capture, browsing the *owning host's* filesystem via the existing `ListSources`
  client call (US4). `space` marks listed folders for removal; `d` opens the removal
  confirmation.
- **Removal confirm** reuses `confirm.go`'s `confirmState` (the FR-031 machinery: explicit verb,
  cancel-default, unrecognised keys ignored), naming the sandbox, each folder, and the
  uncommitted-work consequence (FR-060).
- **Progress**: an in-flight add registers in a per-sandbox in-flight map (keyed by sandbox id,
  like `launchInFlight` but attached to an *existing* row); the row shows an "adding <folder> —
  NN%" badge driven by the shared spinner tick, and the overlay (if open) shows the same. The
  sandbox row never leaves RUNNING/STOPPED — there is no fake CREATING state (FR-054). Client
  timeouts: adds use a 30-minute bound (the `refreshTimeout` class — multi-GB copies), removals
  10 minutes (a `RemoveAll` of a huge dependency tree is not instant either; both are package
  constants in `ui/sources.go`).
- **Daemon interface**: two new methods on `ui.Daemon` + `client` — `AddSources(ctx, id,
  sources, onUpdate)` (consumes the stream exactly as `Refresh` does, surfacing `blocked` as the
  existing resource-warning override flow) and `RemoveSources(ctx, id, paths)`.

**Rationale**: One overlay for both directions matches the spec's single "source editor"
narrative while keeping the destructive path behind its own confirm; reusing the launch browser
honors FR-053's "same browsing experience as launch" with near-zero new browsing code; reusing
the `blocked`/override flow gives FR-057 for free.

**Alternatives considered**: separate keys for add/remove on the list page — burns two keys and
splits one mental object ("this sandbox's folders") across two surfaces; rejected. A full-screen
editor like the kit editor — the folder list is one flat set, not a multi-section document;
rejected.

## R8 — Bounds, gates, and what this feature deliberately does NOT add

**Decision / inventory**:

- **Resource gate**: adds reuse `resources.Check(srcPaths, workspaceRoot)` exactly as
  `LaunchSandbox` does — duplicate mode only (clone sizes are unknowable pre-clone, same as
  launch), `blocked` + `override_resource_warning` round-trip (FR-057).
- **Constants, no new env vars**: staging dir name (`.switchboard/staging`), add timeout
  (client, 30 min), remove timeout (client, 10 min) — all package constants. The Rule VIII
  surface is untouched: `.env.example` files do not change and `env-check` is unaffected.
- **No new `sbx` surface**: zero new `Runner` methods, zero new argv to reconcile — the entire
  daemon-side mechanic is host filesystem work plus the existing record/emit machinery. The
  feature's only runtime assumption is R1's mount semantics.
- **No new packages**: daemon changes land in `internal/sandbox` (a `manager_sources.go` beside
  `manager.go`) + `internal/grpc`; client changes in `internal/ui/sources.go` +
  `internal/client`. New proto messages are additive; `Sandbox` itself gains **no** fields
  (`sources` already exists — it merely becomes mutable).
- **Registry**: no migration. `Sandbox.sources` is already persisted proto; older daemons simply
  reject the unknown RPCs (`Unimplemented`), which the client surfaces as-is — acceptable skew
  behavior given the `u` update fan-out keeps hosts converged (feature 002).

## Reconciliation risks

| # | Risk | Exposure | Handling |
|---|---|---|---|
| 1 | **R1: a real `sbx` snapshots the workspace instead of mounting it live** — the added folder would not appear in a running sandbox. | The feature's headline promise (FR-054). Daemon mechanics, record fidelity, and stopped-sandbox behavior are all unaffected. | First check in [quickstart.md](./quickstart.md) Scenario 0. Degradation is the spec's stated contingency: fall back to option B's stop→mutate→bring-up inside the *same* RPCs (no contract change), paying the restart only on runtimes that need it. |
| 2 | Container-created root-owned files inside a removed folder make `RemoveAll` fail with EACCES. | Partial removal; record correctly keeps the failed folder (R3). | Error names the folder; remedy is the same as `Destroy`'s existing exposure to this (out of scope to solve here — noted as shared behavior). |
| 3 | A staging purge racing a daemon restart mid-add strands a partial `staging/` dir. | Disk waste only — never a corrupt workspace (staged trees are outside the seeded set). | Best-effort purge of `*/.switchboard/staging` on daemon start (R2). |
