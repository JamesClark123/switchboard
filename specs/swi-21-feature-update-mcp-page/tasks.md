# Tasks: MCP Page Quality of Life — Edit, In-Page Secrets, Scopes (SWI-21)

**Input**: Design documents from `/specs/swi-21-feature-update-mcp-page/` (plan.md, spec.md, research.md,
data-model.md, contracts/proto-delta.md, contracts/sbx-cli-delta.md, contracts/mcp-form-ui.md, quickstart.md)

**Tests**: Included. The constitution (Rule VI) requires automated tests and ≥ 90% coverage per module. FR-422
requires an argv-asserting test for every new runtime flag, and SC-007 requires a leak test for secret values.

**Organization**: A shared foundation (proto, rules, record, lock, save skeleton) comes first, followed by one
phase per user story in spec order: US1 Edit, US2 Secrets, US3 Client secret (all P1), then US4 Scopes (P2).
Each story's daemon tasks come before its TUI tasks.

Paths are relative to the repository root. `M/` = `src/services/switchboardd/internal/mcp/`,
`S/` = `src/services/switchboardd/internal/secret/`, `SB/` = `src/services/switchboardd/internal/sandbox/`,
`G/` = `src/services/switchboardd/internal/grpc/`, `MR/` = `src/libs/switchboard-proto/mcprules/`,
`UI/` = `src/apps/switchboard-tui/internal/ui/`, `C/` = `src/apps/switchboard-tui/internal/client/`,
`E2E/` = `src/apps/switchboard-tui-e2e/`, `P` = `src/libs/switchboard-proto/proto/switchboard.proto`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1–US4 from spec.md

---

## Phase 1: Setup

**Purpose**: The contract and the shared rule package that every later task uses.

- [X] T001 Apply [contracts/proto-delta.md](./contracts/proto-delta.md) to `P`: the new messages and enums, the
  field additions with the exact numbers listed there, and the four RPCs in the MCP gateway block. Run
  `make proto` and commit the regenerated `src/libs/switchboard-proto/gen/`. Re-check the field maxima first
  (`Sandbox` 24, `AddMcpServerRequest` 8, `McpServer` 8, `McpGatewaySettings` 2, `Event.McpGatewayChanged` 1);
  if another branch claimed a number, take the next free one and update the contract.
