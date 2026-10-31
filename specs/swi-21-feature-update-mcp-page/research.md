# Research: MCP Page Quality of Life — Edit, In-Page Secrets, Scopes (SWI-21)

**Spec**: [spec.md](./spec.md) · **Plan**: [plan.md](./plan.md) · **Date**: 2026-10-31

## Findings (current code and runtime documentation)

- **F1 — Gateway manager** (`src/services/switchboardd/internal/mcp/manager.go`). `Add` validates and runs
  `sbx mcp add … --skip-auth`, re-lists, then marks. `Remove` runs `sbx mcp rm --force` and clears the mark.
  `Authorize` streams `sbx mcp auth <name>` and keeps `inflight[name]` under `m.mu`. Marks and the attach mode
  live in one `pb.McpGatewaySettings` record in the registry's `mcp` bucket (`registry.Get/UpdateMcpSettings`).
  `reconcile` drops marks whose name the host no longer lists. Nothing stores what was registered.
- **F2 — Secret manager** (`internal/secret`). `Set` pipes the value to `sbx secret set <name> --force` on
  stdin; `Remove` runs `sbx secret rm <name> -f`; `List` parses `sbx secret ls` (scope, type, name, masked).
  Every mutation emits `Event.secrets_changed`. The name rule already allows `:` (`mcp:<s>:<p>`).
- **F3 — TUI MCP view** (`src/apps/switchboard-tui/internal/ui/mcp.go`). One `huh` form (`mcpForm`) for
  add only, `mcpFormVals`, SWI-9 guard surface `gMcp` (`formguard.go`), `reopenForm`, the host-command
  warning modal (`pending`), the authorization modal (`mcpAuthWait`). Validation is duplicated from the
  daemon (`authServerOK`, `maxClientID`) by hand.
- **F4 — Launch** (`internal/grpc/sandbox_rpcs.go`, `orchestrator_rpcs.go`). Both call
  `mcp.ResolveAttach` and hand `sandbox.McpAttach{Mode, Servers, Skipped}` to the sandbox manager, which
  records `Sandbox.mcp_servers` / `mcp_attach_mode` (fields 21–22). The last `Sandbox` field is 24.
- **F5 — Runtime docs (re-read 2026-10-31, docs.docker.com `reference/cli/sbx/mcp/*` and
  `ai/sandboxes/mcp-gateway`).**
  - `sbx mcp add` takes a repeatable `--scope`, plus `--no-scope` (add-time only), `--resource`,
    `--skip-ssrf-check`, `--disable-http2`, `--local`, and **`--callback-port`** (add-time authorization only;
    relevant to SWI-22, not used here because switchboard always passes `--skip-auth`).
  - `sbx mcp auth <name>` takes a repeatable `--scope` (this run only, requested exactly as given, no
    `offline_access`) and `--no-scope` (cannot be combined with `--scope`).
  - `sbx mcp inspect NAME [--json|--format table|json|yaml]` shows a server's details: header templates and
    whether their secrets are set (never values) and the resource. The JSON schema is not documented.
  - `sbx mcp auth status --json` reports granted scopes, recorded defaults and supported scopes (key names not
    documented; the 2026-10-10 real-host run saw only `server_name` + `status`).
  - Credential headers such as `Authorization` **must** use a secret placeholder (runtime rule), which
    matches FR-423.
  - A header secret has an `:endpoint` record; after an endpoint change "follow the CLI guidance to set the
    secret again". After setting/changing/removing a header secret, existing gateways need a sandbox restart
    or a `sandboxd` restart.
  - Change of OAuth client: `sbx secret set mcp:<s>:client_secret`, then `sbx secret rm
    mcp:<s>:client_secret:identity`, then `sbx mcp auth <s>`.
  - `sbx mcp rm` removes the OAuth token before the registration; the client secret and identity binding stay.

## Decisions

### R1 — One shared rule package for TUI and daemon (FR-408, FR-417, FR-420, FR-423)

**Decision**: Add a pure package `github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules`
(new directory `src/libs/switchboard-proto/mcprules/`) holding every definition rule: header syntax
(moved from `mcp.normalizeHeaders`), placeholder extraction and name rule, the reserved `client_secret`
name, the credential-header rule and its one-step conversion, scope tokens, client ID, authorization
server (moved from both `authServerOK` copies), and the URL/argument credential warning. The daemon and
the TUI both call it; the TUI copies are deleted.

