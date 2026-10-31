# Feature Specification: MCP Page Quality of Life — Edit, In-Page Secrets, Scopes

**Feature Branch**: `jamesloganclark/swi-21-feature-update-mcp-page`

**Created**: 2026-10-30

**Status**: Draft

**Input**: Linear SWI-21 "Feature - Update mcp page": "We want to update the mcp page in the daemon tab
with some quality of life changes: 1. We want to be able to edit mcp entries after they have been added.
2. We want secret creation for the mcp entries to happen within the mcp page (so the user doesn't have to
manually go to the secrets tab and enter them manually). 3. When adding a client_id we also have to set a
`client_secret` in the secrets store. We want the mcp page to handle this itself. 4. We want to support the
scope field which is required for some oauth flows. 5. We want to give the oauth callback url for the mcp
gateway to be a static port (if possible) so that oauth flows that require safelisted callback urls can
succeed." (Item 5 is deferred to SWI-22; see Clarifications.)

**Builds on**: `specs/008-mcp-gateway-manager` (the MCP gateway view FR-073–FR-079, headers FR-103,
Secrets FR-104, OAuth client ID / authorization server FR-105 from SWI-20). Requirements here are
numbered FR-401 onward so they never collide with 008's.

## Background *(context for reviewers)*

The facts below come from the host runtime's documentation (Docker Sandboxes *MCP gateway*, read
2026-10-30) and from the current behaviour of the MCP gateway view. They decide what the feature can do.

- The runtime has **no edit verb** for a registration. Its documented way to change a registration is
  *remove, then add again*. Removing an OAuth-backed server **deletes its OAuth access token**, so the
  server has to be authorized again afterwards. A pre-registered client's secret and its identity binding
  stay in the host credential store across a remove.
- The runtime's server list does **not** show a remote server's address, headers, client ID or scopes.
  The runtime offers an **inspect** command for one server, documented to show its header templates and
  whether their secrets are set (never their values) and its resource identifier. The authorization status
  reports the default scopes recorded at registration. Switchboard does not keep a copy of what it
  registered today.
- `sbx mcp rm` deletes the OAuth access token **before** it removes the registration, so a failed remove
  can leave the server registered without its token.
- The runtime binds each header secret to the server's endpoint (a managed `:endpoint` record). After the
  endpoint changes, the CLI asks for the secret to be set again.
- Header placeholders `${name}` read the host secret `mcp:<server>:<name>`. A confidential client's secret
  is the host secret `mcp:<server>:client_secret`, and it must be stored **before** the server is
  registered, or the runtime silently skips authorization. When a server is re-registered with a
  **different client**, the runtime refuses the old secret until the developer stores the new client's
  secret **and** clears the binding record `mcp:<server>:client_secret:identity`. The documented order is
  to store the new secret first, then clear the binding.
- Registration accepts a repeatable **scope** setting that records the default OAuth scopes requested at
  authorization. Authorization accepts scopes that override the defaults for one run, or *no scopes*. With
  recorded defaults the runtime also requests `offline_access` when the server advertises it. One-time
  scopes are requested exactly as given.
- During authorization the runtime listens for the OAuth callback on a loopback port on the daemon's host
  and puts it in the link's `redirect_uri` (`http://localhost:<port>`). The documentation does not say how
  that port is chosen or whether it can be fixed. Providers that only accept safelisted callback URLs
  (exact host **and port**) fail when the port changes between attempts.

## Clarifications

### Session 2026-10-30

- Q: Ticket item 5 asks for a static OAuth callback port, but the runtime documents no way to fix it.
  What should this feature commit to? → A: **Defer to a follow-up ticket (SWI-22).** SWI-21 ships items 1–4 only.
  The fixed callback port is out of scope here (see Assumptions).
- Q: After an edit replaces a registration, what happens to sandboxes that already loaded the server?
  → A: **Tell, don't touch.** List this daemon's sandboxes that have the server attached and say they pick
  up the new definition after a restart or a re-attach. Nothing is stopped or reloaded (FR-419).
- Q: Edit flow: how does an edited registration reach the host, given the runtime has no edit verb?
  → A (informed default): a change to the registration is a **replace** (remove, then add with the new
  definition), as 008's FR-077 already requires, behind a warning when the server's authorization will be
  lost. A change to switchboard-only settings (attach by default) never re-registers.
- Q: Can a server be renamed through edit? → A (informed default): **No.** The name is shown but not
  editable. Host secrets are keyed by server name and their values cannot be read back, so a rename could
  not carry them over. Renaming stays *remove + add under the new name*.
- Q: What does the form do with secrets that already exist? → A (informed default): it shows whether
  each one is **set** or **missing**, never a value. A secret left blank keeps the stored value. A secret
  that is missing and has no value entered is reported before saving.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Edit a registered MCP server (Priority: P1)

A developer registered a remote MCP server with the wrong URL, or now needs to add a header, change its
client ID or add scopes. Today they must remove it and add it again from scratch, retyping everything. With
this feature they highlight the server on the MCP gateway view, open **Edit**, see the form pre-filled with
the current registration, change what they need and save.

**Why this priority**: editing is the most-requested gap. Every other story in this feature (secrets,
scopes, client secret) also has to work on an existing server, which needs the edit form first.

**Independent Test**: register a remote server with one header, edit it to change the URL and add a
second header, save, and confirm that the gateway view and the host's registration show the new
definition. The attach-by-default mark and the server's sandbox records are unchanged.

**Acceptance Scenarios**:

1. **Given** a remote server registered through switchboard, **When** the developer opens Edit on it,
   **Then** the form shows its kind, URL, headers, client ID, authorization server, scopes and the
   attach-by-default mark as last registered, and the name is shown but cannot be changed (FR-401,
   FR-402).
2. **Given** a server registered outside switchboard (for example, by hand on the host), **When** the
   developer opens Edit, **Then** the form is pre-filled from what the runtime reports for that server.
   Any field it cannot read back is left empty and the form names those fields (FR-402).
3. **Given** the developer changes only the attach-by-default mark, **When** they save, **Then** the
   server is not re-registered and its authorization is untouched (FR-403).
4. **Given** the developer changes the URL of an authorized OAuth server, **When** they save, **Then**
   the form first warns that the server will need to be authorized again, marks its header secrets as
   *must be re-entered*, and proceeds only on confirmation. After the replace, the developer is offered
   the authorize step at once (FR-404, FR-405, FR-409).
5. **Given** a step of the replace fails on the host, **When** the error comes back, **Then** the
   runtime's message is shown verbatim, the form stays open with the developer's edits, and the
   developer is told whether the server is *still registered, authorization may be lost* or is now
   **not registered** (FR-406). A remove that reports a failure is re-checked against the server list
   once its process has exited, and counts as succeeded if the name is gone. A failed or timed-out add
   is never re-checked: the host's state is treated as unknown, every secret the save stored (created
   and overwritten) is removed and named, and, if the add was started, the developer is told it may
   still complete on the host (FR-406, FR-412). The save can then be retried, from scratch, with the
   edited definition or with the **previous definition**: the removed secrets are named and entered
   again for the retried definition (their old values are gone, even for the previous definition),
   and a secret whose removal failed is never kept blank by a retry (a cancel or discard tries to
   remove it and names any still left on the host for the developer to remove). Discarding the form asks for a `y`-only confirmation that names the server
   as not registered, says its attach-by-default mark and switchboard's record of it will be
   dropped, and names, as ones it will remove, any *removal failed* secrets still in the host's
   global secret list (all of them as *may be left on the host* if the list cannot be read) (FR-406). If the failed add
   finishes on the host later, the server shows as *registered by another path*: its mark and
   switchboard's record of it are dropped, and Edit shows only what the runtime reports (FR-406).
   **Given**
   the saving TUI quit or lost its link, **When** any client opens the gateway view, **Then** the server
   shows as *not registered (replace failed <age> ago)* with **Retry** and **Cancel** (FR-426).