- [X] T002 [P] Create package `MR/` (`doc.go`; imports only the standard library and the same module's `gen`, research R1) with `headers.go`:
  `NormalizeHeaders` (moved in behaviour from `M/manager.go` `normalizeHeaders`; its messages keep their wording but
  quote `Redact(value)` instead of the raw header for a credential header, SC-007),
  `Placeholders(headers) []string` (distinct `${name}` in order), `ValidPlaceholder` (`^[A-Za-z0-9][A-Za-z0-9._-]*$`,
  ≤ 128, refuses `client_secret` with the collision message, FR-408), `SecretName(server, placeholder)`, and
  `ClientSecretName` / `IdentityName`. Table tests in `MR/headers_test.go`.
- [X] T003 [P] `MR/credential.go`: `IsCredentialHeader(name)` (FR-423 names and name segments split on `-`/`_`,
  case-insensitive; `Keycloak-Realm` false, `X-Api-Key` true), `CheckCredentialValue(name, value) error`
  (placeholders only, optional leading scheme from the fixed list, separators space `:` `;` `,`; `Cookie`
  allows `<token>=${…}`; the message names the header and the literal's **position** ("text after the
  scheme"), never its value, and for an unlisted scheme says the scheme goes inside the secret), `Convert(name, value, used map[string]bool) (newValue, placeholder, secret string)`
  (derived name lower-cased, `_`→`-`, `-2`, `-3`, … on a collision or invalid/reserved name), and
  `Redact(value) string` (scheme kept + `RedactMarker`, the exported constant `••••`). Table tests for every spec example (`ApiKey ${k}`,
  `Cookie: session=${session}` accepted; `Bearer sk-live-abc ${x}`, `session=abc; csrf=${t}`,
  `X-Auth-Mode: oauth` refused).
- [X] T004 [P] `MR/scopes.go`: `ValidScopes([]string) error` (FR-417: printable ASCII without space, `"`, `\`
  or `,`; no leading `-`; ≤ 256 characters; ≤ 32; no duplicates; the comma refusal is controlled by the
  package **constant** `ScopeCommaAllowed = false`, shared by both binaries; Scenario 0 decides it) and `ParseScopes(line string) []string` (space-separated).
  `MR/oauth.go`: `ValidClientID` and `AuthServerOK` (moved from `M/manager.go` and `UI/mcp.go`, same rules
  and messages). `MR/warn.go`: `CredentialLooking(url string, args []string) []string` (query parameter or
  `--flag` names whose segment is a credential word or `sig`/`signature`). Table tests for each.
- [X] T005 [P] `MR/normalize.go`: `Normalize(def *pb.McpServerDefinition, recorded *pb.McpServerDefinition)`
  (research R6: scopes sorted and de-duplicated, minus a runtime-added `offline_access` not in `recorded`;
  headers keyed by lower-cased name with the value trimmed; URL trimmed), `Equal(a, b)` on normalised forms,
  `RegistrationChanged(old, new) bool` (any definition field except the mark), and
  `ClientChanged(old, new) bool` (FR-415: client ID, authorization server, or URL while a client ID is set on
  either side). Tests.

**Checkpoint**: proto regenerated; `go test ./...` passes in `switchboard-proto`.

---

## Phase 2: Foundational (blocking)

**Purpose**: The record, the per-name lock, the new argv, and a save skeleton that every story extends.

- [X] T006 Switch the daemon to `MR/`: `M/manager.go` `Add` calls `mcprules.NormalizeHeaders`,
  `ValidClientID` and `AuthServerOK` (FR-420). `Add` does **not** apply `CheckCredentialValue`: the old
  `AddMcpServer` RPC keeps today's validation (FR-422, research R2 *old clients*), so an older TUI's
  `X-Api-Key: abc` is still accepted (the runtime enforces its own `Authorization` rule, F5). The definition
  `Add` records (T010) stores any header that `CheckCredentialValue` flags as a literal redacted, with its name
  in `literal_on_host` (D1). The stricter rule runs only in `Save` (T013). Delete the moved helpers. Existing
  tests in `M/manager_test.go` keep passing unchanged (only error text moves). Add `G/` tests that pin it: an
  old-client `AddMcpServer` with `X-Api-Key: abc` succeeds, and its recorded definition holds only the redacted
  value.
- [X] T007 [P] `M/hostflags.go`: the six behaviour flags from
  [contracts/sbx-cli-delta.md](./contracts/sbx-cli-delta.md) as package variables, so tests can flip them,
  each with a doc comment naming its Scenario 0 check. `M/hostflags_test.go` pins the defaults.
- [X] T008 [P] `M/cli.go` argv: `argvAddRemote` takes `scopes` and appends one `--scope` each after the
  authorization server and before `--skip-auth`; `argvAuthorize(name, scopes, noScope)`; `argvInspect(name)` =
  `mcp inspect <name> --json`. Add `runBounded(ctx, args…)`: own process group (`setPgid`), kill the group on
  deadline, wait for exit before returning, and return the combined output with an error that never includes
  stdin (research R7). Argv tests in `M/manager_test.go` (`TestArgvScopesInspect`) and a `runBounded` timeout
  test that shows the child is reaped before return.
- [X] T009 `M/locks.go` (research R3): `nameLock` (1-slot channel + waiters) in `busy map[string]*nameLock`
  guarded by `Manager.mu`; `lockName(ctx, name, opts)` where `opts` says whether the request **alone** shows the
  operation touches the client secret/identity or removes the registration (then it fails with
  `ErrAuthInProgress` if `inflight[name]` is set, checked in the same `m.mu` hold as the take). In that same hold, before waiting, `lockName` refuses with
`ErrBeingReplaced` (`BEING_REPLACED`, "a save of this server is still running; try again later") while the
state map shows the name `saving` or `recording`. `nameLock` also carries a `refuse chan struct{}` and a
`refused bool`, guarded by `m.mu` like `busy`: the close and the swap both happen under `m.mu` with `stateMu`
nested for the state-map update (never from `stateMu` alone or inside a bbolt transaction). The holder closes it
through the flag (never twice) in the `m.mu` hold that marks the name `saving` or `recording`; for `saving` that
hold runs **after** the in-progress write lands, never inside its mutate func, so a failed write leaves no stale
`saving` or closed channel (a taker queued in between is refused at the close). The `m.mu` hold whose state-map
update clears `saving`/`recording` (the post-`persist` tail) swaps a closed channel for a fresh one and resets
the flag (an unclosed one is kept, so waiters that queued under an earlier holder still select on the current
channel); as a backstop for the one early exit that skips the tail (the `persist` retry ending on daemon
shutdown), unlock also swaps it if `refused` is still set, in its `m.mu` hold before
freeing the slot. The backstop never clears `saving`/`recording`: it only keeps "a closed channel has no live
waiters" true (research R2 *Leftover records*). No lock holder runs on a
goroutine that recovers panics, so a panic crashes the daemon and the restart resolves any record as
`recovering` (gRPC has no recovery interceptor; the hook `http.Server` in `cmd/sxbd/main.go` recovers handler
panics, so no lock holder may run under an `http.Handler`): `lockName` hands back one shared release that every
lock holder (the save, `Remove`, Cancel, `AddMcpServer`/`Add` (T010), `RemoveMcpServerSecrets` and `recoverName`
(T022)) defers directly as `defer release()` in the function that holds the lock for one name, so it runs when
that function returns. Only the deferred call is pinned: `defer func() { release() }()` and calling the release
from a helper are forbidden, while a goroutine body such as `go func() { defer release(); ... }()` (the detached
save) is fine. A `defer` never sits inside a loop: `recoverLoop` resolves each name by calling
`recoverName(ctx, name)`, which takes `lockName`, defers `release()` and returns, so each name's lock is freed
before the next name or backoff attempt. The release's first
statement is `if r := recover(); r != nil { panic(r) }` (Go stops a panic only when the deferred function itself
calls `recover()`). So every lock holder's deferred release skips its body on a panic (taking neither `m.mu` nor
`stateMu`) and a panic while either lock is held cannot deadlock the release instead of crashing; no deferred call
registered after the release (so run before it) takes `m.mu` or `stateMu`. A waiting
`lockName` selects on `{slot, ctx.Done(), refuse}` and returns `ErrBeingReplaced` (dropping its waiter count)
when it closes. So no caller, queued or new, ever waits on a save that holds an in-progress record or retries
its `persist` (research R2 *Leftover records*). Tests: a `Remove` and a second `Save` queued during a save's step 1
return `BEING_REPLACED` without blocking once the in-progress record is written, and the waiter count drops to
zero (so `Authorize` is refused only for the lock held); holder A unlocks normally with a `Save` and a `Remove`
queued behind it, the `Save` takes the slot and becomes `saving`, and the `Remove` returns `BEING_REPLACED`
without blocking; a `Remove` arriving after A's tail clears `saving` but before A unlocks (held in
`onReplaceEnded`) waits for the slot, is not refused, and takes the lock once A unlocks; closing the channel a
second time in one hold is a no-op (no double-close panic); an unlock with `refused` still set (the shutdown
exit, driven by cancelling the daemon context during the `persist` retry so the tail is skipped and the name is
still `saving`; no simulated panic) leaves a fresh channel with `refused` false, a new taker is
still refused `BEING_REPLACED` by the state-map check while the flag stands, and a later close of the new channel
does not panic; a panic-path test uses the re-exec pattern (`TestSavePanicCrashes` re-runs the test binary with
`exec.Command(os.Args[0], "-test.run=TestSavePanicCrashes", "-test.timeout=1m")` and an env flag; both children
get the non-zero `-test.timeout` so the runtime always has a live timer and a blocked child hangs instead of exiting
at once with `all goroutines are asleep - deadlock!`; in the child a test hook inside the `persist` mutate func,
called first in that func before anything that takes `stateMu` (the lock is not reentrant, so a hook placed after a
`ReplaceState`-style call would self-deadlock before it panics), calls `m.stateMu.Lock()` and panics with no deferred
unlock, and a second child's hook calls
`m.mu.Lock()` and panics the same way in `recoverName` under the per-name lock (the hook fires only on the test
env flag and simulates a panic while reading a malformed record, research R2; the child seeds a well-formed
leftover so the re-read succeeds), called after `lockName` returns and outside any `m.mu`/`stateMu` hold
(right after the record re-read, before the state-map update; `m.mu` is not reentrant either, so a hook placed where it is held would self-deadlock before it
panics), so the lock is still held when the release runs whatever unlock style production code uses; the hooks
panic with distinct values `swi21-test-hook-persist` and `swi21-test-hook-recover`): the parent runs each child
with `exec.CommandContext` and a 5 s deadline that kills it, and checks each child exits non-zero within 5 s
instead of hanging, with `panic: swi21-test-hook-persist` (or `-recover`) in stderr and no `test timed out` (so the
child's own `-test.timeout` panic never passes; the 5 s deadline must stay well below the child's 1 m
`-test.timeout`), so wrapping the release, moving the
`recover()` into a helper, or a deferred call that takes `m.mu`/`stateMu` before the release all fail it. A
negative control re-runs the first child with the guard switched off by a test-only package variable
(`if !skipPanicGuard { if r := recover(); r != nil { panic(r) } }`) and expects it still running at the 5 s
deadline (the parent then kills it), so the test is shown to fail on a broken guard. Add
  `recheckInflight(name)`, which `Save` calls under `m.mu` right after `classify`/`ClientChanged` once it knows
  the save replaces or clears identity (research R3 *Re-check after classify*). `Authorize` refuses with the new
  `ErrBeingReplaced` when `busy[name]` is held or waited on, or the R12 state map (`stateMu`, never the settings
  record) shows a pending replace or in-progress record (`pending`/`saving`/`recording`/`recovering`), checked in the same `m.mu` hold that records
  `inflight`. No registry read happens under `m.mu`. The lock records `replacing` (set under `m.mu` once `classify` decides the
  save re-registers), and each name has a `generation` bumped under `m.mu` on every release (research R3). Race tests with `-race`: authorize vs. save in both orders; a
  header-only save is allowed during an authorization. An authorization is in flight, then a URL-only edit is
  saved: expect `AUTH_IN_PROGRESS`, no `rm` in the argv log, and nothing stored.
- [X] T010 Record definitions (research R4): `M/manager.go` `Add` writes `definitions[name]` together with
  the mark in one `UpdateMcpSettings` (after the re-list, as the mark is today), under `lockName`. `Remove`
  takes the lock and drops the definition with the mark. `reconcile` reads the generations from the leaf state
  map (`stateMu`) before `sbx mcp ls`. It then re-checks them inside the `UpdateMcpSettings` mutate func, under
  `stateMu` only, and skips every name that is locked or whose generation changed (research R4). It **never**
  holds `m.mu` across the registry write (R12 *Lock order*), and calls `onReplaceEnded` only after the update
  returns. It keeps a definition (and mark) whose name has a pending replace, and otherwise drops it
  like a stale mark. `List` returns `settings` with
  `definitions`/`pending_replaces` cleared and fills `ListMcpServersResponse.pending_replaces`. `decorate`
  sets `McpServer.rebind_pending` from the definition, and `McpServer.save_in_progress` (state map `saving` or `recording`) or
  `McpServer.recovering` (state map `recovering`) when the name has an in-progress record (research R2); the mark is reported from `marks` as usual. `reconcile` also keeps the
  definition and mark of a name with an in-progress record, and never resolves one. Tests: a list taken while a name is locked keeps its
  mark and definition; a list taken **before** a lock that is released by the time it applies (stale snapshot)
  changes nothing for that name; stale definitions drop.
- [X] T011 [P] `S/manager.go`: `ListGlobal(ctx) (map[string]bool, error)` (names in the global scope only,
  FR-409), and `Remove` returns the CLI output so callers can re-check. Add `S/` methods
  `SetQuiet`/`RemoveQuiet` that skip `emitChanged`, so a save emits `secrets_changed` once at the end
  (FR-421). Tests in `S/manager_test.go`, including a check that a value never appears in an error.
- [X] T012 Wire the secret manager into the MCP manager: `NewManager` takes a `Secrets` interface (`ListGlobal`,
  `SetQuiet`, `RemoveQuiet`, `EmitChanged`) and an `onReplaceEnded func(name string, registered bool)` hook
  (nil-safe). It draws the fingerprint key and key id with `crypto/rand` (research R5). Update
  `src/services/switchboardd/cmd/sxbd/main.go` (construct the secret manager first) and every test
  constructor.
- [X] T013 `M/save.go` skeleton (research R2): `Manager.Save(ctx, req) (*pb.McpSaveOutcome, error)`. It
  validates with `MR/` (all FR-420 rules, including `CheckCredentialValue` on every header except a redacted
  `LITERAL_ON_HOST` one; scopes and headers refused on a host command; the local-execution acknowledgement;
  worker prefix), runs on `context.WithoutCancel(ctx)` bounded by `saveTimeout = 10 *
  time.Minute`, takes `lockName`, and runs these steps as named functions that later tasks fill in:
  `checkFingerprint`, `snapshot`, `classify`, `markInProgress`, `removeStep`, `storeStep`, `addStep`, `identityStep`,
  `rollbackStep`, `persist`. The forward steps share the `saveTimeout` budget. Each cleanup call (every rollback
  removal, every re-check list, the identity removal and re-check, `persist`) runs on its **own** fresh context
  from `context.WithoutCancel`, with that CLI call's own bound (research R2). `persist` writes definition, mark and pending replace in one `UpdateMcpSettings`,
  then emits one `mcp_gateway_changed` carrying the outcome and pending replaces, and one `secrets_changed`
  when any secret changed. In this task: a new-server add with no secrets and a switchboard-only mark change
  (FR-403: no `rm`/`add` in the argv log). A switchboard-only save of a server with no record creates no
  definition (data-model D5). Tests: a save whose forward budget expires still rolls back; one slow removal does not fail the next one.
- [X] T014 `G/mcp_rpcs.go`: handler for `SaveMcpServer`. It maps sentinel errors to codes per the contract
  table, attaches `McpSaveRefusal` as a status detail, and returns the outcome with `OK` when the save ran.
  The goroutine-detached save still returns to a live caller. `G/server.go`: `DaemonInfo.capabilities` adds
  `"mcp-edit"`. Make sure no interceptor or log line prints `SaveMcpServerRequest` (grep `G/` for request
  logging). Tests in `G/mcp_rpcs_test.go`, including: a client cancel mid-save still persists and emits.
- [ ] T015 [P] `C/mcp.go`: wrappers `GetMcpServerDefinition`, `SaveMcpServer` (returns the outcome, or a
  typed `*SaveRefusal` decoded from the status detail), `CancelMcpPendingReplace`, `RemoveMcpServerSecrets`,
  and `AuthorizeMcpServer` gaining `scopes []string, noScope bool`. Tests in `C/mcp_test.go` against the fake
  server.
- [ ] T016 [P] TUI capability gate: `UI/mcp.go` stores `hasEdit` from the daemon's `HasCapability("mcp-edit")`
  when the view opens; without it the view and form behave exactly as today. Test both paths.

**Checkpoint**: an add through `SaveMcpServer` registers and records a definition; a mark-only save never
re-registers; the gateway list still works for old clients.

---

## Phase 3: User Story 1 — Edit a registered MCP server (P1) 🎯 MVP

**Goal**: Edit opens the form pre-filled from switchboard's record and the host. Saving replaces the
registration, refuses stale forms, handles every failure state, and keeps a pending replace that every
client can see.

**Independent Test**: quickstart Scenario 1 and Scenario 5. Register a server with one header, edit the URL
and add a header, then check that the view and `sbx mcp inspect` show the new definition with the mark
unchanged.

### Daemon

- [X] T017 [US1] `M/parse.go`: `parseInspect(out)` → typed struct with optional fields (headers as strings or
  `{name,value}`, `url`, `client_id`, `oauth_authorization_server`, `resource`, `type`/`kind`, `image`,
  `skip_ssrf_check`); unknown or invalid JSON ⇒ nothing reported. `parseAuthStatusScopes(out, name)` reads
  `default_scopes` / `recorded_scopes` / `scopes.default` when present. Fixture tests in `M/parse_test.go`,
  including the real-host `[{server_name,status}]` shape (⇒ not reported).
- [X] T018 [US1] `M/definition.go`: `Manager.Definition(ctx, name) (*pb.McpDefinitionView, error)` (research
  R11). It runs `ls`, `inspect` and `auth status` in parallel under the list bound; `NOT_FOUND`/`GONE` if the
  name is not listed and has no pending replace. It merges per FR-402: the runtime wins where it positively
  reports (respecting `inspectReportsHeaders` / `authStatusReportsScopes`); `CHANGED_ON_HOST` only on a
  normalised difference; a runtime-added `offline_access` is left out of the pre-fill; `NOT_RECOVERABLE` is
  named for fields with no source; `unsupported_settings` lists resource, SSRF override and image kinds. Set
  `has_record`, `attach_by_default`, `auth_state` and `pending_replace` (with
  `pending_removal_failed_listed` / `_unknown` from `ListGlobal`). It takes **no** lock (research R11). When the name is not listed but the state map shows a save running, it
  returns `save_in_progress` (`saving` or `recording`) or `recovering` (`recovering`) instead of `GONE`; a listed name gets the
  same flag. The TUI says the server is being saved elsewhere, or that an interrupted save is being recovered. With
  `for_add` it returns only `stored_secrets` + `secrets_available`/reason for any name, registered or not. In
  edit mode it fills `stored_secrets` too. Tests cover each FR-402 rule and US1 #2, #8, #9.
- [X] T019 [US1] `M/definition.go` redaction (FR-423, US1 #9): every reported credential header runs through
  `CheckCredentialValue` before it is placed in the view. A literal is replaced with `Redact` and flagged
  `LITERAL_ON_HOST`. A record template reported without a placeholder follows `inspectReturnsTemplates`:
  true ⇒ credential header redacted and flagged, non-credential header `CHANGED_ON_HOST`; false ⇒ *not
  reported*, flagged `RESOLVED_UNCHECKABLE`. Test: the literal string never appears in the marshalled view.
- [X] T020 [US1] `M/definition.go` fingerprint (research R5): `fingerprint(def, reported, pendingRev)` =
  key id ‖ HMAC-SHA-256 over a length-prefixed canonical encoding (deterministic proto marshal of the record;
  raw reported header lines sorted; reported scopes; unsupported names; pending revision). `checkFingerprint`
  in `M/save.go` recomputes it under the lock from fresh reads and refuses with `STALE_RESTARTED` (key id
  differs) or `STALE_CHANGED`. A **failed** re-read (`ls`, `inspect` or `auth status` errors) refuses with the new
  `UNVERIFIED` reason, never `STALE_CHANGED` (research R2 step 1). Tests: a host-side change to a redacted
  literal changes the fingerprint; a restart (new manager) yields `STALE_RESTARTED`; open succeeds, the
  save-time `inspect` fails, and the save is refused `UNVERIFIED` with nothing changed.
- [X] T021 [US1] `M/save.go` replace path (FR-404, FR-406, research R7): `classify` uses
  `mcprules.RegistrationChanged`. `removeStep` runs `rm --force` via `runBounded`; on failure it re-lists
  under the lock: absent ⇒ continue; present ⇒ outcome `STILL_REGISTERED`, stop. `addStep` runs the add via
  `runBounded`; failure or timeout ⇒ never re-checked, `add_started` = true. Refuse a replace without
  `confirm_discard_authorization` unless `auth_state` is `UNAUTHORIZED` or `NOT_APPLICABLE`, and an unsupported setting without
  `confirm_drop_unsupported` (FR-402). Edit of a vanished name ⇒ `GONE` with nothing removed (edge case).
  Kind change is allowed (edge case). Redacted headers (research R2): a header flagged `LITERAL_ON_HOST` (only)
  that still holds the redacted value is unchanged for `classify`, exempt from
  `CheckCredentialValue`, accepted only on a save that does not re-register (persisted redacted, name in
  `literal_on_host`), and refused `INVALID_ARGUMENT` on any re-registering save. Tests: a mark-only save with a
  redacted header runs no `rm`/`add`; a URL change with one is refused; the argv log never holds `••••`; a `RESOLVED_UNCHECKABLE` header's
  template is accepted on a re-registering save, and is compared with the record's template in `classify`: with
  `inspectReturnsTemplates = false`, a mark-only save of a server with a resolved header runs no `rm`/`add`.
  Failure-injection tests for each branch with the scripted fake `sbx`.
- [X] T022 [US1] `M/pending.go` (research R8): `holdPending` on a replace that ends `NOT_REGISTERED` (mark moved
  out of `marks`; `previous` = last live definition; revision+1), `Manager.CancelPending(ctx, name, rev)`
  (best-effort removal of `removal_failed` names with a re-check, then drop pending, mark and definition;
  never refused because a removal failed; returns left-on-host and unconfirmed names), and
  `endByAnotherPath` called from `reconcile` when a pending name is listed and unlocked (drop all three; the
  event outcome names the `removal_failed` secrets). `Add`/new-server `Save` of a pending name ⇒
  `PENDING_EXISTS`. Retry: `Save` with `retry_of_revision` checks the revision (`PENDING_ENDED` when gone);
  the pre-step removes carried `removal_failed` names left blank (still listed or unreadable ⇒
  `PENDING_REMOVAL_FAILED`, no change); then it runs as a new-server save compared with `previous`. Success
  drops the pending replace; failure updates it. Tests for every transition in data-model.md.
  **Crash safety** (research R2 *Crash safety*, SC-006): `markInProgress` writes a separate
  `McpSaveInProgress` (`saves_in_progress[name]`: `definition`, `previous` (a Retry: the pending's), `retry`,
  the save's mark as a copy, `planned_created_secrets` and `planned_overwritten_secrets` from the snapshot) in
  one `UpdateMcpSettings` call before `removeStep` of any registration change. It changes nothing else: the
  mark stays in `marks` (or in the Retry's pending replace) and the pending replace is untouched. `persist`
  deletes it in the same write as the outcome; a failed `persist` write is retried up to 3 times (1 s apart, own
  contexts), and if it still fails the save returns `UNAVAILABLE`, emits `secrets_changed` (if a store ran)
  and one `mcp_gateway_changed` without an outcome, then keeps the lock and re-runs the **same** write every 30 s on
  the daemon context until it lands or the daemon stops (then the record is a start-up leftover). This applies to
  every save: a switchboard-only save (no in-progress record) sets the state map's `recording` flag and closes
  the lock's `refuse` channel before it emits the `UNAVAILABLE` events (and so before returning), with the "the save ran but its result (mark, re-bind state)
  could not be recorded yet; retrying" message naming the stored secrets (a registration change keeps "the host
  was changed …"); its tail clears `recording`, and a daemon stop loses only its mark and `rebind_pending`
  (research R2 *Failed `persist`*). The write is a
  per-name mutate func built once from the outcome, touching only `definitions[name]`, `marks[name]`,
  `pending_replaces[name]` and `saves_in_progress[name]`, re-applied to the record read in each attempt's
  transaction (never a stored snapshot). The retry path reuses the normal post-`persist` tail, run only after the
  write lands: state-map update (with the `refuse` channel swap, T009), `onReplaceEnded` where the normal path calls it, outcome event, unlock and
  generation bump. It never hands the name to `recoverLoop`. `Save`, Cancel, `Remove`, FR-413 removals and `AddMcpServer` that find a record under the lock
  refuse `UNVERIFIED` with no change (it can only be a leftover); `SetDefault` (`SetMcpServerDefault`) refuses
  while the state map shows `saving` or `recording` (`BEING_REPLACED`) or `recovering` (`UNVERIFIED`), checked before its
  `listRows` (`sbx mcp ls`), and re-checks `saves_in_progress[name]` inside its own `UpdateMcpSettings` mutate
  func, which returns a refusal sentinel (aborting the write) when the record is present (`UNVERIFIED` if the
  state map shows `recovering`, else `BEING_REPLACED`); both refusals map to `FAILED_PRECONDITION` with the
  `McpSaveRefusal` detail and return before `reconcile(rows)` and `emitChanged()` (research R2 *Leftover
  records*). `NewManager` makes **no**
  host call: it loads the state map with every record as `recovering`, queues them, and starts the one
  `recoverLoop` goroutine (start-up leftovers only; nothing is queued later). Each attempt runs one `sbx mcp ls`, then resolves each queued name under `lockName`
  (record re-read under the lock; gone ⇒ skipped): not listed ⇒ replace: new pending replace (mark moved out of
  `marks`, error `daemon stopped during the save`, planned-created names as `removal_failed_secrets`, overwritten
  names in the error); Retry: its pending replace kept and updated as a failed Retry (`previous`,
  `removed_secrets` and the carried `removal_failed_secrets` kept, planned-created names added); new server:
  record dropped, planned-created names removed best effort. Listed ⇒ record dropped; replace keeps mark and
  `previous`; new server/Retry get `definition` + mark (Retry's pending dropped); replace and Retry call
  `onReplaceEnded(name, true)` after the registry write returns (a new server: no call); `rebind_pending` set
  when `definition` vs `previous` is `mcprules.ClientChanged` (FR-415). Each name is resolved in
  `recoverName`, which defers its own release (T009), so no name's lock outlives its resolution. A failed list or
  registry write keeps the names queued and retries with backoff (5 s doubling to 5 min); `reconcile` never
  resolves records. Tests: after a recovery attempt resolves a name, a `Remove` of that name takes the lock
  without waiting; an attempt whose registry write fails for a name retries, and the second attempt takes that
  name's lock without hanging;
  the manager is stopped between `rm` and `add`, a new manager starts on the same registry, and expect a pending
  row with the mark held, Retry and Cancel working, and `reconcile` not dropping it. Stopped before `rm` ⇒ mark
  and previous definition live. Stopped after the add ⇒ mark live, and for a client change (new client ID)
  `rebind_pending` set. For both, a sandbox launched during the save records `SKIPPED_BEING_REPLACED`, and after
  the restart and recovery its entry is `NOT_ATTACHED_AT_LAUNCH`. A Retry stopped after its first store, then a
  restart ⇒ a pending row with the original `previous`, `removed_secrets` and `removal_failed_secrets` plus the
  planned-created names. A save paused between `rm` and `add` while `reconcile` runs ⇒ the record, mark and
  definition are untouched. A leftover plus a blocking `ls` stub ⇒ `NewManager` returns at once, and `List`
  reports the name `recovering`. A list failure keeps the record; a `Save`, an old-client `AddMcpServer` and a
  `SetMcpServerDefault` of that name are refused `UNVERIFIED`; the retry loop resolves it once the list
  succeeds. An in-progress write that fails ⇒ the save stops with no host change, the name is not `saving`
  (`List` shows no `save_in_progress`), and the next `Remove` takes the lock instead of returning
  `BEING_REPLACED`; a `Remove` queued during the save's steps 1–4, followed by a failed in-progress write ⇒ that
  queued `Remove` gets the slot once the save unlocks and is not refused `BEING_REPLACED`. A save paused after its
  in-progress write lands but before the `m.mu` hold that marks it `saving`, with a `SetMcpServerDefault` of the
  name sent then ⇒ the toggle is refused `BEING_REPLACED` with no write to `marks`, and after `persist` the mark
  is the save's own (never reverted after a toggle that saw the record). A `SetMcpServerDefault` sent during a
  save's step 1–4 pause (before the record) ⇒ the toggle returns OK and emits `mcp_gateway_changed`, and after
  `persist` the mark is the form's (last save wins, research R2). A `SetMcpServerDefault` on a `recovering` name
  with a failing `ls` stub ⇒ `UNVERIFIED` with no `ls` call; a refused toggle emits no `mcp_gateway_changed`. `persist` fails once ⇒ the retry succeeds and the outcome is normal; a registration change whose `persist` fails 3 times ⇒
  `UNAVAILABLE` with both events, the name reports `save_in_progress`, `SetMcpServerDefault` is refused
  `BEING_REPLACED`, a `RemoveMcpServer`, a second `Save` and an `AddMcpServer` of the name return
  `BEING_REPLACED` at once (they do not block), and once the registry accepts writes the save's own outcome is recorded with no restart:
  for a successful replace the recorded definition is the **new** one and no `rebind_pending` is set; for a
  confirmed client change no `rebind_pending`; for a failed add the pending replace carries the real add error
  and only the rollback's actual `removal_failed_secrets`. A replace whose `persist` fails 3 times while
  another name is saved, then the registry recovers ⇒ both names' records are correct, the state map keeps the
  first name `saving` (no `pending` change, sweep entries untouched) until the write lands, and
  `onReplaceEnded` is called once, after it. A switchboard-only save (mark change plus a secret, and one with a
  failed identity clear) whose `persist` fails 3 times ⇒ `UNAVAILABLE` with the "the save ran but its result …"
  message (never "the host was changed") naming the stored secret, the state map shows `recording` (not
  `saving`, no in-progress record, `ResolveAttach` does not skip the server), a `List` triggered by its
  `mcp_gateway_changed` already reports `save_in_progress`, a `RemoveMcpServer` already queued on the lock and a new one both return
  `BEING_REPLACED` without blocking, `SetMcpServerDefault` is refused `BEING_REPLACED`, and once the registry
  accepts writes the mark and `rebind_pending` are recorded and `recording` clears. The recovered error
  (start-up leftover) names `planned_overwritten_secrets`.
- [X] T023 [US1] `G/mcp_rpcs.go`: handlers for `GetMcpServerDefinition` and `CancelMcpPendingReplace`; and
  `ListMcpServers` returns `pending_replaces`. Tests in `G/mcp_rpcs_test.go`.
- [X] T024 [US1] FR-425 launch skip (research R12): `ResolveAttach` returns `BeingReplaced` (marked names
  locked by a replace or with an in-progress record, live or leftover, plus pending names whose held mark is
  on). `SB/manager.go` `McpAttach` carries it;
  `applyMcp` logs `mcp: <name> skipped: being replaced` and records `Sandbox.mcp_skipped`
  (`SKIPPED_BEING_REPLACED`). New `SB/manager_mcp.go` `SweepMcpSkipped(name, registered bool)` changes entries
  to `NOT_ATTACHED_AT_LAUNCH` (registered) or clears them, emitting `sandbox_changed`. `sandbox.Manager.AttachMcp` (the `AttachMcpServer`
  RPC) clears a matching entry. Wire `onReplaceEnded` in `cmd/sxbd/main.go` and call it on **every** end:
  `REGISTERED` (any replace or retry) or `STILL_REGISTERED` ⇒ true; cancel or another path ⇒ false; nothing while a
  pending replace is held. Add the leaf state map (`stateMu`; `name → {replacing, pending, saving, recording, recovering, generation}`, loaded in `NewManager` with leftovers as `recovering`) in
  `M/locks.go`. The launch writes its `mcp_skipped` entry in a registry update whose mutate func calls
  `Manager.ReplaceState(name)`, which reads only that map, and writes the post-sweep state when the replace
  already ended. No registry write and no `onReplaceEnded` call happens under `m.mu` (research R12 *Lock order*);
  add a `-race` test that runs a launch, a reconcile and a pending end concurrently and checks it finishes.
  Only a `replacing` lock counts as being replaced; a mark- or secrets-only save does not skip the server. Update both callers (`G/sandbox_rpcs.go`, `G/orchestrator_rpcs.go`). Tests in
  `SB/manager_mcp_test.go` and `M/manager_test.go`, including the late-launch race, a plain replace that
  succeeds (entry → `NOT_ATTACHED_AT_LAUNCH`), and a launch between `markInProgress` and `persist` that records
  `SKIPPED_BEING_REPLACED` (and `List` still shows the mark, with `save_in_progress`).
- [X] T025 [US1] FR-419 data: the save outcome's `attached_sandboxes` lists this daemon's sandboxes whose
  `mcp_servers` contain the name, plus FR-425 skipped ones (`skipped_at_launch`), with `exclusive` from
  `mcp_attach_mode`. Read through a `Sandboxes` lister interface given to the manager in `cmd/sxbd/main.go`.
  Test.

### TUI

- [ ] T026 [US1] `UI/mcp_form.go` (new): move the form out of `UI/mcp.go` and make it the add/edit form.
  `mcpFormVals` gains only comparable fields (`scopes`, `mode` add/edit/retry). Secret values
  (`secretVals map[string]string`), the opened definition (`opened`) and the view (`*pb.McpDefinitionView`)
  live in `mcpState` beside it (research R14). Add mode calls `GetMcpServerDefinition{for_add: true}` when the
  form opens and whenever the name changes. Builders are split into `fillMcpVals(view)` +
  `buildMcpForm(spec, vals)` (SWI-9 pattern) so `reopenForm` keeps edits. Edit mode shows `Edit <server> @
  <host>` with the name not editable, and field descriptions carry the view's flags
  ([contracts/mcp-form-ui.md](./contracts/mcp-form-ui.md)). The client-side rules all go through `MR/`
  (remove the `authServerOK`/`maxClientID` copies). Tests in `UI/mcp_form_test.go`.
- [ ] T027 [US1] `UI/mcp.go` keys: `e` on a registered server opens the form after `GetMcpServerDefinition`
  (`loading…` status; `GONE` ⇒ status and offer to add as new); hidden worker servers are never shown.
  Pending-replace rows render `<server> — not registered (replace failed <age> ago)`; `R` asks `h` held / `p`
  previous, then opens the form in retry mode (names removed and removal-failed secrets; the "old values are
  gone" note for previous); `X` opens the `y`-only Cancel confirm naming the server as not registered, the
  dropped mark and record, and the secrets it will remove (or *may be left on the host*). Rows show `rebind
  pending`, `saving…` (`save_in_progress`) and `recovering interrupted save` (`recovering`); `e` on a
  `recovering` row shows that status instead of opening the form; `e` and `R` on a `saving…` row show "a save
  of this server is still running; try again later" without calling `GetMcpServerDefinition` or opening the
  form, and a read that returns `save_in_progress` (the row was stale) shows the same instead of opening it;
  `m`/`space` on a `saving…` or `recovering` row shows that status without calling `SetMcpServerDefault`; `d`
  and `X` on a `saving…` row send their RPC and show the `BEING_REPLACED` message. Help bindings updated.
  Tests, including the `m` toggle on both row states and `e`/`R` on a `saving…` row (no RPC sent), and `e`
  on a stale row whose read reports `save_in_progress` (no form).
- [ ] T028 [US1] `UI/mcp_form.go` save sequence: client rules → confirmations in the contract's order, each
  `y`-only through `confirm.go` with back-out reopening the form: dropped settings (FR-402), local execution
  for any resulting host-command definition (FR-407), authorization will be discarded (FR-404). Then
  `SaveMcpServer` with the fingerprint, `confirm_*` and `acknowledge_local_execution` set. Kind change clears
  the remote-only fields with a status line. `UNVERIFIED` and `BEING_REPLACED` show the refusal's own message,
  keep the form and its values (no reload) and re-list; `UNAVAILABLE` closes the form with the message, and the row shows `saving…` until the next
  `mcp_gateway_changed`, with no outcome panel or follow-up offers for that save when its result lands
  later (contracts/mcp-form-ui.md). Tests for each confirm and its back-out, for `UNVERIFIED`
  with each of its two messages, for `BEING_REPLACED` (form and typed secrets kept, view re-listed; after the
  other save lands, a re-save from the kept form is `STALE_CHANGED` when that save re-registered and goes through
  with the form's mark when it was switchboard-only), and for
  `UNAVAILABLE`.
- [ ] T029 [US1] Outcome panel and follow-ups in `UI/mcp.go`: render `McpSaveOutcome` (state line, host
  messages verbatim, secret lists, FR-419 list with the skipped and exclusive notes), then the FR-405
  authorize offer after a successful replace of a remote server. On `STILL_REGISTERED`/`NOT_REGISTERED` the
  form stays open with the edits. Tests.
- [ ] T030 [US1] In-place reload on refusal (FR-404): `reloadMcpForm(refusal)` re-reads the definition (for
  retry mode it re-reads the pending replace, and if that has ended, closes the form with how it ended). A
  field counts as edited when its value ≠ `opened` (via `MR.Equal`). Edited fields keep the developer's value
  and are flagged `changed on the host: was …, host now …` where the host also changed. Unedited fields take
  the re-read value. Secret drop rules: URL replaced ⇒ header secrets dropped; client ID, authorization server
  or URL replaced ⇒ client secret dropped; an orphaned placeholder or a client secret without a client ID ⇒
  dropped. The status names every dropped secret. The re-read becomes the new baseline and fingerprint.
  Table tests per rule (US1 #7).
- [ ] T031 [US1] `UI/sandbox_mcp.go`: the overlay lists `mcp_skipped` entries (`skipped: being replaced`,
  `not attached at launch — a to attach`, exclusive: `missing from this sandbox's fixed set — relaunch to
  include it`). `a` on such an entry attaches it, and the sandbox list's third line appends `· mcp skipped:
  <n>`. Tests in `UI/sandbox_mcp_test.go`.
- [ ] T032 [US1] `UI/formguard.go`: replace the `*m.mcpView.vals != m.mcpView.baseline` comparison with
  `mcpSnapshot()` / `equal()` (vals + `secretVals`, as `launchSnapshot` does) and a deep-copied baseline, so a
  non-empty masked field is dirty. While the form holds a pending replace (after a `NOT_REGISTERED` outcome, or
  in retry mode), the leave path (clean or dirty) opens the `y`-only Cancel confirmation of T027 and calls
  `CancelMcpPendingReplace`; declining keeps the form (FR-406, US1 #5). Tests in `UI/formguard_test.go`.

**Checkpoint**: US1 independently testable (Scenario 1, Scenario 5, Scenario 6.1/6.3).

---

## Phase 4: User Story 2 — Set a server's secrets from the MCP form (P1)

**Goal**: One masked field per placeholder; values are stored by the save, in the right order, with
rollback and a conversion offer for literal credentials.

**Independent Test**: quickstart Scenario 2. Add a server with `Authorization: Bearer ${api-key}`, enter
the value, save, and check the Secrets view lists `mcp:<server>:api-key`.

### Daemon

- [X] T033 [US2] `M/save.go` `snapshot` + `storeStep` (FR-410, FR-412): `ListGlobal` under the lock right
  after the fingerprint check; stores via `SetQuiet` in placeholder order, then the client secret. A new
  server or switchboard-only save runs the stores before the add (or alone); a replace runs them after the
  remove and before the add. The add runs only if every store succeeded. `secret_values` keys must be
  `mcp:<name>:<placeholder>` for a placeholder in the definition or `mcp:<name>:client_secret` with a client
  ID present; anything else ⇒ `INVALID_ARGUMENT`. Multi-line values, **empty** values, and values containing
  `mcprules.RedactMarker` (`••••`) are refused with `INVALID_ARGUMENT` naming the secret, never the value
  (research R2 *Secret values*). The TUI leaves blank fields out. Tests: `{"mcp:s:api-key": ""}` is refused and
  nothing runs; `Bearer ••••` as a value is refused. Each secret is classified
  created or overwritten from the snapshot. Argv-order tests.
- [X] T034 [US2] `M/save.go` `rollbackStep` (FR-406, FR-412): after a failed store or add, remove created
  secrets, plus overwritten ones for a replace or a new server's add. For a switchboard-only save, remove
  created ones only and name the overwritten ones as changed. Each failed removal is re-checked against a
  fresh `ListGlobal`. Fill `rolled_back`/`rollback_failed` and, for a replace, the pending replace's names.
  Failure-injection tests for each branch.
- [X] T035 [US2] FR-413 removal (research R13): `Manager.RemoveSecrets(ctx, name, secrets)` under the lock;
  only names of the server's own placeholder, client secret, or identity forms are allowed; `ErrAuthInProgress`
  when it touches the client secret or identity during an authorization; failed removals are re-checked.
  `G/mcp_rpcs.go` handler. The save outcome's `unused_secrets` names stored secrets whose placeholder the
  edit removed. Tests.
- [X] T036 [US2] SC-007 leak test in `M/save_test.go`: run every save path (success, each failure, rollback,
  retry, cancel) with distinctive secret values and a typed literal credential header that is refused, and assert that none
  appears in the fake `sbx` argv log, any
  error string, the outcome, the emitted events, the registry bytes, or captured `log` output.

### TUI

- [ ] T037 [US2] `UI/mcp_form.go` secrets group: `advanceMcpForm` tracks the focused field and group; when
  focus leaves group 1 (not merely the headers field, so later fields stay reachable), the form is suspended,
  then rebuilt from the vals with `reopenForm(gMcp)` and `NextGroup` into the secrets group, keeping `secretVals`
  by secret name (research R14). Group 2 always starts with a `huh.NewNote`, so it is never empty (huh v1.0.0
  panics on an empty group). Test: a form with no placeholders and no client ID opens group 2 without panic. The headers field's `DescriptionFunc` lists the derived `mcp:<server>:<p>`
  names live. One `EchoModePassword` input per placeholder, with a status derived from `stored_secrets` and the view (`set (blank keeps
  it)`, `missing`, `must be re-entered (URL changed)`, `removal failed; will be removed unless re-entered`).
  When secret storage is unavailable, the note carries the reason and no inputs follow it. Placeholder rule errors
  name the placeholder (FR-408). Tests (US2 #1, #3, #7).
- [ ] T038 [US2] Literal-credential conversion (FR-423, US2 #8, #9): at the T037 group-leave suspension and
  again on save, each credential header with literal text opens a `y`-only offer that applies `MR.Convert`, moves the
  literal into the new secret field, and keeps that field editable. Declining keeps the form open with the
  refusal. A header that still equals the view's redacted `LITERAL_ON_HOST` value is converted to the template
  **without** moving `••••` into the secret: the new field stays blank and `missing`, with `re-enter the
  credential (held on the host)`. Declining for such a header continues into group 2 with the warning, so
  secrets- and mark-only saves stay reachable (research R14). Tests: converting `Bearer ••••` leaves the secret
  field blank; declining it reaches group 2. URL/argument credential warning (US2 #10) as a `y`-only confirm. A save that would re-register
  while a `LITERAL_ON_HOST` header is unconverted is refused; a mark- or secrets-only save is allowed and
  keeps the warning. Tests.
- [ ] T039 [US2] Pre-save secret confirmations (FR-409, FR-411): missing secrets ("register anyway?"),
  removal-failed blanks ("this save will remove …"), and blanks after a URL change ("header will not be sent
  until set") are combined into one `y`-only prompt. Tests.
- [ ] T040 [US2] FR-413 offer after the outcome panel: name each `unused_secrets` entry and remove it
  behind a `y`-only confirm via `RemoveMcpServerSecrets`. The Secrets view (if open on that host) and the
  gateway view refresh from the events (FR-421). Tests.

**Checkpoint**: US2 independently testable (Scenario 2); no secret value appears anywhere (T036).

---

## Phase 5: User Story 3 — Pre-registered OAuth client with its client secret (P1)

**Goal**: A client-secret field beside the client ID, stored before registration, and the identity binding
cleared (or the server marked) on a client change.

**Independent Test**: quickstart Scenario 3. Add a server with a client ID and secret, check that the
secret is stored before the add, and authorize.

- [X] T041 [US3] `M/save.go` `identityStep` (FR-415, research R9): after a successful add (or a
  switchboard-only save storing the client secret for a marked or unrecorded server), clear the binding with `RemoveQuiet(IdentityName)` when any of these holds: a
  client change with a client secret stored in this save; a `rebind_pending` server with a client secret
  stored; or no record and a client secret stored. Otherwise set `LEFT_IN_PLACE` + mark on a client change
  without a secret, or `NOT_TOUCHED`. A failed clear is re-checked with a fresh `ListGlobal`: absent ⇒
  `CLEARED`; listed ⇒ `NOT_CLEARED` (error verbatim); unreadable ⇒ `UNCONFIRMED` (both errors verbatim). When the
  re-check confirms the record absent, the removal's output is **left out** of `host_errors` (a first add has no
  `:identity` record yet), and the table test asserts it. The mark stays or is set unless cleared. A failed/refused save before this step reports `NOT_TOUCHED` and sets
  no mark (FR-404 precedence). The FR-424 exclusion (via T009) covers any save storing or removing
  `client_secret`/`:identity`. Table tests for every row of the FR-404 outcome rules.
- [X] T042 [US3] `M/save.go`: when the outcome is `LEFT_IN_PLACE` and `mcp:<name>:client_secret` is stored,
  add the client secret and identity record to `unused_secrets` with a public-client reason. `RemoveSecrets`
  clears `rebind_pending` when both are confirmed gone (research R9, round-18 resolution). When the client ID
  is cleared, add the same names (FR-413, US3 #5). Tests.
- [ ] T043 [US3] `UI/mcp_form.go` client-secret field (FR-414): shown when a client ID is present, titled
  `mcp:<server>:client_secret`, with "Needed only for a confidential client." and its stored status, plus
  the re-bind pending text from the contract when the server is marked. Replace the old "store it under
  Secrets first" description. Pre-save FR-415 warning when the client changes with no secret entered while
  one is stored (US3 #4). Tests.
- [ ] T044 [US3] Outcome panel identity text for each `Identity` value. For `NOT_CLEARED`/`UNCONFIRMED` it
  leads with "re-enter the client secret and save to retry the clear", then names
  `mcp:<server>:client_secret:identity` as removable in Secrets or the host CLI, and says the mark clears
  only on the next save that stores the client secret. The FR-413 offer uses the public-client wording for
  T042's names. Tests.

**Checkpoint**: US3 independently testable (Scenario 3).

---

## Phase 6: User Story 4 — OAuth scopes (P2)

**Goal**: Default scopes are recorded with the registration, and a one-time scope override or *no scopes*
is available on authorize.

**Independent Test**: quickstart Scenario 4. Register with `read write` and check that the authorization
link requests them.

- [X] T045 [US4] Daemon: `Save` and `Add` pass `definition.scopes` / `AddMcpServerRequest.scopes` to
  `argvAddRemote` after `ValidScopes`, and refuse them on a host command. `Authorize` takes `scopes`/`noScope`
  from `AuthorizeMcpServerRequest` (validated; both set ⇒ `INVALID_ARGUMENT`) and keeps today's argv when
  neither is set. `G/mcp_rpcs.go` passes them through. Argv tests (`--scope a --scope b`, `--no-scope`), and a
  test that the recorded definition is unchanged after a one-time authorize (SC-005).
- [ ] T046 [US4] TUI: the Scopes field in the definition group (space-separated, `MR.ValidScopes` errors kept
  in the form). `S` on a remote server opens a one-line prompt for one-time scopes with `ctrl+n` for *no
  scopes* and the "requested exactly as given; no refresh-token scope is added" note, then starts the
  existing authorization modal, whose verbatim runtime lines cover refused-scope output (FR-418). `A` is
  unchanged. Tests in `UI/mcp_test.go`.

**Checkpoint**: US4 independently testable (Scenario 4).

---

## Phase 7: Polish & cross-cutting

- [ ] T047 [P] E2E: make `E2E/harness.go` `stubSbx`'s MCP state stateful: `mcp add` writes a per-name state
  file (unless it fails), `mcp rm` deletes it, and `mcp ls` prints the table from that state (keeping the seeded
  `notion` row), so a failed replace leaves the name unlisted. Add `mcp inspect --json` from the same state, `secret ls/set/rm` state (set reads stdin), and an `mcp add` failure for URLs
  containing `refuse.invalid`. Add `E2E/mcp_e2e_test.go` tests: edit URL + header secret (US1/US2), and a
  failed replace → pending row → Retry previous → Cancel (FR-406/FR-426). Run `make e2e`.
- [ ] T048 [P] FR-421: every save, edit, secret store/removal and cancel refreshes the gateway and Secrets
  views on another connected client (`G/` event test, plus a TUI test that the `mcp_gateway_changed` outcome
  updates a second open view).
- [X] T049 [P] Update `specs/008-mcp-gateway-manager/contracts/sbx-mcp-cli.md` and `sbx-secret-cli.md` with
  the rows from [contracts/sbx-cli-delta.md](./contracts/sbx-cli-delta.md), all marked
  *documentation-derived*.
- [ ] T050 Update the `CLAUDE.md` SPECKIT block to mark SWI-21 **implemented** (tasks done, T052 pending) and
  summarise the shipped design in the house style.
- [ ] T051 Run `make fmt-check vet lint test cover env-check` (≥ 90% per module) and fix any gaps.
- [ ] T052 Manual: quickstart Scenario 0 on a real host. Record each flag's outcome in
  `contracts/sbx-cli-delta.md` and `M/hostflags.go`, then run Scenarios 1–6.

---

## Dependencies & execution order

- Phase 1 (T001–T005) → Phase 2 (T006–T016) → user stories.
- **US1** (T017–T032) depends on Phase 2. **US2** (T033–T040) depends on T013 and on T026 for the form.
  **US3** (T041–T044) depends on T033 (stores) and T026. **US4** (T045–T046) depends only on Phase 2 and T026.
- Inside US1: T017 → T018 → T019 → T020 → T021 → T022 → T023; T024–T025 after T022; TUI T026 → T027 → T028 →
  T029 → T030; T031 after T024; T032 after T026.
- T036 after T034 and T022. T047 after every story. T050–T052 last.

## Parallel opportunities

- T002, T003, T004, T005 (separate `MR/` files) after T001.
- T007, T008, T011, T015, T016 after T006.
- After Phase 2, the US4 daemon task (T045) can run beside US1. The US2 daemon tasks T033–T035 can run beside
  the US1 TUI tasks.
- T047, T048 and T049 in parallel at the end.

## Implementation strategy

1. **MVP = Phase 1 + Phase 2 + US1**: edit with replace, failure states and the pending replace, which
   already removes the "remove and retype" pain.
2. Add **US2** (secrets) and **US3** (client secret) next. They share the save's store and rollback steps,
   so build them back to back.
3. **US4** (scopes) is small and independent. It can land with US1 if convenient.
4. Finish with e2e, contract updates and the real-host Scenario 0 run, whose flag outcomes may simplify
   FR-402/FR-415 behaviour in a follow-up.