**Rationale**: FR-420 requires byte-identical rules on both sides. Both modules already depend on
`switchboard-proto`, whose `types.go` already holds shared domain helpers. `mcprules` imports only the
standard library and the same module's `gen` package, so the `libs` direction rule holds. A shared table of
test cases cannot drift. Every rule that the real-host run may relax (the scope comma, FR-417) is a
**constant in `mcprules`**, never a daemon-side runtime flag, so the TUI and daemon binaries always agree.

**Error text never carries a credential (SC-007)**: a refused credential header is named by its header name
and the position of the literal ("text after the scheme", "text before `${…}`"), never by its value. Every
`mcprules` error that would quote a header value quotes `Redact(value)` for a credential header, including
the messages moved from `normalizeHeaders` (which quote the whole header with `%q` today). The daemon's
`--debug` interceptor (`grpc/logging.go`) logs these errors, so this is the only safe form.

**Alternatives**: a new `src/libs/switchboard-mcprules` module (one more `go.work` entry and `replace` lines
for a few hundred lines of code); keeping two copies with a shared fixture (still two implementations).

### R2 — The save is one daemon RPC that outlives its caller (FR-404)

**Decision**: New unary RPC `SaveMcpServer`. The handler validates the request, then runs
`Manager.Save` on a context derived with `context.WithoutCancel(ctx)` bounded by `saveTimeout` (a package
constant, 10 min: one `mcp rm` + N stores + one `mcp add` + rollbacks, each with its own existing bound).
If the client goes away the gRPC handler returns, but the goroutine finishes, persists the outcome and emits
`mcp_gateway_changed` (with the outcome) and `secrets_changed`. The response returns the same
`McpSaveOutcome`.

Steps, all under the per-name lock (R3):

1. Definition check: recompute the fingerprint (R5) and compare with the request's; mismatch →
   `FAILED_PRECONDITION` with `McpSaveRefusal{STALE_CHANGED | STALE_RESTARTED | PENDING_ENDED}`.
2. Pending-replace pre-step (R8): remove carried *removal failed* names that the retry leaves blank.
3. Secret-list snapshot (`secret.Manager.List`, global scope only).
4. Classify: *switchboard-only* (mark and/or secrets) or *registration change* (any definition field,
   compared in normalised form, R6).
5. Registration change: `sbx mcp rm --force` (existing argv) → on failure, wait for exit, re-list (R7).
6. Stores (`secret.Manager.Set`, global, stdin) in placeholder order, then the client secret.
7. `sbx mcp add … --skip-auth` with the new definition (scopes added, R10).
8. Identity clear (R9).
9. Rollback on failure (R7), pending replace on a replace's failure (R8).
10. Persist definition + mark (+ pending replace) in **one** `UpdateMcpSettings` call; emit once.

**Contexts**: the forward steps (1–7) share the `saveTimeout` budget. Every cleanup call runs on its **own**
fresh context derived from `context.WithoutCancel`: each rollback removal, each re-check list, the identity
removal and its re-check, and `persist`. Each context has the CLI's own bound (`secret` mutate 60 s, list
30 s), never one shared step budget. An exhausted forward budget or a slow removal therefore never makes the
remaining cleanups fail.

**Redacted headers (FR-403, FR-423)**: a request header whose name the view flagged `LITERAL_ON_HOST` and
whose value equals the view's redacted value counts as **unchanged** in `classify`, and is exempt from
`CheckCredentialValue`. It is accepted only by a save that does not re-register, and is persisted redacted with
its name in `literal_on_host`. Any save that would re-register while such a header is unconverted is refused
with `INVALID_ARGUMENT` by the daemon (not only the TUI), so the host's credential is never replaced by `••••`.
This applies to `LITERAL_ON_HOST` **only**. A `RESOLVED_UNCHECKABLE` header (when *inspect returns resolved
values*) is pre-filled with the record's `${…}` template, is never redacted, and is accepted on any save,
including a re-registering one, which registers the template (FR-423). In `classify` (and the R6 comparison) it
counts as *not reported*: it is compared with the record's template, never with the host's resolved value, so it
triggers a re-registration only when the developer edits it (FR-403). The TUI shows the "cannot be checked"
warning before such a save.