6. **Given** a host-command server, **When** the developer edits its command, arguments or working
   directory, **Then** the local-execution warning (008 FR-076) is shown again before anything is
   registered (FR-407).
7. **Given** another client changed the server after this form was opened, **When** the developer saves,
   **Then** the save is refused with no change and the form reloads in place: fields the developer
   edited keep their values (flagged where the host also changed them), the others take the host's
   values, and an entered secret is dropped and must be re-entered when the reload replaces the URL
   (header secrets) or the client ID, authorization server or URL (client secret) (FR-404).
8. **Given** a server registered by hand with a setting the form cannot express (for example, a resource
   override), **When** the developer opens Edit, **Then** it is refused with the reason, or the developer
   must confirm that the replace drops that named setting (FR-402).
9. **Given** a server switchboard registered and that was later changed by hand on the host (for
   example, a header added with the runtime's CLI), **When** the developer opens Edit, **Then** each
   field where the host's registration differs from switchboard's record is flagged, pre-filled with the
   host's value, and named in the form, so saving never silently reverts the host-side change. **Given**
   a server saved with default scopes whose authorization status reports no scopes (or whose inspect
   output has no header section), **When** Edit opens, **Then** the form shows switchboard's recorded
   scopes (or headers), not flagged. A host value that differs only in order, duplicates, a
   runtime-added `offline_access`, header-name case or whitespace is not flagged (FR-402).
   **Given** the host reports a credential header holding a literal value (for example,
   `Authorization: Bearer sk-live-…` added by hand), **When** Edit opens, **Then** the value arrives
   redacted (`Bearer ••••`) and flagged *literal credential on host*, never in clear on any client, and
   the form refuses a save that would re-register the server until the header is converted to a
   placeholder and its secret entered. A save that only changes the mark or stores secrets is allowed
   (FR-403, FR-423).
10. **Given** an authorization of the server is in progress, **When** the developer saves an edit that
    would replace it, or that would store or remove `mcp:<server>:client_secret` or clear its binding
    record (including a secrets-only save, or the FR-413 removal offered after a save), **Then** it is
    refused with no change and a message saying to finish or cancel the authorization first. A
    save that only stores header secrets is allowed. **Given** a save or replace of the server is running, or a pending
    replace holds its name, **When** the developer chooses **Authorize** or **Authorize with
    scopes…**, **Then** it is refused with a message saying the server is being replaced (FR-424).
11. **Given** a sandbox is launched while a replace of a marked server is running or pending, **When**
    the launch output reports the server, **Then** it is reported as *skipped: being replaced* (not as a
    stale mark), the mark is kept, and the skip is recorded on the sandbox. **When** the pending
    replace ends with a retry that registers the server, **Then** the sandbox's entry says the server
    was not attached at launch and can be attached from its MCP overlay, and the sandbox is in the
    FR-419 list. **When** it ends with a cancel or a registration by another path, **Then** the entry
    is cleared (FR-425).

---

### User Story 2 - Set a server's secrets from the MCP form (Priority: P1)

A developer adds a remote server whose `Authorization` header uses `${api-key}`. Today they must leave the
MCP page, open Secrets, store `mcp:<server>:api-key` under exactly that name, and come back. With this
feature the MCP form finds every `${placeholder}` in the headers and shows a masked value field for each,
labelled with its secret name. Saving stores the secrets on the daemon's host and then registers the
server.

**Why this priority**: a mistyped secret name is the most common way a credential header fails silently.
Doing it in one place removes that error class.

**Independent Test**: add a remote server with header `Authorization: Bearer ${api-key}`, enter a value in
the generated field, save, and confirm that the Secrets view lists `mcp:<server>:api-key` (masked) and the
server is registered. No visit to the Secrets view is needed.

**Acceptance Scenarios**:

1. **Given** headers that contain `${api-key}` and `${org}`, **When** the developer reaches the secrets
   part of the form, **Then** one masked field is shown for each distinct placeholder, labelled with its
   full secret name `mcp:<server>:<placeholder>`, and each shows whether a value is already stored
   (FR-408, FR-409).
2. **Given** values entered for the placeholders, **When** the developer saves, **Then** every entered
   secret is stored on that daemon's host **before** the server is added (for a replace: after the
   remove, before the add), and the server is added only if every store succeeded (FR-410).
3. **Given** a placeholder whose secret already exists, **When** the developer leaves its field blank,
   **Then** the stored value is kept unchanged (FR-409).
4. **Given** a placeholder whose secret is missing and no value is entered, **When** the developer saves,
   **Then** the form names the missing secret and asks whether to register anyway (the header will not
   work until it is set) or go back (FR-411).
5. **Given** a secret store fails part-way through a save, or the add after the stores fails,
   **When** the error comes back, **Then** nothing is added, the error is shown, secrets this save
   **created** are removed again, and
   secrets it **overwrote** are reported by name as changed (FR-412).
6. **Given** an edit removes a header whose placeholder secret is stored, **When** the save completes,
   **Then** the developer is told that the secret is no longer used and is offered its removal. It is
   never removed silently (FR-413).
7. **Given** a placeholder named `client_secret`, or one containing `:` (for example
   `${client_secret:identity}`), **When** the developer saves, **Then** the form refuses it with a
   message naming the placeholder and the reserved name it would collide with, and the daemon applies
   the same rule (FR-408).
8. **Given** an `Authorization` header typed with a literal credential (no placeholder), **When** the
   developer leaves the header field, **Then** the form offers to turn the value into a `${placeholder}`
   with a masked secret field holding the typed value, and saving with the literal value is refused, so
   the credential never reaches switchboard's record. A value that mixes a placeholder with other
   literal text (`Bearer sk-live-abc ${x}`, `session=abc; csrf=${t}`) is refused the same way, while
   `ApiKey ${k}` and `Cookie: session=${session}` are accepted (FR-423).
9. **Given** a non-secret header whose name matches the credential rule (for example
   `X-Auth-Mode: oauth`), **When** the developer saves, **Then** it is refused like any literal
   credential, and the same one-step conversion moves the value into a placeholder secret. There is no
   override (FR-423).
10. **Given** a URL with a credential-looking query parameter (`?api_key=…`) or host-command arguments
    with a credential-looking flag (`--token=…`), **When** the developer saves, **Then** the form warns
    that the value is stored in plain text in switchboard's record and shown to every client, and
    points to placeholders (remote) or environment-based secrets (host command) (FR-423).

---

### User Story 3 - Pre-registered OAuth client with its client secret (Priority: P1)

A developer adds Slack, which has no Dynamic Client Registration, so they enter a client ID. Today the form
only tells them to go and store `mcp:slack:client_secret` under Secrets first. With this feature the form
has a masked **client secret** field next to the client ID. Saving stores it under the right name before
registering, and clears the old client binding when the client changes.

**Why this priority**: SWI-20 made pre-registered clients possible, but the ordering (secret first) and
the identity-binding step after a client change are easy to get wrong. Getting them wrong leaves a server
that silently never authorizes.

**Independent Test**: add a remote server with a client ID and a client secret, then confirm that the
secret `mcp:<server>:client_secret` is stored before registration and that authorizing reaches the
provider's consent page.

**Acceptance Scenarios**:

