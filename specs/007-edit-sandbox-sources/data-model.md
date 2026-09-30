# Phase 1 Data Model: Edit Sandbox Sources (Add & Remove Folders)

Derived from [spec.md](./spec.md) Key Entities + FR-053–FR-067, with mechanics fixed by
[research.md](./research.md) R2/R3/R6. This feature introduces **no new persisted entity and no
new proto field on `Sandbox`** — it changes the *mutability contract* of an existing field
(`Sandbox.sources`) and defines the two transient operations that mutate it.

## Entity map

| Entity | Kind | Stored | Change in 007 |
|--------|------|--------|----------------|
| `SourceRef` | existing message | inside `Sandbox.sources` (bbolt) | unchanged shape; gains post-launch lifecycle |
| `Sandbox.sources` | existing field | bbolt registry | **launch-frozen → mutable** via exactly two operations |
| Add operation | transient (in-memory) | never | new |
| Remove operation | transient (in-memory) | never | new |
| Operation latch | in-memory map | never | new (also acquired by `Refresh`) |

## SourceRef (existing, unchanged shape)

| Field | Type | Notes |
|-------|------|-------|
| `path` | string | Host-side absolute origin path. **Identity for removal** (exact match, R6) |
| `is_repo` | bool | Captured at selection; **re-verified daemon-side** on add for clone-mode sandboxes (R6) |

Derived, not stored: `folder = basename(path)` — the child directory name inside the workspace
(`duplicate.CopyAll` layout). All collision rules operate on `folder`.

## Sandbox.sources — the mutable seeded-folder record

### Invariants (hold after EVERY successful operation; FR-064)

- **I1 — record == disk**: for every `SourceRef`, `<workspace>/<folder>` exists (modulo FR-063's
  tolerated already-gone case, which is repaired by the removal that observes it); every
  *seeded* top-level workspace entry corresponds to a `SourceRef`. (Agent-created directories
  and `.switchboard/` are outside the seeded set and never appear in the record.)
- **I2 — never empty**: `len(sources) >= 1` (FR-061). Enforced before deletion, not after.
- **I3 — unique folders**: no two `SourceRef`s share `basename(path)` (add-time refusal, R6).
- **I4 — reserved name**: no `SourceRef` has folder `.switchboard`.
- **I5 — originals untouched**: no operation ever opens an origin path for writing; adds open
  sources read-only (`duplicate` semantics), removals touch only `<workspace>/<folder>`.
- **I6 — deletion stays home**: every `RemoveAll` target must pass the existing `within(workspaceRoot, …)`
  guard.
- **I7 — edits never drive sandbox state**: `Sandbox.state` is unchanged by both operations —
  no CREATING, no STOPPED, no ERROR transitions (FR-054, FR-058). Only `sources` and
  `updated_at` change (plus the emit that carries them, FR-067).

### Downstream readers (unchanged code, new obligation)

| Reader | Behavior after an edit |
|--------|------------------------|
| Sandbox list display (FR-017a) | shows the edited set (it renders `sources`) |
| `Manager.Refresh` (FR-030) | re-seeds exactly the edited set |
| `Manager.bringUp` relaunch path | passes the edited `Sources` in `LaunchSpec` |
| Escape-hatch `workspaces` globs (005) | evaluate against current workspace children — self-adjusting |
| `config_snapshot.default_sources` | **frozen history; deliberately NOT updated** (spec assumption) |

## Add operation (transient)

**Input**: `sandbox_id`, `[]SourceRef` (≥1), `override_resource_warning`.

**Eligibility**: sandbox state ∈ {RUNNING, STOPPED}; operation latch free.

### Lifecycle

