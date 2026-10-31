# Data Model: MCP Page Quality of Life (SWI-21)

All persisted state lives in the existing registry `mcp` bucket record (`pb.McpGatewaySettings`) and in the
existing sandbox records. Wire shapes are in [contracts/proto-delta.md](./contracts/proto-delta.md).

## McpServerDefinition (new, persisted per daemon and server name)

What switchboard last registered (or saved) for a name. Key: server name in
`McpGatewaySettings.definitions`.

| Field | Type | Rules |
|-------|------|-------|
| `kind` | `McpServerKind` | REMOTE or LOCAL |
| `url` | string | remote only; `http(s)://` |
| `command` | `AddMcpServerRequest.LocalCommand` | local only; args may not contain `,` |
| `headers` | repeated string | `Name: value`; credential headers are placeholder-only (FR-423), or a redacted value whose name is in `literal_on_host` |
| `client_id` | string | `mcprules.ValidClientID` |
| `oauth_authorization_server` | string | `mcprules.AuthServerOK` |
| `scopes` | repeated string | `mcprules.ValidScopes`; remote only; ordered as entered, de-duplicated |
| `rebind_pending` | bool | FR-415 *client re-bind pending* |
| `literal_on_host` | repeated string | header names stored redacted (FR-423, mark/secrets-only save of a host literal) |

**Invariants**

- D1: never holds a secret value or a credential-header literal (SC-007).
- D2: dropped by `reconcile` when the host no longer lists the name, unless the name is locked by a save or
  has a pending replace or an in-progress record (`McpSaveInProgress`, FR-404, research R2 *Crash safety*).
- D3: written in the same `UpdateMcpSettings` call as the mark it belongs with.
- D5: created only by a save that registers. A mark- or secrets-only save of a server with no record does not
  create one (FR-402 keeps naming its unrecoverable fields).
- D4: removed by `Remove`, by Cancel of a pending replace, and when a pending replace ends *registered by
  another path*.

## McpPendingReplace (new, persisted per daemon and server name)

Key: server name in `McpGatewaySettings.pending_replaces`.

| Field | Type | Notes |
|-------|------|-------|
| `name` | string | |
| `attach_by_default` | bool | the held mark; **not** in `marks` while pending |
| `definition` | McpServerDefinition | the one being saved (last attempt) |
| `previous` | McpServerDefinition | last live definition (unchanged across retries) |
| `removed_secrets` | repeated string | names this attempt removed in rollback |
| `removal_failed_secrets` | repeated string | names whose rollback removal failed |
| `add_started` | bool | the add ran (it may still finish on the host) |
| `error` | string | host message, verbatim |
| `failed_at` | Timestamp | for `replace failed <age> ago` |
| `revision` | uint64 | +1 on each update; part of the Retry fingerprint |

**State transitions**

```text
McpSaveInProgress (replace) ──daemon start, name not listed──► PENDING (error "daemon stopped during the save")
McpSaveInProgress (Retry) ──daemon start, name not listed──► PENDING kept, updated as a failed Retry
(replace fails after remove) ──► PENDING
PENDING ──retry ok──► (gone; definition+mark live; skipped entries → NOT_ATTACHED_AT_LAUNCH)
PENDING ──retry fails──► PENDING (definition, names, error, failed_at, revision updated; previous kept)
PENDING ──retry pre-step: removal-failed name still listed──► PENDING (unchanged; save refused)
PENDING ──cancel──► (gone; mark + definition dropped; skipped entries cleared)
PENDING ──name listed by host while unlocked, no in-progress record──► (gone, "registered by another path"; mark + definition dropped; skipped entries cleared)
```

Invariants: P1 never holds a value; P2 no time limit; P3 never applied at launch; P4 an add of the name
from switchboard's form is refused while it exists.

## McpSaveInProgress (new, persisted per daemon and server name)

Key: server name in `McpGatewaySettings.saves_in_progress`. Crash safety (research R2): written by a
registration change (replace, new-server add, Retry) under the per-name lock before its first host mutation,
and deleted by its `persist`. Separate from `pending_replaces`, so it never overwrites a pending replace.

