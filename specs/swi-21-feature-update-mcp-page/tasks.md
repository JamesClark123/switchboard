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

- [ ] T001 Apply [contracts/proto-delta.md](./contracts/proto-delta.md) to `P`: the new messages and enums, the
  field additions with the exact numbers listed there, and the four RPCs in the MCP gateway block. Run
  `make proto` and commit the regenerated `src/libs/switchboard-proto/gen/`. Re-check the field maxima first
  (`Sandbox` 24, `AddMcpServerRequest` 8, `McpServer` 8, `McpGatewaySettings` 2, `Event.McpGatewayChanged` 1);
  if another branch claimed a number, take the next free one and update the contract.
- [ ] T002 [P] Create package `MR/` (`doc.go`, standard library only, research R1) with `headers.go`:
  `NormalizeHeaders` (moved verbatim in behaviour from `M/manager.go` `normalizeHeaders`; error messages kept),
  `Placeholders(headers) []string` (distinct `${name}` in order), `ValidPlaceholder` (`^[A-Za-z0-9][A-Za-z0-9._-]*$`,
  ≤ 128, refuses `client_secret` with the collision message, FR-408), `SecretName(server, placeholder)`, and
  `ClientSecretName` / `IdentityName`. Table tests in `MR/headers_test.go`.
- [ ] T003 [P] `MR/credential.go`: `IsCredentialHeader(name)` (FR-423 names and name segments split on `-`/`_`,
  case-insensitive; `Keycloak-Realm` false, `X-Api-Key` true), `CheckCredentialValue(name, value) error`
  (placeholders only, optional leading scheme from the fixed list, separators space `:` `;` `,`; `Cookie`
  allows `<token>=${…}`; the message names the literal part and, for an unlisted scheme, says the scheme goes
  inside the secret), `Convert(name, value, used map[string]bool) (newValue, placeholder, secret string)`
  (derived name lower-cased, `_`→`-`, `-2`, `-3`, … on a collision or invalid/reserved name), and
  `Redact(value) string` (scheme kept + `••••`). Table tests for every spec example (`ApiKey ${k}`,
  `Cookie: session=${session}` accepted; `Bearer sk-live-abc ${x}`, `session=abc; csrf=${t}`,
  `X-Auth-Mode: oauth` refused).