1. **Given** a client ID is entered, **When** the form is shown, **Then** a masked client-secret field
   appears beside it, says the value is needed only for confidential clients, and shows whether
   `mcp:<server>:client_secret` is already stored (FR-414).
2. **Given** a client secret is entered, **When** the developer saves, **Then** it is stored as
   `mcp:<server>:client_secret` on that daemon's host before the server is added, and for a replace
   only after the old registration was removed, so a failed remove never leaves the old client paired
   with the new secret (FR-410, FR-414).
3. **Given** an edit changes the client ID, the authorization server, or the URL of a server with a
   pre-registered client and enters the new client's secret, **When** the save completes, **Then**
   switchboard stores the secret, then clears the binding record `mcp:<server>:client_secret:identity`,
   so the next authorization binds the new client, and the developer is told this happened (FR-415).
4. **Given** the client ID is changed but no new client secret is entered while one is stored, **When**
   the developer saves, **Then** the form warns that the runtime will refuse to authorize the new client
   while the old client's binding is in place and that entering the new client's secret and saving
   clears it (or, for a public client, removing the stored secret and its binding record), and the
   binding is left in place and the server is marked *client re-bind pending*, so the old secret is
   never offered to the new client (FR-415).
5. **Given** the client ID is cleared on edit while a client secret is stored, **When** the save
   completes, **Then** the developer is offered removal of `mcp:<server>:client_secret` and its binding
   record (FR-413).

---

### User Story 4 - OAuth scopes (Priority: P2)

Some providers only grant tokens when specific scopes are requested. A developer adds those scopes on the
MCP form. They are recorded as the server's default scopes and requested at every authorization. A
developer can also authorize once with a different set of scopes, or with none, without changing the
recorded defaults.

**Why this priority**: it is needed for some providers but not most. Servers without scopes keep working
as today.

**Independent Test**: register a remote server with scopes `read` and `write`, authorize it, and confirm
the authorization link requests those scopes (plus `offline_access` if the server advertises it, as the
runtime adds it to recorded defaults).

**Acceptance Scenarios**:

1. **Given** a remote server form, **When** the developer enters scopes, **Then** they are recorded as
   the server's default scopes on registration, one runtime scope setting per scope (FR-416).
2. **Given** scopes with whitespace, control characters, a `,`, a leading `-`, duplicates or an over-long value,
   **When** the developer saves, **Then** the form refuses them with a message and stays open, and the
   daemon applies the same rule (FR-417).
3. **Given** a registered server, **When** the developer chooses **Authorize with scopes…**, **Then** they
   can enter scopes for this authorization only, or choose *no scopes*. The recorded defaults are
   unchanged (FR-418).
4. **Given** a host-command server, **When** scopes are entered, **Then** they are refused (FR-417).
5. **Given** the runtime refuses some requested scopes and suggests a retry, **When** authorization ends,
   **Then** the runtime's text, including its suggestion, is shown verbatim (FR-418).

---

### Edge Cases

- **Edit of a server that disappeared**: the server was removed on the host (or by another client) while
  the form was open. Save reports that it no longer exists and offers to add it as new. Nothing is
  removed.
- **Concurrent edit from two clients**: replaces of one name are serialised on the daemon, and a save
  whose form was opened on a definition that has since changed is refused (FR-404). The gateway view of
  every client refreshes on the existing `mcp_gateway_changed` event.
- **Kind change on edit** (remote ↔ host command): allowed as a replace. Every remote-only field (headers,
  client ID, authorization server, scopes, secrets) is cleared from the form, and the local-execution
  warning is shown.
- **Sandboxes that already loaded the server**: the runtime keeps the old definition in sandboxes that
  already loaded it. FR-419 covers what the developer is told; nothing is stopped, unloaded or reloaded.
- **Secret values**: never shown, logged, persisted by switchboard, echoed in errors, or placed in a
  process argument (008 FR-104 applies unchanged). They are single-line, and a multi-line value is
  refused.
- **Placeholder names**: a `${…}` placeholder that breaks the placeholder name rule (no `:`), or that
  is the reserved name `client_secret`, is refused before saving, with a message naming it (FR-408).
- **Literal credentials in headers**: a credential header (matched on name segments) must take its
  credential from placeholders, with at most a leading auth-scheme token (and cookie names in
  `Cookie`). The form offers to convert a typed literal value into a placeholder and secret, which is
  also the way through for a header that only looks like a credential. A literal credential found on
  the host is redacted by the daemon before any client sees it (FR-423).
- **Credentials in URLs or host-command arguments**: stored in plain text in switchboard's record;
  the form warns and points to placeholders or environment-based secrets (FR-423).
- **Client drops mid-save**: the TUI quits or its SSH link drops while a save is running. The daemon
  finishes the save, including rollback and the identity clear, and the outcome is visible to every
  client through the gateway-changed and secrets-changed events (FR-404). If it ends *not registered*,
  the pending replace is a row on every client's gateway view with Retry and Cancel (FR-426).
- **Gateway or Secrets unavailable**: the host runtime is missing or below the baseline. Edit is
  unavailable with the reason (008 FR-079). If only secret storage is unavailable, the secret fields are
  shown disabled with the reason, and saving a form that needs a new secret is refused.
- **The orchestrator's per-worker server** (`sxb-orch-…`) stays hidden and is never editable.
- **Leaving the form with unsaved edits** goes through the shared unsaved-changes guard (SWI-9). Masked
  secret fields count as edits when they are non-empty.

## Requirements *(mandatory)*

### Editing a registration

- **FR-401**: The MCP gateway view MUST offer **Edit** on any registered server (except the hidden
  per-worker orchestrator servers) through a single documented keystroke. Edit opens the same form used
  to add, in an edit mode in which the server's **name is shown but not editable**.