| Field | Type | Notes |
|-------|------|-------|
| `name` | string | |
| `attach_by_default` | bool | the save's mark (a copy; `marks` and the pending replace are not changed by this write) |
| `definition` | McpServerDefinition | the one being saved |
| `previous` | McpServerDefinition | live definition (replace), the pending replace's `previous` (Retry), absent (new server) |
| `retry` | bool | a Retry: a pending replace of the name exists |
| `planned_created_secrets` | repeated string | `secret_values` names absent in the snapshot (the save would create them) |
| `planned_overwritten_secrets` | repeated string | `secret_values` names present in the snapshot (the save would overwrite them) |

**State transitions**

```text
(registration change, under the lock, before rm/add) ──► SAVING
SAVING ──save persists──► (gone; the outcome is persisted in the same write)
SAVING ──daemon start, name not listed──► (gone) replace: new PENDING, planned-created names = removal_failed;
                                          Retry: its PENDING updated as a failed Retry; new server: names removed best effort
SAVING ──daemon start, name listed──► (gone) replace: mark + previous kept; new server/Retry: definition + mark live,
                                      Retry's PENDING dropped; rebind_pending set if definition vs previous is a client change
SAVING ──daemon start, list fails──► SAVING (start-up recovery retries the list with backoff; never resolved by reconcile)
```

Invariants: S1 a lock holder that finds one refuses with `UNVERIFIED` (it can only be a leftover); S2 while it
exists the name is kept by `reconcile`, refused by `Authorize`, skipped at launch while marked, and reported
`save_in_progress` by `List`.

## McpGatewaySettings (existing, extended)

`default_attach_mode` (1), `marks` (2), **`definitions` (3)**, **`pending_replaces` (4)**,
**`saves_in_progress` (5)**.

## McpSkippedServer (new, on `Sandbox.mcp_skipped`)

| Field | Type | Notes |
|-------|------|-------|
| `name` | string | server name |
| `state` | `McpSkipState` | `SKIPPED_BEING_REPLACED` → `NOT_ATTACHED_AT_LAUNCH` |

Transitions: created at launch when the marked server is being replaced; → `NOT_ATTACHED_AT_LAUNCH` when a
retry (or the running replace) registers it; removed when attached from the overlay, or when the pending
replace is cancelled or the name is registered by another path. Persisted with the sandbox record.

## McpSaveOutcome (wire only, response + event)

| Field | Notes |
|-------|-------|
| `name` | |
| `state` | `REGISTERED`, `UNCHANGED_REGISTRATION` (switchboard-only save), `STILL_REGISTERED` (remove failed), `NOT_REGISTERED` |
| `replaced` | the registration was removed and added |
| `add_started` | for `NOT_REGISTERED`: may still complete on the host |
| `created`, `overwritten`, `rolled_back`, `rollback_failed` | secret names (never values) |
| `identity` | `NOT_TOUCHED`, `LEFT_IN_PLACE`, `CLEARED`, `NOT_CLEARED`, `UNCONFIRMED` |
| `rebind_pending` | resulting mark |
| `host_errors` | verbatim runtime messages, in step order |
| `attached_sandboxes` | FR-419: sandboxes whose records list the server, plus FR-425 skipped ones (with a flag) |
| `unused_secrets` | FR-413 / R9 removal offer candidates |
| `pending_replace` | set when the outcome left one |

## McpDefinitionView (wire only, definition read)

`definition` (merged, redacted), per-field `McpFieldFlag` list (`{field, kind: CHANGED_ON_HOST |
NOT_RECOVERABLE | LITERAL_ON_HOST | RESOLVED_UNCHECKABLE, host_value (redacted)}`), `unsupported_settings`,
`secrets` (`{name, status: SET | MISSING | REMOVAL_FAILED}`), `stored_secrets` (every global `mcp:<name>:*` name,
also returned `for_add`), `pending_removal_failed_listed` + `pending_removal_failed_list_unknown` (for the Cancel
confirmation),
`auth_state`, `has_record` (switchboard has a definition), `pending_replace`, `fingerprint`,
`secrets_available` (+ reason), `save_in_progress` (not listed because a save of it is running).

## Secret names (existing host secrets)

`mcp:<server>:<placeholder>` (placeholder rule `^[A-Za-z0-9][A-Za-z0-9._-]*$`, ≤ 128, not `client_secret`),
`mcp:<server>:client_secret`, runtime-managed `mcp:<server>:client_secret:identity` and
`mcp:<server>:<placeholder>:endpoint`. Switchboard reads only presence in the global scope.