**Authorization warning**: `confirm_discard_authorization` is required for a replace unless the auth state is
`UNAUTHORIZED` or `NOT_APPLICABLE` (host commands have no authorization).

**First save of an unrecorded server**: a switchboard-only save (mark and/or secrets) of a server switchboard
has no record of does **not** create a definition, so FR-402 keeps naming its unrecoverable fields. Only a
save that registers creates the record.

**Rationale**: the spec makes the save one daemon operation with a fixed order; the existing `Add` already
re-lists before marking for the same reason. `context.WithoutCancel` is the standard way to finish work
after the caller leaves; the existing auth wait is deliberately the opposite (killable), so it is not reused.

**Alternatives**: a server-streaming save (progress frames). Rejected: the outcome is one message and the
spec needs no progress; a stream ties completion to the connection unless detached anyway.

`AddMcpServer` stays for older clients and keeps its behaviour, but now also records a definition (R4) and
takes the per-name lock. The new TUI uses only `SaveMcpServer` for form saves.

### R3 — Per-name lock and the authorization exclusion in one critical section (FR-404, FR-424)

**Decision**: `Manager.mu` (already guarding `inflight`) also guards `busy map[string]*nameLock`, where
`nameLock` is a 1-slot channel semaphore plus a waiter count. `lockName(ctx, name)` takes it (waiting is
allowed for saves, adds, removes and FR-413 removals; serialised, FR-404). Before taking it a caller that
touches `client_secret`/`:identity` or removes the registration checks `inflight[name]` under `m.mu` and
fails with `ErrAuthInProgress` (FR-424 refusals). `Authorize` checks, under the same `m.mu` hold, that no
lock is held or waited for **and** that no pending replace exists (read from the settings record) before it
records `inflight[name]`; otherwise `ErrBeingReplaced` (`FAILED_PRECONDITION`). The check-and-take for both
directions happens inside one `m.mu` critical section, so neither can slip in between.

A save that only stores header secrets and does not re-register skips the in-flight check (FR-424 allows it).

The lock records its **purpose** (`replacing bool`, set when `classify` decides the save re-registers, under
`m.mu`). Only a replacing lock counts as "being replaced" for launch (R12). Authorize is refused while any
save holds or waits for the lock (FR-424). Each name also has a **generation** counter, bumped under `m.mu`
whenever its lock is released, which `reconcile` uses to ignore stale list snapshots (R4).

The definition read (R11) **does not take the lock**.

**Rationale**: reuses the existing mutex; no new lock ordering. Waiting on the semaphore never holds `m.mu`.

### R4 — Switchboard's definition record lives beside the marks (FR-402, Key Entities)

**Decision**: Extend `pb.McpGatewaySettings` (the one record in the `mcp` bucket) with
`map<string, McpServerDefinition> definitions = 3` and `map<string, McpPendingReplace> pending_replaces = 4`.
Marks, definitions and pending replaces therefore change in one bbolt write. `reconcile` drops a definition
whose name the host no longer lists unless the name is locked (save running) or has a pending replace; it
detects *registered by another path* (R8). Definitions hold credential headers only as templates or as
redacted+flagged values (R11). `AddMcpServer` (old clients) and `SaveMcpServer` both write it; `Remove`
deletes it.

**Stale snapshots**: `reconcile` reads the per-name generations (R3) **before** running `sbx mcp ls`. When it
applies the result, it skips every name that is locked now or whose generation changed since the list
started. Generations live in the leaf state map (R12 *Lock order*), so `reconcile` reads them before the list and
re-checks them **inside** the `UpdateMcpSettings` mutate func. It never holds `m.mu` across the registry
write, and it calls `onReplaceEnded` (the sweep) only after the update has returned. A list taken before a replace's
failed add therefore cannot end the new pending replace as *registered by another path*. A list taken before
a new server's add cannot drop the definition just written.