```
validate (R6: paths exist+dirs, folder collisions vs record ∪ disk ∪ batch,
          reserved name, clone-mode is_repo re-verified, state eligible)
   └─ any failure → refuse; nothing copied, record untouched
acquire latch ("add sources")
resource gate (duplicate mode only; blocked + override round-trip, FR-057)
stage: for each source → <workspace>/.switchboard/staging/<folder>
       (duplicate.CopyAll | Runner.CloneRepo; progress streamed, FR-055)
   └─ any failure → RemoveAll(staging); record untouched; sandbox state untouched (FR-058)
commit: os.Rename(staging/<folder> → <workspace>/<folder>) for each, then RemoveAll(staging)
   └─ rename failure → delete already-renamed folders from THIS op + staging; record untouched
record: store.Update — append SourceRefs, bump updated_at
emit: Event.sandbox_changed (FR-067)
release latch
```

**Failure atomicity (FR-058)**: observable outcomes are exactly two — {folder set unchanged,
record unchanged, no partial trees outside `.switchboard/staging`} or {all requested folders
present, record extended}. The live agent never observes an intermediate tree at a seeded path
(staging is inside `.switchboard/`, which agents ignore; the rename is atomic per folder).

## Remove operation (transient)

**Input**: `sandbox_id`, `[]path` (exact recorded `SourceRef.path` values, ≥1).

**Eligibility**: sandbox state ∈ {RUNNING, STOPPED}; operation latch free; service guard clear
(no active service instance with `working_dir` at or beneath any selected folder — checked at
the RPC boundary against the portforward instance store, FR-062); would leave ≥1 source (I2).
Client precondition: the FR-060 confirmation has already been accepted (daemon does not
re-confirm, matching refresh).

### Lifecycle

```
validate (all paths resolve to recorded SourceRefs — else NOT_FOUND, nothing deleted;
          I2 preserved; state eligible)
acquire latch ("remove sources")
service guard (FR-062) — refusal names the service and remedy; nothing deleted
per folder, in request order:
   within() re-check → RemoveAll(<workspace>/<folder>)  [missing folder = success, FR-063]
   └─ deletion failure → STOP; folders already deleted stay dropped from the record
      (record follows disk, R3); error names the failed folder
record: store.Update — drop the SourceRefs whose folders were deleted (or already gone)
emit: Event.sandbox_changed (FR-067)
release latch
```

**Partial-batch semantics**: unlike adds (rolled back), removals are applied per folder and the
record always reflects what is actually on disk — un-deleting is impossible, so the record
follows the disk (I1 over batch-atomicity; R3 rationale).

## Operation latch (in-memory)

`map[sandboxID]label` + mutex, try-acquire only (never blocks). Held by: add, remove, refresh.
Refusal: `FAILED_PRECONDITION "<label> in progress for this sandbox"` (FR-066). Not persisted;
a daemon restart clears it (with any in-flight operation dying with the daemon, whose staging
debris R2's startup purge collects).

## State-eligibility matrix

| Sandbox state | Add | Remove | Why |
|---|---|---|---|
| RUNNING | ✅ | ✅ | the headline case (FR-054); mount undisturbed (R1) |
| STOPPED | ✅ | ✅ | retained copy edited in place; next start reflects it (FR-065) |
| CREATING | ❌ | ❌ | launch owns the workspace until done |
| DESTROYING | ❌ | ❌ | workspace is being deleted |
| ERROR | ❌ | ❌ | recovery paths are refresh/destroy (R6); editing a half-seeded workspace compounds the damage |

## Wire additions (see [contracts/switchboard-edit-sources.proto](./contracts/switchboard-edit-sources.proto))

- `rpc AddSandboxSources(AddSandboxSourcesRequest) returns (stream LaunchProgress)` — existing
  stream shape reused verbatim (copy/log_line/blocked/done).
- `rpc RemoveSandboxSources(RemoveSandboxSourcesRequest) returns (Sandbox)`.
- `AddSandboxSourcesRequest { sandbox_id, repeated SourceRef sources, override_resource_warning }`
- `RemoveSandboxSourcesRequest { sandbox_id, repeated string source_paths }`
- **No** new enum, Event arm, NotificationKind, or `Sandbox` field. Purely additive revision.