- **FR-402**: The edit form MUST be pre-filled with the server's current definition: kind, URL or
  command/arguments/working directory, headers, client ID, authorization server, default scopes, and the
  attach-by-default mark. Switchboard MUST remember the definition of every server it registers or
  replaces (per daemon, beside the existing attach marks). When the form opens, the daemon MUST also
  read what the host runtime reports for the server: header templates from the per-server inspect, and
  default scopes from the authorization status, which is documented to report them (the 2026-10-10
  real-host run saw only name and status there, so in practice this may come back empty). For a field
  the runtime **positively reports**, the **runtime's value wins**: where it differs from switchboard's
  record, the form MUST pre-fill the runtime's value and flag the field as *changed on the host*, so a
  save never silently reverts a host-side change. A field counts as *not reported* (and comes from
  switchboard's record, unflagged) when its key or section is missing, cannot be parsed, or is an empty
  list from a source not yet confirmed on a real host to report that field (FR-422); until then an
  empty scope list from the authorization status or an empty header section from inspect is *not
  reported*, never "the host has none". Before flagging, both sides MUST be compared in normalised
  form: scopes sorted and de-duplicated, ignoring an `offline_access` the runtime added to recorded
  defaults; headers by case-insensitive name with the value trimmed around `:`. A difference only in
  presentation is not a change. A runtime-added `offline_access` (one not in switchboard's record) MUST
  also be left out of the pre-filled scopes, so a later save never records it as a default. Fields the runtime does not report come from switchboard's
  record. For a server switchboard has no record of, only the runtime's fields are available. Any field
  that cannot be recovered MUST be left empty and named in the form, so that saving does not silently
  drop it. Host-side changes to fields the runtime does not report cannot be seen; that is accepted. If
  the runtime reports a setting or kind the form cannot express (for example, a resource override, an
  SSRF-check override, or a registry/image-based local server), Edit MUST either refuse with the reason
  or require the developer to confirm that the replace will drop each named setting.
- **FR-403**: Saving an edit that changes only switchboard-side settings (the attach-by-default mark)
  and/or secret fields MUST NOT re-register or re-authorize the server (008 FR-077). It updates the mark
  and stores the entered secrets (FR-410), and applies FR-415 when it applies.
- **FR-404**: Every save from the MCP form (add, edit, or secrets-only) MUST be **one daemon operation**
  (a *save*), not a series of client calls. Its request carries the definition, the mark, the
  fingerprint of the definition the form was opened with, and the entered secret values (proto field →
  CLI standard input only, as in 008 FR-104). The daemon runs every step of it under a per-server-name
  lock that also serialises add, remove, authorization starts and other saves of that name, in this
  order: definition check → secret-list snapshot → secret stores (FR-410) → remove + add when the
  registration changes → identity clear (FR-415) → rollback on failure (FR-412). The save is not tied to
  the client's connection: if the client goes away, the daemon finishes it. The response (and the
  gateway-changed event) reports the outcome: which secrets were created, overwritten or rolled back,
  the identity binding outcome as one of *not touched* (no clear attempted in this save), *left in
  place* (this save changed the client without a new client secret, so FR-415 marks it *client
  re-bind pending*), *cleared*, *not cleared* or *unconfirmed* (may still be in place) (FR-415; not a
  yes/no), and the resulting registration state, including whether the server is marked *client
  re-bind pending*. *Not touched* says only that this save attempted no clear: no clear is needed,
  the save failed or was refused before the identity-clear step, or the server is already marked
  and this save stores no client secret (FR-415 retries the clear only on a save that stores it;
  round-17 open item). *Left in place* takes precedence whenever this save completed a client change
  under FR-415's no-new-secret branch, whether or not the server was already marked; a save that
  fails or is refused before the add reports *not touched*. It never means the binding is gone or the mark is cleared; the mark is
  reported in the resulting state, not in this outcome. The TUI only renders it. A save that changes
  the registration is carried out as a **replace**: remove the registration, then add the edited
  definition, always registering without authorizing. It keeps the attach-by-default mark and
  switchboard's definition across the remove and add (a list taken
  mid-replace MUST NOT drop them), and emits one gateway-changed event when it finishes. The save MUST
  be refused, with no change, when the server no longer matches what the form was opened with
  (switchboard's stored definition and the runtime-reported fields, FR-402); the check runs under the
  lock before any secret is stored. The fingerprint is computed by the daemon over the
  **unredacted** runtime-reported fields as a keyed MAC (HMAC-SHA-256) under a random key the daemon
  generates at start, keeps in memory only and never sends to a client, so a client cannot use it to
  test guessed values of a redacted literal. It is returned with the definition read and echoed back
  by the form, so a host-side change to a redacted literal (FR-423) is still caught. The MAC covers
  the raw reported value of every header, including one that counts as *not reported* (FR-423). The
  token also carries a random id of the key, so a daemon restart (new key) is told apart from a change:
  a save from a form opened before the restart is refused as *stale (daemon restarted)*, not
  *changed*. On either refusal the form reloads in place: it re-reads the definition, and the re-read
  definition becomes the form's baseline (for FR-402 flags, FR-409, FR-415 and the next fingerprint).
  A field counts as **edited** when its form value differs from the value the form opened with (the
  same comparison as the FR-402 flags; a value typed back to the original is not edited).
  A field the developer did not edit takes the re-read value, flagged *changed on the host* where it
  differs from the one the form opened with. A field the developer edited keeps the developer's value;
  where the host also changed it, it is flagged *changed on the host: was <opened>, host now
  <re-read>* (redacted where FR-423 applies), and the save needs no further confirmation. Entered
  secret values are kept, except that when the reload replaces the form's URL, every entered header
  secret is dropped and marked *must be re-entered*, and when it replaces the form's client ID,
  authorization server or URL, an entered client secret is dropped the same way, so a secret typed for
  one endpoint or client is never stored for another (as FR-406 and FR-415). An entered value whose
  placeholder is not in the form's headers after the reload, or an entered client secret when the
  form's definition after the reload has no client ID, is dropped too, so no secret is stored that nothing references; the
  refusal message names every dropped secret. A pending replace's **Retry** form (FR-406) reloads
  differently: it re-reads the **pending replace**, not the server, and keeps FR-406's comparison
  with the previous definition taken from it. If the
  pending replace has ended (retried from another client, cancelled, or registered by another path),
  the form does not reload: it closes and says how the pending replace ended, and the developer can
  open an ordinary Edit. Changes made directly on the host with the runtime's CLI bypass the
  lock and are caught only as far as this comparison can see them; that is accepted. Before anything is
  sent, the form MUST warn that the server's authorization will be discarded unless its state is known
  to be unauthorized, and MUST proceed only on confirmation.
- **FR-405**: After a successful replace of a remote server, the TUI MUST offer to start authorization
  immediately, using the existing authorization flow (008 FR-075).
- **FR-406**: If the replace fails, the host's message MUST be shown verbatim (008 FR-078), the form MUST
  stay open with the developer's edits, and the developer MUST be told the resulting state:
  *still registered, authorization may be lost* (the remove failed: the runtime deletes the OAuth token
  before the registration) or *not registered* (the remove succeeded and the add, or a store before it,
  failed). When the remove or the add reports a failure (including a timeout), the daemon MUST first
  make sure the CLI process has exited (on a timeout it kills the process and waits for it to exit).
  After a failed **remove**, it reads the runtime's server list again under the per-name lock: if the
  name is not listed the remove counts as succeeded and the save goes on (stores, then add, FR-410);
  only when it is still listed is the outcome *still registered*. No secret has been stored at that
  point (FR-410), so there is nothing to roll back. A timed-out remove that the host finishes later
  shows on the next list as an unregistered server, like any other host-side remove.
  **A failed add is never re-checked or counted as succeeded.** After any failed or timed-out add (or
  a failed store after the remove), the host's state for that name is treated as **unknown**: every
  secret this save stored, created **and** overwritten, MUST be removed (best effort, FR-412), the
  developer is told by name which secrets were removed (and which removals failed), the outcome is
  *not registered*, and, only if the add was started, the developer is told it may still complete on
  the host (a failed store means the add never ran). For a **new server** no pending replace is held
  and no definition is stored: the form keeps the definition and names the removed secrets, and a
  late finish shows the server as one switchboard has no record of. For a replace, the daemon then
  holds a **pending replace** of that name: the server's attach-by-default mark, the definition being
  saved, the **previous** definition, and the **names** (never values) of the secrets this save
  removed and of those whose removal failed. Because nothing this save stored is kept, a retry
  starts from scratch: the removed secrets are named as removed and must be entered again for
  whatever definition is retried (FR-409, FR-411), so a value typed for one endpoint or client is
  never kept for another, even if the earlier add finishes on the host later. A name whose removal
  failed MUST NOT be kept blank by a retry: its field shows *removal failed; will be removed unless
  re-entered* (FR-409), and if the retry enters no value for it, removing it is the retry's **first**
  step (under the same lock, right after the definition check, before any store or add). A removal
  that reports a failure is re-checked against the host's global secret list under the same lock, as
  for a failed server remove: if the name is not listed, it counts as removed (the secret is already
  gone, e.g. removed in the Secrets view or the host CLI), so the daemon never has to recognise the
  runtime's *not found* answer. If the name is still listed (or the list cannot be read), the retry stops with no change: nothing is
  stored, no add runs, and the pending replace is left as it was. The pending replace is persisted beside the
  definitions (it holds no credential-header literals, FR-423), so it survives a daemon restart. It is
  exempt from the stale-mark drop, MUST NOT be applied at sandbox launch (FR-425), and ends only when:
  - a **retry** succeeds (mark and the retried definition become live);
  - the developer **cancels** it: discarding the form, or Cancel on its gateway row (FR-426), each
    behind a `y`-only confirmation that names the server as not registered and says its
    attach-by-default mark will be dropped. Cancel also drops switchboard's stored definition for
    that name, so whatever is later listed under it is a server switchboard has no record of. Under
    the same lock, before dropping the pending replace, Cancel makes a **best-effort removal** of
    every secret it holds as *removal failed* (a failed removal is re-checked against the global
    secret list as for a retry). The confirmation names, as ones the cancel will remove, those of these secrets
    still in the host's global secret list (if the list cannot be read, it names all of them as
    *may be left on the host*).
    Cancel is never refused because a removal fails: the outcome names only the secrets still listed
    (or not confirmed gone) as left on the host, for the developer to remove in the Secrets view or
    the host CLI (a known limitation, see *Out of scope*); or
  - the runtime lists a server of that name (a late finish of the failed add, or a registration
    through the host CLI): *registered by another path*. The held mark is dropped, not applied, no
    FR-415 step runs, the sandboxes' skipped entries are cleared as on cancel (FR-425), the
    gateway-changed event names any secrets the pending replace held as *removal failed* (so the
    developer can check them in the Secrets view), and
    switchboard **drops its stored definition** for that name, so Edit treats it as a server
    switchboard has no record of (FR-402: only runtime-reported fields, the rest empty and named)
    and never pre-fills a URL or client the host may not hold. An add of that name from
    switchboard's own form is refused while the pending replace exists, pointing to Retry.
  A **retry** is an ordinary save (FR-404) from the edit form, pre-filled with the definition being
  saved or, at the developer's choice, the **previous definition** (which is how the previous
  registration is restored; there is no separate restore operation). For FR-409 and FR-415 the retry
  is compared with the **previous** definition (the last one that was live), exactly as the failed
  save was. A failed retry is handled by this same rule and updates the existing pending replace (the
  held definition becomes the retried one, the secret names become the failed retry's own; the
  previous definition stays the last live one). Because carried *removal failed* names are removed
  first or the retry changes nothing, none is lost this way; names an earlier attempt *removed* and
  the retry did not re-enter are not carried (see the review round 9 cut). A Retry
  save's definition check (FR-404) also compares the pending replace the form was opened on, so a
  pending replace updated by another client's retry refuses the save and reloads the form (FR-404).
  Retry and cancel are daemon operations available from every client, not only the form that ran the
  save (FR-426).