**Rationale**: the spec says "per daemon, beside the existing attach marks"; a single record keeps the
"a list taken mid-replace MUST NOT drop them" rule simple (lock and generation checks in `reconcile`).

### R5 — Fingerprint: HMAC-SHA-256 under a per-start key (FR-404)

**Decision**: At `NewManager` the daemon draws 32 random bytes (`crypto/rand`) and an 8-byte key id. The
fingerprint is `keyID ‖ HMAC-SHA-256(key, canonical)` hex-encoded, where `canonical` is a length-prefixed
encoding of: the stored definition (deterministic `proto.MarshalOptions{Deterministic: true}`), the raw
runtime-reported header lines (unredacted, sorted), the reported scopes, the reported unsupported-setting
names, and, for a Retry form, the pending replace's `revision`. The key never leaves memory. On a mismatch
the daemon reports `STALE_RESTARTED` when the key id differs, else `STALE_CHANGED`.

**Rationale**: exactly the spec's construction; deterministic marshalling keeps the MAC stable.

### R6 — Normalisation for comparison and pre-fill (FR-402)

**Decision**: `mcprules.Normalize(def)` returns a comparable form: scopes sorted and de-duplicated with a
runtime-added `offline_access` removed (one not in the record); headers keyed by lower-cased name with the
value trimmed around `:`; URL trimmed. The flags on the definition read and the "is this a registration
change" test (R2 step 4) both use it, so a presentation-only difference neither flags nor re-registers.

### R7 — Failure handling for remove and add (FR-406, FR-412)

**Decision**: the CLI runner gains `runBounded`, used for `rm` and `add` inside a save: the child runs in
its own process group (as `runStreaming` already does); on the context deadline the group is killed and
`Wait` is awaited before the step returns, so "the process has exited" holds before any re-check.

- Failed **remove** → re-list under the lock: name absent ⇒ success, continue; present ⇒ outcome
  `STILL_REGISTERED`, nothing stored yet, nothing rolled back.