- [ ] T004 [P] `MR/scopes.go`: `ValidScopes([]string) error` (FR-417: printable ASCII without space, `"`, `\`
  or `,`; no leading `-`; ≤ 256 characters; ≤ 32; no duplicates; the comma refusal is controlled by a
  `CommaAllowed` package variable, default false) and `ParseScopes(line string) []string` (space-separated).
  `MR/oauth.go`: `ValidClientID` and `AuthServerOK` (moved from `M/manager.go` and `UI/mcp.go`, same rules
  and messages). `MR/warn.go`: `CredentialLooking(url string, args []string) []string` (query parameter or
  `--flag` names whose segment is a credential word or `sig`/`signature`). Table tests for each.
- [ ] T005 [P] `MR/normalize.go`: `Normalize(def *pb.McpServerDefinition, recorded *pb.McpServerDefinition)`
  (research R6: scopes sorted and de-duplicated, minus a runtime-added `offline_access` not in `recorded`;
  headers keyed by lower-cased name with the value trimmed; URL trimmed), `Equal(a, b)` on normalised forms,
  `RegistrationChanged(old, new) bool` (any definition field except the mark), and
  `ClientChanged(old, new) bool` (FR-415: client ID, authorization server, or URL while a client ID is set on
  either side). Tests.

**Checkpoint**: proto regenerated; `go test ./...` passes in `switchboard-proto`.

---

## Phase 2: Foundational (blocking)

**Purpose**: The record, the per-name lock, the new argv, and a save skeleton that every story extends.

- [ ] T006 Switch the daemon to `MR/`: `M/manager.go` `Add` calls `mcprules.NormalizeHeaders`,
  `ValidClientID` and `AuthServerOK`, and also applies `CheckCredentialValue` to every header (FR-420,
  FR-423; the runtime enforces the same rule, F5). Delete the moved helpers. Existing tests in
  `M/manager_test.go` keep passing unchanged (only error text moves).
- [ ] T007 [P] `M/hostflags.go`: the seven behaviour flags from
  [contracts/sbx-cli-delta.md](./contracts/sbx-cli-delta.md) as package variables, so tests can flip them,
  each with a doc comment naming its Scenario 0 check. `M/hostflags_test.go` pins the defaults.
- [ ] T008 [P] `M/cli.go` argv: `argvAddRemote` takes `scopes` and appends one `--scope` each after the
  authorization server and before `--skip-auth`; `argvAuthorize(name, scopes, noScope)`; `argvInspect(name)` =
  `mcp inspect <name> --json`. Add `runBounded(ctx, args…)`: own process group (`setPgid`), kill the group on
  deadline, wait for exit before returning, and return the combined output with an error that never includes
  stdin (research R7). Argv tests in `M/manager_test.go` (`TestArgvScopesInspect`) and a `runBounded` timeout
  test that shows the child is reaped before return.
- [ ] T009 `M/locks.go` (research R3): `nameLock` (1-slot channel + waiters) in `busy map[string]*nameLock`
  guarded by `Manager.mu`; `lockName(ctx, name, opts)` where `opts` says whether the operation touches the
  client secret/identity or removes the registration (then it fails with `ErrAuthInProgress` if
  `inflight[name]` is set, checked in the same `m.mu` hold as the take). `Authorize` refuses with the new
  `ErrBeingReplaced` when `busy[name]` is held or waited on, or a pending replace exists, checked in the same
  `m.mu` hold that records `inflight`. Race tests with `-race`: authorize vs. save in both orders; a
  header-only save is allowed during an authorization.
- [ ] T010 Record definitions (research R4): `M/manager.go` `Add` writes `definitions[name]` together with
  the mark in one `UpdateMcpSettings` (after the re-list, as the mark is today), under `lockName`. `Remove`
  takes the lock and drops the definition with the mark. `reconcile` keeps a definition (and mark) whose name
  is locked or has a pending replace, and otherwise drops it like a stale mark. `List` returns `settings` with
  `definitions`/`pending_replaces` cleared and fills `ListMcpServersResponse.pending_replaces`. `decorate`
  sets `McpServer.rebind_pending` from the definition. Tests: a list taken while a name is locked keeps its
  mark and definition; stale definitions drop.
- [ ] T011 [P] `S/manager.go`: `ListGlobal(ctx) (map[string]bool, error)` (names in the global scope only,
  FR-409), and `Remove` returns the CLI output so callers can re-check. Add `S/` methods
  `SetQuiet`/`RemoveQuiet` that skip `emitChanged`, so a save emits `secrets_changed` once at the end
  (FR-421). Tests in `S/manager_test.go`, including a check that a value never appears in an error.
- [ ] T012 Wire the secret manager into the MCP manager: `NewManager` takes a `Secrets` interface (`ListGlobal`,
  `SetQuiet`, `RemoveQuiet`, `EmitChanged`) and an `onReplaceEnded func(name string, registered bool)` hook
  (nil-safe). It draws the fingerprint key and key id with `crypto/rand` (research R5). Update
  `src/services/switchboardd/cmd/sxbd/main.go` (construct the secret manager first) and every test
  constructor.
- [ ] T013 `M/save.go` skeleton (research R2): `Manager.Save(ctx, req) (*pb.McpSaveOutcome, error)`. It
  validates with `MR/` (all FR-420 rules; scopes and headers refused on a host command; the local-execution
  acknowledgement; worker prefix), runs on `context.WithoutCancel(ctx)` bounded by `saveTimeout = 10 *
  time.Minute`, takes `lockName`, and runs these steps as named functions that later tasks fill in:
  `checkFingerprint`, `snapshot`, `classify`, `removeStep`, `storeStep`, `addStep`, `identityStep`,
  `rollbackStep`, `persist`. `persist` writes definition, mark and pending replace in one `UpdateMcpSettings`,
  then emits one `mcp_gateway_changed` carrying the outcome and pending replaces, and one `secrets_changed`
  when any secret changed. In this task: a new-server add with no secrets and a switchboard-only mark change
  (FR-403: no `rm`/`add` in the argv log).
- [ ] T014 `G/mcp_rpcs.go`: handler for `SaveMcpServer`. It maps sentinel errors to codes per the contract
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

- [ ] T017 [US1] `M/parse.go`: `parseInspect(out)` → typed struct with optional fields (headers as strings or
  `{name,value}`, `url`, `client_id`, `oauth_authorization_server`, `resource`, `type`/`kind`, `image`,
  `skip_ssrf_check`); unknown or invalid JSON ⇒ nothing reported. `parseAuthStatusScopes(out, name)` reads
  `default_scopes` / `recorded_scopes` / `scopes.default` when present. Fixture tests in `M/parse_test.go`,
  including the real-host `[{server_name,status}]` shape (⇒ not reported).
- [ ] T018 [US1] `M/definition.go`: `Manager.Definition(ctx, name) (*pb.McpDefinitionView, error)` (research
  R11). It runs `ls`, `inspect` and `auth status` in parallel under the list bound; `NOT_FOUND`/`GONE` if the
  name is not listed and has no pending replace. It merges per FR-402: the runtime wins where it positively
  reports (respecting `inspectReportsHeaders` / `authStatusReportsScopes`); `CHANGED_ON_HOST` only on a
  normalised difference; a runtime-added `offline_access` is left out of the pre-fill; `NOT_RECOVERABLE` is
  named for fields with no source; `unsupported_settings` lists resource, SSRF override and image kinds. Set
  `has_record`, `attach_by_default`, `auth_state` and `pending_replace` (with
  `pending_removal_failed_listed` / `_unknown` from `ListGlobal`). It returns `UNAVAILABLE` "being saved, try
  again" when the name is locked, and never waits. Tests cover each FR-402 rule and US1 #2, #8, #9.
- [ ] T019 [US1] `M/definition.go` redaction (FR-423, US1 #9): every reported credential header runs through
  `CheckCredentialValue` before it is placed in the view. A literal is replaced with `Redact` and flagged
  `LITERAL_ON_HOST`. A record template reported without a placeholder follows `inspectReturnsTemplates`:
  true ⇒ credential header redacted and flagged, non-credential header `CHANGED_ON_HOST`; false ⇒ *not
  reported*, flagged `RESOLVED_UNCHECKABLE`. Test: the literal string never appears in the marshalled view.
- [ ] T020 [US1] `M/definition.go` fingerprint (research R5): `fingerprint(def, reported, pendingRev)` =
  key id ‖ HMAC-SHA-256 over a length-prefixed canonical encoding (deterministic proto marshal of the record;
  raw reported header lines sorted; reported scopes; unsupported names; pending revision). `checkFingerprint`
  in `M/save.go` recomputes it under the lock from fresh reads and refuses with `STALE_RESTARTED` (key id
  differs) or `STALE_CHANGED`. Tests: a host-side change to a redacted literal changes the fingerprint; a
  restart (new manager) yields `STALE_RESTARTED`.
- [ ] T021 [US1] `M/save.go` replace path (FR-404, FR-406, research R7): `classify` uses
  `mcprules.RegistrationChanged`. `removeStep` runs `rm --force` via `runBounded`; on failure it re-lists
  under the lock: absent ⇒ continue; present ⇒ outcome `STILL_REGISTERED`, stop. `addStep` runs the add via
  `runBounded`; failure or timeout ⇒ never re-checked, `add_started` = true. Refuse a replace without
  `confirm_discard_authorization` unless `auth_state` is `UNAUTHORIZED`, and an unsupported setting without
  `confirm_drop_unsupported` (FR-402). Edit of a vanished name ⇒ `GONE` with nothing removed (edge case).
  Kind change is allowed (edge case). Failure-injection tests for each branch with the scripted fake `sbx`.
- [ ] T022 [US1] `M/pending.go` (research R8): `holdPending` on a replace that ends `NOT_REGISTERED` (mark moved
  out of `marks`; `previous` = last live definition; revision+1), `Manager.CancelPending(ctx, name, rev)`
  (best-effort removal of `removal_failed` names with a re-check, then drop pending, mark and definition;
  never refused because a removal failed; returns left-on-host and unconfirmed names), and
  `endByAnotherPath` called from `reconcile` when a pending name is listed and unlocked (drop all three; the
  event outcome names the `removal_failed` secrets). `Add`/new-server `Save` of a pending name ⇒
  `PENDING_EXISTS`. Retry: `Save` with `retry_of_revision` checks the revision (`PENDING_ENDED` when gone);
  the pre-step removes carried `removal_failed` names left blank (still listed or unreadable ⇒
  `PENDING_REMOVAL_FAILED`, no change); then it runs as a new-server save compared with `previous`. Success
  drops the pending replace; failure updates it. Tests for every transition in data-model.md.
- [ ] T023 [US1] `G/mcp_rpcs.go`: handlers for `GetMcpServerDefinition` and `CancelMcpPendingReplace`; and
  `ListMcpServers` returns `pending_replaces`. Tests in `G/mcp_rpcs_test.go`.
- [ ] T024 [US1] FR-425 launch skip (research R12): `ResolveAttach` returns `BeingReplaced` (marked names
  locked by a replace, plus pending names whose held mark is on). `SB/manager.go` `McpAttach` carries it;
  `applyMcp` logs `mcp: <name> skipped: being replaced` and records `Sandbox.mcp_skipped`
  (`SKIPPED_BEING_REPLACED`). New `SB/manager_mcp.go` `SweepMcpSkipped(name, registered bool)` changes entries
  to `NOT_ATTACHED_AT_LAUNCH` (registered) or clears them, emitting `sandbox_changed`. `AttachMcpServer`
  clears a matching entry. Wire `onReplaceEnded` in `cmd/sxbd/main.go` (retry success ⇒ true; cancel or
  another path ⇒ false). Update both callers (`G/sandbox_rpcs.go`, `G/orchestrator_rpcs.go`). Tests in
  `SB/manager_mcp_test.go` and `M/manager_test.go`.
- [ ] T025 [US1] FR-419 data: the save outcome's `attached_sandboxes` lists this daemon's sandboxes whose
  `mcp_servers` contain the name, plus FR-425 skipped ones (`skipped_at_launch`), with `exclusive` from
  `mcp_attach_mode`. Read through a `Sandboxes` lister interface given to the manager in `cmd/sxbd/main.go`.
  Test.

### TUI

- [ ] T026 [US1] `UI/mcp_form.go` (new): move the form out of `UI/mcp.go` and make it the add/edit form.
  `mcpFormVals` gains `scopes`, `mode` (add/edit/retry), `opened` (baseline definition), `view`
  (`*pb.McpDefinitionView`) and `secrets map[string]string`. Builders are split into `fillMcpVals(view)` +
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
  pending`. Help bindings updated. Tests.
- [ ] T028 [US1] `UI/mcp_form.go` save sequence: client rules → confirmations in the contract's order, each
  `y`-only through `confirm.go` with back-out reopening the form: dropped settings (FR-402), local execution
  for any resulting host-command definition (FR-407), authorization will be discarded (FR-404). Then
  `SaveMcpServer` with the fingerprint, `confirm_*` and `acknowledge_local_execution` set. Kind change clears
  the remote-only fields with a status line. Tests for each confirm and its back-out.
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
- [ ] T032 [US1] `UI/formguard.go`: the edit form uses the `gMcp` surface with `opened` as the baseline;
  non-empty masked fields count as dirty. Test in `UI/formguard_test.go`.

**Checkpoint**: US1 independently testable (Scenario 1, Scenario 5, Scenario 6.1/6.3).

---

## Phase 4: User Story 2 — Set a server's secrets from the MCP form (P1)

**Goal**: One masked field per placeholder; values are stored by the save, in the right order, with
rollback and a conversion offer for literal credentials.

**Independent Test**: quickstart Scenario 2. Add a server with `Authorization: Bearer ${api-key}`, enter
the value, save, and check the Secrets view lists `mcp:<server>:api-key`.

### Daemon

- [ ] T033 [US2] `M/save.go` `snapshot` + `storeStep` (FR-410, FR-412): `ListGlobal` under the lock right
  after the fingerprint check; stores via `SetQuiet` in placeholder order, then the client secret. A new
  server or switchboard-only save runs the stores before the add (or alone); a replace runs them after the
  remove and before the add. The add runs only if every store succeeded. `secret_values` keys must be
  `mcp:<name>:<placeholder>` for a placeholder in the definition or `mcp:<name>:client_secret` with a client
  ID present; anything else ⇒ `INVALID_ARGUMENT`. Multi-line values are refused. Each secret is classified
  created or overwritten from the snapshot. Argv-order tests.
- [ ] T034 [US2] `M/save.go` `rollbackStep` (FR-406, FR-412): after a failed store or add, remove created
  secrets, plus overwritten ones for a replace or a new server's add. For a switchboard-only save, remove
  created ones only and name the overwritten ones as changed. Each failed removal is re-checked against a
  fresh `ListGlobal`. Fill `rolled_back`/`rollback_failed` and, for a replace, the pending replace's names.
  Failure-injection tests for each branch.
- [ ] T035 [US2] FR-413 removal (research R13): `Manager.RemoveSecrets(ctx, name, secrets)` under the lock;
  only names of the server's own placeholder, client secret, or identity forms are allowed; `ErrAuthInProgress`
  when it touches the client secret or identity during an authorization; failed removals are re-checked.
  `G/mcp_rpcs.go` handler. The save outcome's `unused_secrets` names stored secrets whose placeholder the
  edit removed. Tests.
- [ ] T036 [US2] SC-007 leak test in `M/save_test.go`: run every save path (success, each failure, rollback,
  retry, cancel) with distinctive secret values, and assert that none appears in the fake `sbx` argv log, any
  error string, the outcome, the emitted events, the registry bytes, or captured `log` output.

### TUI

- [ ] T037 [US2] `UI/mcp_form.go` secrets group: rebuilt from the definition group each time it is entered,
  keeping values by secret name. The headers field's `DescriptionFunc` lists the derived `mcp:<server>:<p>`
  names live. One `EchoModePassword` input per placeholder, with a status from the view (`set (blank keeps
  it)`, `missing`, `must be re-entered (URL changed)`, `removal failed; will be removed unless re-entered`).
  When secret storage is unavailable the fields are disabled and show the reason. Placeholder rule errors
  name the placeholder (FR-408). Tests (US2 #1, #3, #7).
- [ ] T038 [US2] Literal-credential conversion (FR-423, US2 #8, #9): on leaving the headers field and again on
  save, each credential header with literal text opens a `y`-only offer that applies `MR.Convert`, moves the
  literal into the new secret field, and keeps that field editable. Declining keeps the form open with the
  refusal. URL/argument credential warning (US2 #10) as a `y`-only confirm. A save that would re-register
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

- [ ] T041 [US3] `M/save.go` `identityStep` (FR-415, research R9): after a successful add (or a
  switchboard-only save), clear the binding with `RemoveQuiet(IdentityName)` when any of these holds: a
  client change with a client secret stored in this save; a `rebind_pending` server with a client secret
  stored; or no record and a client secret stored. Otherwise set `LEFT_IN_PLACE` + mark on a client change
  without a secret, or `NOT_TOUCHED`. A failed clear is re-checked with a fresh `ListGlobal`: absent ⇒
  `CLEARED`; listed ⇒ `NOT_CLEARED` (error verbatim); unreadable ⇒ `UNCONFIRMED` (both errors verbatim). The
  mark stays or is set unless cleared. A failed/refused save before this step reports `NOT_TOUCHED` and sets
  no mark (FR-404 precedence). The FR-424 exclusion (via T009) covers any save storing or removing
  `client_secret`/`:identity`. Table tests for every row of the FR-404 outcome rules.
- [ ] T042 [US3] `M/save.go`: when the outcome is `LEFT_IN_PLACE` and `mcp:<name>:client_secret` is stored,
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

- [ ] T045 [US4] Daemon: `Save` and `Add` pass `definition.scopes` / `AddMcpServerRequest.scopes` to
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

- [ ] T047 [P] E2E: extend `E2E/harness.go` `stubSbx` with `mcp inspect --json` (from a per-name state file
  written on `mcp add`), `secret ls/set/rm` state (set reads stdin), and an `mcp add` failure for URLs
  containing `refuse.invalid`. Add `E2E/mcp_e2e_test.go` tests: edit URL + header secret (US1/US2), and a
  failed replace → pending row → Retry previous → Cancel (FR-406/FR-426). Run `make e2e`.
- [ ] T048 [P] FR-421: every save, edit, secret store/removal and cancel refreshes the gateway and Secrets
  views on another connected client (`G/` event test, plus a TUI test that the `mcp_gateway_changed` outcome
  updates a second open view).
- [ ] T049 [P] Update `specs/008-mcp-gateway-manager/contracts/sbx-mcp-cli.md` and `sbx-secret-cli.md` with
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