- **FR-407**: An edit that results in a host-command definition MUST show the local-execution warning
  and require acceptance again (008 FR-076), even if the server was a host command before.

### Secrets on the MCP form

- **FR-408**: For a remote server, the form MUST list one masked value field for each distinct
  `${placeholder}` in its headers, each labelled with its full secret name
  `mcp:<server>:<placeholder>`. The list MUST follow the header field as it is edited. A placeholder is
  `${` + name + `}`. The placeholder name MUST match its own rule, `^[A-Za-z0-9][A-Za-z0-9._-]*$` (at
  most 128 characters; no `:`, so a placeholder can never address the runtime-managed `:identity` or
  `:endpoint` records), and MUST NOT be the reserved name `client_secret` (the confidential client's
  secret, which would otherwise be sent to the resource server in a header). A placeholder that breaks
  either rule MUST be refused with a message naming it and, for `client_secret`, saying what it collides
  with. The resulting full secret name always satisfies the host secret name rule (008 FR-104). The
  runtime's exact placeholder grammar is documentation-derived (see Assumptions).
- **FR-409**: Each secret field (placeholder secrets and the client secret) MUST show whether the secret
  is currently stored on the daemon's host **in the global scope** (from the host's secret list; never
  the value; a sandbox-scoped secret of the same name does not count). A blank field MUST mean *keep
  the stored value*, except after a URL change, or for a name a pending replace holds as *removal
  failed* (FR-406): while that name is in the host's global secret list, such a field shows *removal
  failed; will be removed unless re-entered* instead of *set*, and saving with it blank MUST name it
  in a confirmation (combined with FR-411's prompt when that is shown) as a secret this save will
  remove; for `client_secret` the confirmation also repeats FR-414's note that it is needed for a
  confidential client. Once it is not listed, the field shows *missing* like any other and only
  FR-411's prompt (or, for `client_secret`, FR-414's note) applies. After a URL change the runtime binds header secrets to the server's
  endpoint, so when an edit changes the URL, every placeholder field MUST be marked *must be re-entered*,
  and saving with any of them blank MUST warn that the header will not be sent until that secret is set
  again. That a blank-kept secret is not sent to the new endpoint relies on the runtime's documented
  endpoint binding (Assumptions, FR-422). After a failed add, switchboard removes the secrets the save
  stored instead (FR-406).
- **FR-410**: On save, every secret with an entered value MUST be stored on that daemon's host by the
  daemon's save operation (FR-404), in the global scope and through the existing secret path (008
  FR-104: request → CLI standard input only), **after** the definition check. For a new server, or a
  save that does not change the registration, the stores run before the add (or alone). For a
  **replace**, the stores run **after the remove succeeds and before the add**, so a failed remove
  never leaves the old registration (old client, old authorization server, old endpoint) paired with
  newly stored secrets, and endpoint-bound header secrets are stored once the old endpoint is gone. The
  add MUST NOT run unless every store succeeded; a store failure after the remove leaves the server
  *not registered* and is handled as FR-406's pending replace, after FR-412's rollback.
- **FR-411**: When a placeholder's secret is missing and no value was entered, saving MUST name each
  missing secret and ask the developer to register anyway or go back. It MUST NOT block a deliberate
  register.
- **FR-412**: If storing secrets fails part-way, nothing MUST be added (and for a save that does not
  change the registration, nothing removed). Whether each secret was *created* or *overwritten* is
  decided from the host's secret list read by the save operation under the per-name lock, immediately
  before the stores. If a store **or the add** fails, the secrets this save created MUST be removed
  again (best effort, with any failure reported; a timed-out add included). For a **replace or a new server's add**, the
  secrets it overwrote MUST be removed too (FR-406): their old values cannot be restored, and their new
  values belong to a definition that is not registered. For a save that does not change the
  registration, the overwritten secrets keep their new values and are named as changed. That a secret set outside this save (from the Secrets view or the host CLI, neither of which
  takes the per-name lock) in the window between the list and the stores could be rolled back is
  accepted.
- **FR-413**: When an edit stops using a stored secret (a placeholder removed from the headers, or the
  client ID cleared), the TUI MUST name that secret after the save and offer to remove it behind a
  confirmation. Switchboard MUST never remove a secret silently. The removal it offers runs as a
  daemon operation under the server's per-name lock (FR-404, FR-424).

### Pre-registered client secret

- **FR-414**: When a client ID is present, the form MUST show a masked **client secret** field that
  stores `mcp:<server>:client_secret`. The field MUST say it is needed only for a confidential client.
  The 008 FR-105 instruction to set the secret under Secrets first MUST be replaced by this field.