- Failed **store** or failed/timed-out **add** → never re-checked. Roll back: remove every secret this save
  created **and** (for a replace or a new server's add) overwrote; for a switchboard-only save, created only,
  overwritten named as changed. A rollback removal that fails is re-checked against a fresh global list:
  absent ⇒ removed. Outcome `NOT_REGISTERED`, with `add_started` set only if the add ran.

### R8 — Pending replace lifecycle (FR-406, FR-426)

**Decision**: on a replace that ends `NOT_REGISTERED`, store `McpPendingReplace{name, attach_by_default,
definition, previous, removed_secrets, removal_failed_secrets, failed_at, error, revision}` in
`pending_replaces` (revision increments on each update). The mark is removed from `marks` and held in the
pending replace (so launch never applies it, FR-425); the previous definition is kept as `previous`.

- **Retry**: an ordinary `SaveMcpServer` with `retry_of_revision` set. Its definition check compares the
  revision. Pre-step: each carried `removal_failed` name the request leaves blank is removed first (failed →
  re-check list; still listed or unreadable ⇒ refuse with no change, `PENDING_REMOVAL_FAILED`). Then it runs
  as a new-server save (no remove; the name is not registered) compared with `previous` for FR-409/FR-415.
  Success ⇒ pending dropped, mark and definition live, skipped sandbox entries become *not attached at
  launch* (R12). Failure ⇒ the pending replace is updated (definition = retried one, names = this attempt's).
- **Cancel**: new RPC `CancelMcpPendingReplace{name, revision}`. Under the lock: best-effort removal of every
  `removal_failed` name (with re-check), then drop pending, mark and definition; clear skipped entries.
  Never refused for a failed removal; the response names secrets still listed (or unconfirmed).
- **Registered by another path**: `reconcile` sees a listed name that has a pending replace and no lock ⇒
  drop pending, mark and definition; clear skipped entries; the emitted event's outcome lists the pending
  replace's `removal_failed` names.
- An `AddMcpServer`/new-server save of a name with a pending replace is refused (`FAILED_PRECONDITION`,
  "retry the pending replace").

The TUI's *Cancel* confirmation lists secrets "it will remove": the daemon's definition read for a pending
replace includes the current global-list status of each `removal_failed` name (`listed` / `unknown`).

### R9 — Identity binding clear and the *client re-bind pending* mark (FR-415)

**Decision**: a client change is a change of client ID, authorization server, or URL while a client ID is
set (old or new definition). After a successful add:

- client change **and** a client secret stored in this save, **or** the server is marked
  `rebind_pending` and this save stores a client secret, **or** switchboard had no definition and this save
  stores a client secret ⇒ `secret.Remove("mcp:<s>:client_secret:identity")`. On error, fresh list: absent ⇒
  `CLEARED`; listed ⇒ `NOT_CLEARED`; list unreadable ⇒ `UNCONFIRMED`. Not cleared/unconfirmed ⇒ mark stays/set.
  Cleared ⇒ mark cleared.
- client change, no client secret stored ⇒ `LEFT_IN_PLACE`, mark set.
- otherwise ⇒ `NOT_TOUCHED`.

**Open item round 17 (resolved)**: a later save of a marked server **without** the client secret does not
retry the clear (a removal without a new secret could hand the old secret to the new client). The mark also
stays. The real-host run decides whether the list shows `:identity` (contract flag
`secretListShowsIdentity`, default `false`); if it is later confirmed `true`, a follow-up can clear the mark
when the snapshot shows no `:identity` record. Nothing is added now.

**Open item round 18 (resolved)**: the FR-413 removal offer **is** extended to a save outcome of
`LEFT_IN_PLACE` while `mcp:<s>:client_secret` is stored: the post-save prompt offers *"public client? remove
the stored client secret and its binding record"*. It runs through the same FR-413 removal operation (R13),
which removes `client_secret` and `client_secret:identity` and, when both are confirmed gone, clears the
`rebind_pending` mark. This adds no new operation, closes the round-18 limitation, and keeps "never remove
silently".

### R10 — Scopes on add and authorize (FR-416–FR-418)

**Decision**: `argvAddRemote` appends one `--scope <s>` per default scope, after the authorization server
and before `--skip-auth`. `argvAuthorize(name, scopes, noScope)` appends `--scope` per one-time scope, or
`--no-scope`. `AuthorizeMcpServerRequest` gains `scopes` and `no_scope`; with neither, today's argv. The
authorize stream already forwards every runtime line verbatim, which covers refused-scope output. Rules:
`mcprules.ValidScopes` (printable ASCII 0x21–0x7E except `"` and `\`, no `,` unless the constant
`ScopeCommaAllowed`, no leading `-`, ≤ 256 chars, ≤ 32, no duplicates). `--no-scope` on add is not used.

### R11 — Reading the host's definition: inspect + auth status (FR-402, FR-423)

**Decision**: new RPC `GetMcpServerDefinition{name, for_add}`. It takes **no** lock: the save's fingerprint
check under the lock catches any race, and a read never blocks Authorize or another client's Edit. With
`for_add` (add mode, or any name that is not registered) it runs only step 5 and returns only the secret
status: every global-scope `mcp:<name>:*` name (`stored_secrets`) plus `secrets_available`/reason (from whether
`ListGlobal` succeeded). The TUI derives each field's *set / missing* from `stored_secrets` as headers are
typed, in add and edit mode alike. The same field is filled in edit mode. When the name is not listed but
the state map shows a save running, the read returns `save_in_progress = true` (with `stored_secrets`)
instead of `GONE`, and the TUI says "being saved on another client, try again". Otherwise the daemon:

1. reads `sbx mcp ls` (kind from TRANSPORT), `sbx mcp inspect <name> --json` (new argv) and
   `sbx mcp auth status <name> --json` (existing argv);
2. parses inspect tolerantly: an object with any of `headers` (list of strings or `{name,value}`), `url`,
   `client_id`, `oauth_authorization_server`, `resource`, `type`/`kind`, `image`, `skip_ssrf_check`. Unknown
   JSON or a failed command ⇒ every field *not reported*. Auth status: any of `default_scopes`,
   `recorded_scopes`, `scopes.default` arrays. An empty list from a source not yet confirmed
   (`inspectReportsHeaders`, `authStatusReportsScopes`, both `false`) ⇒ *not reported*;
3. merges with the stored definition per FR-402 (runtime wins where positively reported, flagged when it
   differs in normalised form), names unrecoverable fields, lists unsupported settings (`resource`,
   `skip_ssrf_check`, registry/image kind);
4. applies the credential-header rule to every reported header **before** building the response: a literal
   credential ⇒ value redacted (scheme kept + `••••`), flagged `LITERAL_ON_HOST`. Behaviour flag
   `inspectReturnsTemplates = true` decides the template-vs-resolved case;
5. adds secret status for each placeholder and for `client_secret` from the global list (`SET`, `MISSING`,
   `REMOVAL_FAILED` while a pending replace holds it and it is listed);
6. lists this daemon's sandboxes whose `mcp_servers` contain the name (for FR-419, shown after a save);
7. returns the fingerprint (R5).

### R12 — Launch skip and per-sandbox record (FR-425)

**Decision**: `ResolveAttach` returns `BeingReplaced []string` (marked names whose lock is `replacing`, plus
names held in pending replaces whose held mark is on). `sandbox.McpAttach` carries it, the
launch log reports `mcp: <name> skipped: being replaced`, and the sandbox record gets
`repeated McpSkippedServer mcp_skipped = 25` (`{name, state}` with `McpSkipState`
`SKIPPED_BEING_REPLACED` / `NOT_ATTACHED_AT_LAUNCH`). When a pending replace or a running replace ends, the
manager calls `onReplaceEnded(name, registered bool)`, wired in `main.go` to a sandbox-registry sweep that
changes or clears entries and emits `sandbox_changed`. It is called for **every** end:
`REGISTERED` (any replace or retry) ⇒ `registered=true`; cancel or another path ⇒ `false`;
`STILL_REGISTERED` ⇒ `true` (the server is still registered, and it was skipped only because of the
replace). `NOT_REGISTERED` with a pending replace held ⇒ no call (still pending).

**Lock order**: the MCP manager keeps a small in-memory **state map**, `name → {replacing, pending,
generation}`, guarded by its own leaf mutex `stateMu`. It is updated whenever a lock is taken or released, a
save turns `replacing`, or a pending replace is created or ends; it is loaded from the settings record at
start. `stateMu` is a leaf: nothing else is acquired while it is held, and it may be taken under `m.mu` or
inside a bbolt transaction. Rules: no registry write (bbolt `Update`) and no `onReplaceEnded` call happens
while `m.mu` is held, and no registry read is opened inside another registry transaction.

**Late launch race**: a launch resolves `BeingReplaced` before the replace ends but persists its
`mcp_skipped` entry after the sweep. To close that gap, the launch writes the entry in a registry update whose
mutate func re-reads the replace state through `ReplaceState(name)`, which reads only the state map under
`stateMu`. If the replace has ended,
it writes the post-sweep state instead (`NOT_ATTACHED_AT_LAUNCH`, or no entry). `sandbox.Manager.AttachMcp`
(behind the `AttachMcpServer` RPC) clears the entry. Exclusive-mode
sandboxes get the "relaunch to include it" note in the TUI (flag `mcpLoadWorksInExclusive = false`).

### R13 — FR-413 removal as a daemon operation (FR-413, FR-424)

**Decision**: new RPC `RemoveMcpServerSecrets{name, secrets[]}`. Accepts only names of the form
`mcp:<name>:<placeholder>`, `mcp:<name>:client_secret`, `mcp:<name>:client_secret:identity`. Runs under the
per-name lock, refused with `ErrAuthInProgress` when it touches the client secret or identity during an
authorization. Each removal is re-checked against the list on failure. Clears `rebind_pending` when both
the client secret and the identity record are confirmed gone (R9).

### R14 — TUI form shape (FR-401, FR-408, FR-414, US1–US4)

**Decision**: the add form becomes the add/edit form in a new file `ui/mcp_form.go`; `mcp.go` keeps the
list, keys and auth modal. The form has two `huh` groups.

**huh v1.0.0 mechanism**: huh cannot add fields to a built group, has no blur hook, and has no disabled
input. So:

- `advanceMcpForm` tracks the focused field key (`form.GetFocusedField().GetKey()`) before and after each
  update.
- When focus **leaves group 1** (the group index changes from 0 to 1), it **suspends** the form, the way the
  host-command warning's `pending` modal does today. If a credential header holds a literal, it shows the
  conversion prompt. Leaving the headers field within group 1 does nothing, so every field after it stays
  reachable.
- It then rebuilds the whole form from the vals with `reopenForm(gMcp)` and calls `NextGroup` to land in the
  secrets group, so the secret fields always match the current headers. Moving back to group 1 and leaving it
  again repeats this.
- Group 2 always begins with a `huh.NewNote` (the secret list summary, "no secrets needed", or the
  unavailable-storage reason), so it is never empty (an empty group panics in huh v1.0.0 `Group.Init`).

**Guard compatibility (SWI-9)**: the comparable form fields stay in `mcpFormVals`. Secret values, the opened
definition and the view live beside it in `mcpState` (`secretVals map[string]string`, `opened`, `view`).
`formguard.go` compares through `mcpSnapshot()`/`equal()` (as `launchSnapshot` does) and deep-copies the
baseline, so a non-empty secret field counts as an edit.

**Leaving a failed-replace or Retry form (FR-406)**: while the form shows a `NOT_REGISTERED` outcome with a
pending replace, or is in retry mode, the guard's discard path opens the same `y`-only Cancel confirmation
as `X` and calls `CancelMcpPendingReplace`. Declining keeps the form. *Cancel* in the SWI-9 dialog keeps the
form too. Leaving with **no** edits after a failure is still a discard and still asks.

The two groups are:

1. **Definition**: name (read-only in edit: shown as the group description, not a field), kind, URL,
   headers (multi-line, `formSpec.text`), client ID, authorization server, scopes (one line,
   space-separated), command/args/dir, attach-by-default. Flags from the definition read appear in each
   field's description (`changed on the host: …`, `literal credential on host`, `not recoverable`).
2. **Secrets**: rebuilt from the definition group's values each time the developer moves into it
   (`DescriptionFunc` on the headers field also lists the derived secret names live): one masked
   `EchoModePassword` input per distinct placeholder, labelled `mcp:<server>:<placeholder>`, with
   `set` / `missing` / `must be re-entered` / `removal failed; will be removed unless re-entered`, then the
   client-secret field when a client ID is present. Values entered are kept by secret name across rebuilds.

Pre-save checks (all client-side, then confirmations in this order, each `y`-only through `confirm.go`):
literal-credential conversion offer (on leaving the headers field, and again on save); URL/argument
credential warning; FR-402 dropped settings; host-command local-execution warning (FR-407); authorization
will be discarded (unless `UNAUTHORIZED`); FR-411 missing secrets / FR-409 removal-failed / FR-409 URL
change must re-enter; FR-415 left-in-place warning. Then `SaveMcpServer`.

After the save: outcome summary (created/overwritten/rolled back secrets, identity outcome, state), the
FR-419 sandbox list, the FR-413/R9 removal offer, then the FR-405 authorize offer. A refusal reloads the form
in place per FR-404 (`reloadMcpForm` keeps edited fields by comparing against the opened baseline).

Keys on the gateway view: `e` Edit (FR-401), `S` Authorize with scopes… (FR-418); on a pending-replace row:
`R` Retry (prompts: held / previous definition), `X` Cancel. `A`, `d`, `space`, `a`, `m` are unchanged.

The guard surface stays `gMcp`; masked fields count as dirty when non-empty (SWI-9).

### R15 — Capability gate

**Decision**: the daemon advertises `DaemonInfo.capabilities` entry `mcp-edit`. The TUI shows `e`, `S`,
the secret group, scopes and pending rows only when the daemon has it; otherwise the old add form and keys
remain. Older clients keep working because every change is additive (FR-422).

### R16 — Real-host reconciliation flags (FR-422)

Package constants in `internal/mcp/hostflags.go`, each pinned by a test and listed in the CLI contract:
`inspectReportsHeaders`, `authStatusReportsScopes`, `inspectReturnsTemplates`, `secretListShowsIdentity`,
`killedAddMayComplete`, `mcpLoadWorksInExclusive`. The scope-comma rule is the constant
`mcprules.ScopeCommaAllowed` (R1), not a daemon flag, because both binaries apply it. Quickstart Scenario 0
runs each one.