- **FR-415**: When a save changes the client ID, the authorization server or the URL of a server with a
  pre-registered client, the save operation (FR-404) MUST clear the binding record
  `mcp:<server>:client_secret:identity` after the add succeeds **only if** a new client secret was
  stored in the same save, and MUST tell
  the developer. If no new client secret was entered, the binding MUST be left in place, the server's
  definition MUST be marked **client re-bind pending**, and the form MUST warn before saving that the
  runtime will refuse to authorize the new client while the old client's binding is in place, and that
  entering the new client's secret and saving clears it, or, if the new client is public (has no
  client secret), removing `mcp:<server>:client_secret` and `mcp:<server>:client_secret:identity` in
  the Secrets view or the host CLI. FR-404 reports this outcome as *left in place*. A later save that
  stores a client secret for a server marked *re-bind pending*, including a secrets-only save
  (FR-403), MUST then clear the binding and the mark; a failed clear that the re-check below does not
  count as success keeps the mark. For a server switchboard has no definition of (so no *re-bind
  pending* flag can be known), a save that stores a client secret MUST clear the binding as well.
  The edit form MUST show the pending state next to the client-secret
  field, worded so it holds for both causes of the mark (no new secret entered, or a failed clear):
  the old binding may still be in place, and saving with the client secret entered clears it and the
  mark (for a public client, the stored secret and its binding record can instead be removed in the
  Secrets view or the host CLI; the mark then stays, round-18 known limitation under *Assumptions*).
  A failed clear of the binding is re-checked with a fresh read of the host's secret list
  (not FR-404's snapshot) under the same lock and counts as success only if the list is read and the
  identity record is not in it (a binding that never existed, or was already removed, is already
  cleared, so the daemon does not rely on recognising the runtime's *not found* answer). If the record
  is still listed, the removal's failure MUST be shown verbatim and the binding described as *not
  cleared*; if the list cannot be read, the removal's failure and the read failure MUST both be shown
  verbatim and the binding described as *unconfirmed* (may still be in place). Either way the
  registration is not rolled back, the server MUST be marked (or stay) *client re-bind pending*, and
  the message MUST lead with the in-product retry (re-enter the client secret and save to retry the
  clear), then name `mcp:<server>:client_secret:identity` as the record that can instead be removed in
  the Secrets view or the host CLI, saying that the mark clears only on the next save that stores the
  client secret
  (whose re-check then finds the record gone). Whether any later save of a marked server should retry
  the clear is an open item for the plan phase (round-17 known limitation).

### Scopes

- **FR-416**: A remote server registration MUST accept an optional list of **default OAuth scopes**,
  recorded with the registration (one runtime scope setting per scope) and shown and editable on the
  edit form.
- **FR-417**: Each scope MUST be a valid OAuth scope token: printable ASCII with no space, `"` or `\`,
  no leading `-`, and at most 256 characters. A `,` MUST also be refused, although OAuth allows it,
  because the runtime may split a scope value on commas (as switchboard already assumes for host-command
  arguments) and documents no escaping; this is lifted only if the real-host run shows the scope flags
  take a comma literally (FR-422). There MUST be at most 32 distinct scopes (this and the
  256-character limit are switchboard's own bounds). Duplicates MUST be refused. Scopes on a host-command server MUST be
  refused. The TUI and the daemon MUST apply the same rule, so a daemon refusal never discards the form
  (the SWI-20 pattern).
- **FR-418**: The gateway view MUST offer **Authorize with scopes…** for a registered remote server. It
  takes a one-time scope list (same rule as FR-417) or *no scopes*, both passed to that one
  authorization only. The recorded defaults are unchanged. The option MUST say that one-time scopes are
  requested exactly as given, so no refresh-token scope is added. The plain authorize action keeps
  today's behaviour, using the recorded defaults. The runtime's output about requested, advertised or
  refused scopes MUST be shown verbatim.

### Cross-cutting

- **FR-419**: After a successful replace, the TUI MUST list this daemon's sandboxes whose records show
  the server attached, and say that each picks up the new definition only after a restart or a re-attach
  from the sandbox's MCP overlay. Switchboard MUST NOT stop, unload or reload any sandbox as part of an
  edit, and the sandboxes' attached-server records MUST be unchanged.
- **FR-420**: Every validation rule the daemon applies to a definition (headers, including FR-423's
  credential-header rule, placeholder names, client ID, authorization server, scopes) MUST also be applied by the TUI before any
  request is sent, so the form stays open on an invalid value.
- **FR-421**: Every completed save, edit, secret store or secret removal MUST refresh the gateway view
  and the Secrets view, and MUST reach other connected clients through the existing
  `mcp_gateway_changed` and `secrets_changed` events.
- **FR-422**: Every new runtime command or flag this feature uses (scope on add, scope and no-scope on
  authorize, the per-server inspect, scopes read from the authorization status, removing the identity
  record) MUST be pinned by an argv-asserting test and listed in the host CLI contract as
  *documentation-derived* until it is run on a real host, together with whether the scope flags treat a
  `,` literally (FR-417), whether the authorization status and inspect positively report scopes and
  headers (FR-402), whether the secret list shows the `:identity` record (FR-415, the round-15
  known limitation), whether a killed `mcp add` / `mcp rm` can still complete on the host (FR-406),
  whether inspect returns header templates or resolved values (the FR-423 behaviour flag), and whether `mcp load` attaches a server to a sandbox launched in exclusive mode
  (FR-425). The new client-facing operations (the save, the cancel of a pending replace, the FR-413
  secret removal, the pending-replace state carried by the server list and the gateway-changed event
  (FR-426), the definition read for the edit form with its redacted-header flag and fingerprint
  (FR-404, FR-423), the sandbox's skipped-server record (FR-425), scopes on add, one-time scopes on
  authorize) are additive to the existing daemon contract; older clients keep working unchanged. This
  feature adds **no** `NotificationKind`.
- **FR-423**: A header is a **credential header** when its name is `Authorization`,
  `Proxy-Authorization` or `Cookie`, or when any of its name segments (the name split on `-` and `_`,
  case-insensitive) is `key`, `apikey`, `token`, `secret`, `password`, `auth` or `credential(s)`. Name
  segments, not substrings, are matched, so `Keycloak-Realm` is not a credential header while
  `X-Api-Key` is. A credential header's value MUST consist only of `${placeholder}`s, optionally led by
  one auth-scheme token from a fixed list (`Bearer`, `Basic`, `Token`, `Bot`, `ApiKey`, `Key`, `SSWS`,
  `DPoP`, `Splunk`, `GenieKey`; case-insensitive) and joined by literal separators (space, `:`, `;`,
  `,`). In a `Cookie` header each placeholder MAY also be led by a cookie name and `=`
  (`<cookie-name>=${…}`, the name an RFC 6265 token), since a cookie name is not a secret. Any other
  literal text is refused by the TUI and the daemon (FR-420), with a message saying which part is
  literal and why; for an unlisted scheme the message says the scheme then has to go inside the secret.
  The list is not open-ended on purpose, so `sk-live-abc ${x}` stays refused. When such a value is
  typed, the form MUST offer to convert it in one step: the value is replaced by the scheme token (if
  any) and a `${placeholder}`, and the rest of the typed value moves into that placeholder's masked
  secret field. The derived placeholder name is the header name lower-cased with `_` mapped to `-`, MUST
  satisfy FR-408, MUST NOT be `client_secret`, and MUST NOT collide with a placeholder already used by
  another header; on collision or an invalid name the form appends `-2`, `-3`, … and the field stays
  editable, so two credentials are never merged into one secret. There is no *not a credential*
  override: a header that matches the rule but carries no secret (for example `X-Auth-Mode: oauth`)
  is converted the same way, and its value is then held as a host secret. Other headers keep their
  values as typed, and the form says that those values are kept in switchboard's record and shown when
  the form opens. Runtime-reported headers (FR-402) go through the same rule **in the daemon, before
  the definition read returns them**: a credential header whose value holds literal text (for example
  one added by hand on the host) is returned redacted (scheme token kept, the rest replaced by `••••`)
  and flagged *literal credential on host*; the literal value is never sent to a client. A header that
  switchboard's record holds as a `${…}` template but that the runtime reports with no placeholder is
  handled by one behaviour flag pinned in the host CLI contract (FR-422), *inspect returns templates*
  or *inspect returns resolved values*. With **templates** (the default until a real host confirms
  otherwise), such a value can only be a hand edit. For a credential header it is redacted, flagged
  and blocks re-registering like any other literal; a non-credential header is an ordinary host-side
  change, pre-filled with the reported value and flagged *changed on the host* (FR-402), with no
  redaction and no block. With **resolved values**, the header is taken to be that template resolved:
  its value is never sent to a client and it counts as *not reported* (FR-402), so the form pre-fills
  the record's template with no flag, and the form MUST say that the host value cannot be checked
  against the template and that a re-registering save registers the template, so an overwrite is
  never silent. The fingerprint still covers its raw value (FR-404); that a secret set elsewhere then
  makes open forms reload as changed is accepted. The form MUST refuse a
  save that would **re-register** the server while such a header is unconverted; the conversion needs
  the secret re-entered, since its value is not available. A save that does not change the
  registration (mark or secrets only, FR-403) is allowed, keeps the warning on the form, and stores the
  header in switchboard's record only in redacted, flagged form. The stored definition and the
  definition read for the edit form therefore never hold a credential header's literal value. **URL query parameters and host-command arguments** are not covered by this
  rule, because the runtime offers no `${…}` substitution for host-command arguments and a URL cannot
  hold a header. When the URL has a query parameter, or the arguments a flag, whose name segment is one
  of the credential words above or `sig`/`signature`, the form MUST warn before saving that the value
  is stored in plain text in switchboard's record and served to every connected client, and point to
  placeholder headers (remote) or environment-based secrets (host command). It does not refuse.
- **FR-424**: While an authorization of a server name is in progress, the daemon MUST refuse, with no
  change and a message to finish or cancel it first (the existing `ErrAuthInProgress` pattern): a save
  that would remove the server (a replace); and any save, including a secrets-only save (FR-403), or
  FR-413 removal that would store or remove `mcp:<server>:client_secret` or remove
  `mcp:<server>:client_secret:identity` (FR-415). A save that only stores header secrets, without
  re-registering, is allowed: header secrets are read per request, not at the token exchange.
  **Authorize** and **Authorize with scopes…** MUST be refused while a save of that name holds the
  per-name lock or while a pending replace of that name exists (FR-406), each with its reason. The
  in-flight authorization record and the per-name lock MUST be checked and taken in one critical
  section, so neither side can start between the other's check and its start. The Secrets view and
  the host CLI do not take the per-name lock and are not covered, as for FR-412: a `client_secret`
  changed there during an authorization can only make that authorization fail, and the developer
  runs it again.
- **FR-425**: A launch that resolves attach-by-default servers while a replace of a marked server is
  running or pending MUST skip that server, keep its mark, and report it as *skipped: being replaced*,
  not as a stale mark. The skip MUST be recorded on the sandbox itself (a skipped-server list beside
  its attached servers, persisted with the sandbox record), so it survives a daemon restart and every
  client sees it on the sandbox's row and in its MCP overlay. When the replace or pending replace
  ends with switchboard registering the server (a retry), each entry changes to *not attached at
  launch* and the overlay offers to attach it; attaching clears the entry. When it ends any other way
  (cancel, or a registration by another path (FR-406), which is not the server the developer chose and whose
  mark is dropped), the daemon clears the entries. No notification is
  raised: the entries carry the per-sandbox detail, and on success the FR-419 list shown by the form
  that saved also includes those sandboxes with the same note. For a sandbox launched in
  **exclusive** mode (`--static-mcp`), the
  note MUST instead say that the server is missing from its fixed set and the sandbox must be
  relaunched to include it, unless the real-host run shows `mcp load` works in that mode (FR-422).
- **FR-426**: A pending replace (FR-406) MUST be visible on every connected client, not only in the
  form that ran the save: the server list and the gateway-changed event carry it, and the gateway view
  shows it as a row `<server> — not registered (replace failed <age> ago)` with **Retry** (opens the
  edit form pre-filled from the held definition or, at the developer's choice, the previous one, and
  names, from the pending replace, the secrets the failed save removed and those whose removal
  failed; when the previous definition is chosen it says the removed secrets' old values are gone and
  must be entered again; its save completes the pending replace, FR-406) and
  **Cancel** (the same `y`-only confirmation as discarding the form; it makes a best-effort removal
  of any *removal failed* secrets and names those still left on the host, FR-406). Because the pending replace is
  persisted and has no time limit, it is never dropped without the developer seeing this row, except
  when the runtime lists that name again (registered by another path, FR-406).

### Key Entities

- **MCP server definition** (new, switchboard-owned, per daemon): what switchboard last registered for a
  server name: kind, URL or command/args/dir, headers (credential headers only as `${placeholder}`
  templates, never secret values, or redacted and flagged *literal credential on host*, FR-423),
  client ID, authorization server, default scopes, and the *client re-bind pending* flag (FR-415).
  Kept beside the existing attach marks and removed when
  the server is removed. It is the source for the edit form. The host runtime stays the authority on what
  is registered: a definition whose name the runtime no longer lists is dropped, like a stale mark, except
  while a replace of that name is in progress or is a *pending replace* (FR-404, FR-406).
- **Pending replace** (new, persisted, per daemon and server name): the mark, the definition being
  saved, the previous definition, and the names (never values) of the secrets the failed save
  removed and of those whose removal failed, held after a replace's add (or a store after its remove) failed, so a retry from any
  client can finish it with either definition (FR-406, FR-426). It is never applied at launch
  (FR-425), has no time limit, and ends as FR-406 states.
- **Skipped server on a sandbox** (new, persisted with the sandbox record): an attach-by-default
  server skipped at launch because a replace was running or pending (FR-425). It becomes *not
  attached at launch* when a retry registers the server, and is cleared when it is attached from
  the overlay, the pending replace is cancelled, or the name is registered by another path.
- **Server secret** (existing host secret, not stored by switchboard): `mcp:<server>:<placeholder>` for
  header placeholders, `mcp:<server>:client_secret` for a confidential client, and the runtime-managed
  binding record `mcp:<server>:client_secret:identity`. Switchboard reads only whether each is set.
- **Default scopes** (new attribute of a remote registration): the ordered, de-duplicated scope list
  recorded with the runtime and remembered in the definition.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can change the URL of a registered remote server in one form, without
  retyping any other field, in under 30 seconds.
- **SC-002**: Adding a remote server with credential headers and a confidential OAuth client takes one
  form and zero visits to the Secrets view.
- **SC-003**: In 100% of saves, every secret the form stores lands under the exact name the runtime
  reads (`mcp:<server>:<placeholder>` / `mcp:<server>:client_secret`). There is no path where the
  developer types a secret name by hand.
- **SC-004**: After a client ID change saved with the new client's secret, the next authorization
  succeeds without the developer running any host command when the identity clear succeeds (a
  reported failed clear names the record to remove and keeps the re-bind mark, FR-415; one the secret
  list cannot see is the round-15 known limitation), and the
  previous client's secret is never sent for the new client. After a failed or timed-out add of a
  replace, no client secret or header secret that the failed save stored is used by a retry (created
  and overwritten ones are removed, and one whose removal failed is removed by the retry unless
  re-entered, FR-406, FR-412), so a retry never uses it for a different client or endpoint; a
  Cancel removes such a secret too, and one whose removal fails again on Cancel is named as left on
  the host (the known limitation below). A form reloaded after a refused save never stores a secret typed for
  the URL or client it replaced (FR-404). For an
  ordinary edit that changes the URL and keeps a header secret blank, keeping it from the new endpoint
  relies on the runtime's endpoint binding (FR-409), which is reconciled on a real host (FR-422).
- **SC-005**: A server registered with default scopes requests those scopes on every plain
  authorization, a one-time override requests exactly the scopes given, and an override never changes
  the recorded defaults.
- **SC-006**: A successful edit never loses a server's attach-by-default mark, a failed replace always
  tells the developer which of the two failure states the server is in, and a held mark is never
  applied at launch or to a different registration.
- **SC-007**: No secret value entered in a secret field, and no literal value of a credential header
  (FR-423), ever appears in a log line, error message, process argument, the switchboard registry
  (including the stored definition), a daemon response, or the TUI's screen (masked entry only, and
  runtime-reported credential headers redacted). Credentials placed in a URL query string or
  host-command arguments are outside this guarantee: they are stored in plain text and the form warns
  about them before saving (FR-423).

## Assumptions

- The runtime's *remove then add* is the only way to change a registration, and removing deletes the
  OAuth access token (documented). The client secret and its identity binding survive a remove
  (documented).
- For servers switchboard did not register, the runtime's inspect output (header templates, resource) and
  authorization status (default scopes) are the only documented sources. Whether inspect also reports the
  URL, client ID or authorization server is **unknown and must be reconciled on a real host**. If it does
  not, FR-402's *named empty field* rule covers the gap.
- Documentation-derived, to reconcile on a real host: the runtime's `${…}` placeholder grammar; whether the
  secret list shows the `:identity` and `:endpoint` records; and whether a header secret has to be set
  again after an endpoint change, or only checked again (SC-004 relies on the former for a blank-kept
  secret, FR-409); and whether the scope flags on add and
  authorize treat a `,` in a scope literally (FR-417); whether the authorization status and inspect
  positively report scopes and headers, so an empty answer can be read as "none" (until then it is
  *not reported*, FR-402); whether a killed `mcp add` / `mcp rm` can still complete on the host
  (until then a failed add leaves the host's state unknown, and a late finish ends the pending replace
  as *registered by another path*, FR-406); and whether `mcp load` attaches a server to a sandbox launched with
  `--static-mcp` (FR-425). Storing secrets after a replace's remove
  (FR-410) means a header secret is never stored while the old endpoint is still registered, so which
  endpoint it binds to is no longer an open question.
- Scope flags on add and authorize, the *no scopes* authorize option, and removing
  `mcp:<server>:client_secret:identity` through the existing secret removal are taken from the runtime's
  documentation (2026-10-30), not yet run on a real host.
- Secrets the form stores are global-scope host secrets (header secrets must be global per the runtime's
  documentation), so the form offers no sandbox scope.
- The existing Secrets view stays. The MCP form is an additional, faster path to the same secrets.
- Servers registered before this feature have no switchboard definition. They are pre-filled from the
  runtime (FR-402) and get a definition on their first save.
- Out of scope: a fixed OAuth callback port (ticket item 5) is **deferred to SWI-22**,
  because the runtime documents no way to fix its callback port and switchboard cannot change the
  `redirect_uri` itself. The existing callback relay and paste-back (008 FR-075) are unchanged.
- Out of scope: editing the per-worker orchestrator servers, the runtime's `--resource` override,
  hosted-gateway or cloud-sandbox registrations, and moving secrets on rename.
- Cut in review round 3 to keep the feature small: a separate *Restore previous definition*
  operation (a retry pre-filled with the previous definition does the same through the ordinary
  save), a *not a credential* header override (the value goes into a placeholder secret instead), a
  time limit on pending replaces and notifications when a replace ends (the gateway row and the
  sandbox entries carry that state), and per-name locking of the Secrets view (FR-424).
- Cut in review round 9 (follow-up, not specified here): after a failed or timed-out add, switchboard
  does not try to work out what the host holds. It does not re-check a failed add and count it as
  succeeded when the listed server matches the saved definition, does not carry secret names from
  earlier attempts into a retry (only the latest failed attempt's names are held, so Retry stops
  naming an earlier attempt's *removed* secrets that a failed retry did not re-enter; FR-409 still
  shows them as missing), and does not compare a retry with earlier attempts per field. All of these are
  replaced by FR-406's single rule (host state unknown, every stored secret removed, retry from
  scratch). The pending replace keeps only secret names (removed, or removal failed), never values. The cost
  is that a failed add which in fact registered, or which finishes later, needs its mark set and its
  secrets entered again, and that Restore (a retry with the previous definition) brings back the
  previous registration but not the old values of the secrets the failed save overwrote: they must
  be entered again. A follow-up can narrow this once a real host shows
  whether a killed `mcp add` can still complete and whether inspect reports URL, client ID and
  authorization server (FR-422).
- Known limitation (review round 10): for a **new server's** failed add no pending replace is held
  (FR-406), so a secret whose rollback removal failed is reported only in the save's outcome (its
  response and gateway-changed event); a developer whose client was gone when the save ended does not
  see it. The developer removes or re-enters it by hand.
- Known limitation (review rounds 12–13): **Cancel** of a pending replace is never blocked by a
  secret whose rollback removal failed. Cancel makes a best-effort removal of it (FR-406); only if
  that removal fails again (secret storage failing, or the list unreadable) is it named in the
  outcome as left on the host, and the developer removes it in the Secrets view or the host CLI.
  Switchboard does not track it after the cancel, so a later add of that name from the form shows
  it as *set* and a blank field keeps it without a warning. This keeps Cancel always available at
  the cost of a named leftover, which arises only when the removal fails on Cancel (or cannot be
  confirmed) and stays on the host until the developer removes it.
- Known limitation (review round 15): a failed clear of the identity binding counts as done when
  the secret list does not show the `:identity` record (FR-415). If the real-host run finds that the
  list never shows that record, a genuine failed clear is not reported; the runtime then refuses to authorize
  the new client, and the developer removes `mcp:<server>:client_secret:identity` in the Secrets
  view or the host CLI. The real-host run (FR-422, *Assumptions*) checks whether the list shows it.
- Known limitation (review round 17): a server marked *client re-bind pending* by a failed clear
  (FR-415) keeps the mark until a save that stores the client secret, even after the developer removes
  the `:identity` record by hand or saves other changes without re-entering the secret, so the form
  keeps showing the pending state until the secret is re-entered. The failure message says so. FR-404
  reports such a save without the secret as *not touched* (no clear attempted), with the mark shown
  in the resulting state. Whether every save of a marked server should retry the clear (and whether
  the mark should record its cause) is an open item for the plan phase; the spec adds no mechanism for
  it.
- Known limitation (review round 18): a developer who switches a server to a public client follows
  FR-415's warning by removing `mcp:<server>:client_secret` and its `:identity` record by hand, but
  the *client re-bind pending* mark stays (it clears only on a save that stores a client secret), and
  FR-413's removal offer covers only a cleared client ID. Whether FR-413's offer (and the mark's clear)
  should extend to a client-ID change saved without a secret is an open item for the plan phase,
  alongside the round-17 item; the spec adds no mechanism for it.
