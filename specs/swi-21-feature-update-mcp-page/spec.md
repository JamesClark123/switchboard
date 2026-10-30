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
   **not registered**, and which secrets this save already stored (FR-406). In the *not registered*
   case the form offers **Restore previous definition** beside retrying the save, and discarding the
   form asks for a `y`-only confirmation that names the server as not registered and says its
   attach-by-default mark will be dropped (FR-406). If that save overwrote header secrets and the
   previous definition has a different URL, the restore asks for each of those secrets again by name,
   or removes them behind a confirmation, so the other endpoint's credential is never sent to the old
   one (FR-406). **Given** the saving TUI quit or lost its link, **When** any client opens the gateway
   view, **Then** the server shows as *not registered (replace failed <age> ago)* with **Retry**,
   **Restore previous definition** and **Cancel** (FR-426).
6. **Given** a host-command server, **When** the developer edits its command, arguments or working
   directory, **Then** the local-execution warning (008 FR-076) is shown again before anything is
   registered (FR-407).
7. **Given** another client changed the server after this form was opened, **When** the developer saves,
   **Then** the save is refused with no change and the developer is told to reopen Edit (FR-404).
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
   the form refuses to save until the header is converted to a placeholder and its secret entered
   (FR-423).
10. **Given** an authorization of the server is in progress, **When** the developer saves an edit that
    would replace it, or that would store `mcp:<server>:client_secret` or clear its binding record
    (including a secrets-only save, or storing that secret from the Secrets view), **Then** the save
    is refused with no change and a message saying to finish or cancel the authorization first. A
    save that only stores header secrets is allowed. **Given** a save or replace of the server is running, or a pending
    replace holds its name, **When** the developer chooses **Authorize** or **Authorize with
    scopes…**, **Then** it is refused with a message saying the server is being replaced (FR-424).
11. **Given** a sandbox is launched while a replace of a marked server is running or pending, **When**
    the launch output reports the server, **Then** it is reported as *skipped: being replaced* (not as a
    stale mark), the mark is kept, and the skip is recorded on the sandbox. **When** the replace ends,
    whatever the ending (success, restore, cancel, expiry or daemon restart), **Then** one
    notification per affected sandbox is raised on every client, and on success or restore the sandbox
    is also in the FR-419 list with a note to attach the server from its MCP overlay (FR-425).

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
5. **Given** a secret store fails part-way through a save, **When** the error comes back, **Then**
   nothing is added, the error is shown, secrets this save **created** are removed again, and
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
   literal text (`Bearer sk-live-abc ${x}`, `session=abc; csrf=${t}`) is refused the same way (FR-423).
9. **Given** a non-secret header whose name matches the credential rule (for example
   `X-Auth-Mode: oauth`), **When** the developer saves, **Then** the form asks a `y`-only
   *not a credential* confirmation for that header; on `y` the value is kept as typed and the choice is
   recorded in the definition (FR-423).
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
   the developer saves, **Then** the form warns that the runtime will refuse to authorize until the new
   client's secret is entered, and the binding is left in place, so the old secret is never offered to
   the new client (FR-415).
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
  credential from placeholders, with at most a leading auth-scheme token. The form offers to convert a
  typed literal value into a placeholder and secret, a header that only looks like a credential can be
  confirmed *not a credential*, and a literal credential found on the host is redacted by the daemon
  before any client sees it (FR-423).
- **Credentials in URLs or host-command arguments**: stored in plain text in switchboard's record;
  the form warns and points to placeholders or environment-based secrets (FR-423).
- **Client drops mid-save**: the TUI quits or its SSH link drops while a save is running. The daemon
  finishes the save, including rollback and the identity clear, and the outcome is visible to every
  client through the gateway-changed and secrets-changed events (FR-404). If it ends *not registered*,
  the pending replace is a row on every client's gateway view with Retry, Restore and Cancel, and its
  expiry or drop raises a notification (FR-426).
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
  presentation is not a change. Fields the runtime does not report come from switchboard's
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
  whether the identity binding was cleared, and the resulting registration state. The TUI only renders
  it. A save that changes the registration is carried out as a **replace**: remove the registration,
  then add the edited definition, always registering without authorizing. It keeps the
  attach-by-default mark and switchboard's definition across the remove and add (a list taken
  mid-replace MUST NOT drop them), and emits one gateway-changed event when it finishes. The save MUST
  be refused, with no change, when the server no longer matches what the form was opened with
  (switchboard's stored definition and the runtime-reported fields, FR-402); the check runs under the
  lock before any secret is stored. Changes made directly on the host with the runtime's CLI bypass the
  lock and are caught only as far as this comparison can see them; that is accepted. Before anything is
  sent, the form MUST warn that the server's authorization will be discarded unless its state is known
  to be unauthorized, and MUST proceed only on confirmation.
- **FR-405**: After a successful replace of a remote server, the TUI MUST offer to start authorization
  immediately, using the existing authorization flow (008 FR-075).
- **FR-406**: If the replace fails, the host's message MUST be shown verbatim (008 FR-078), the form MUST
  stay open with the developer's edits, and the developer MUST be told the resulting state:
  *still registered, authorization may be lost* (the remove failed: the runtime deletes the OAuth token
  before the registration) or *not registered* (the remove succeeded and the add failed). In the
  *not registered* case, the daemon MUST hold the server's attach-by-default mark and the definition
  being saved as a **pending replace** of that name, and the form's save MUST then act as an add that
  completes it. A pending replace is exempt from the stale-mark drop, MUST NOT be applied at sandbox
  launch (FR-425), and ends when the add succeeds (mark and definition become live), when the developer discards
  the form (the TUI tells the daemon through an explicit cancel), after 30 minutes, or when the daemon restarts (mark and definition dropped, as for any
  removed server). A server registered under that name by any other path also ends it, and the held
  mark is then dropped, not applied. The pending replace MUST also hold the **previous** definition,
  and in the *not registered* state the form MUST offer **Restore previous definition**: one daemon
  operation that re-adds the previous definition without authorizing, makes the held mark live, and
  then offers authorization (FR-405). The restore guards secrets this save overwrote, because a
  restore is itself a re-bind back to the previous definition:
  - **Header secrets**: if this save overwrote any placeholder secret and the previous definition's
    URL differs, the restore MUST show a masked field for each such secret, named
    `mcp:<server>:<placeholder>`, and warn that the stored value belongs to the other endpoint. For each
    one the developer either enters the previous value (stored as part of the restore, before the add)
    or confirms its removal (`y`-only, per FR-413); the restore MUST NOT proceed while any of them would
    keep the other endpoint's value, so that credential is never sent to the old endpoint.
  - **Client secret**: if this save overwrote `mcp:<server>:client_secret` and the previous definition
    differs from the saved one by any of FR-415's triggers (client ID, authorization server, or the URL
    of a server with a pre-registered client), the restore MUST first ask for the previous client's
    secret. If the developer restores without it, the definition is marked *client secret mismatch*
    and switchboard refuses to authorize the server (FR-424) while it is set. The flag is shown next to
    the client-secret field (like *re-bind pending*, FR-415) and in the gateway view row, with how to
    clear it. Any store of `mcp:<server>:client_secret` through this daemon clears it: an MCP-form save
    (including secrets-only) or a store from the Secrets view, both of which go through the daemon's
    secret manager, which clears the flag after a successful store. A store made directly with the host
    CLI is not seen; the flag's text says to store the secret through switchboard instead.
  - **Identity binding**: the restore MUST NOT clear `mcp:<server>:client_secret:identity`. The
    replace's add never succeeded, so FR-415 never cleared it, and it still binds the previous client.
  Discarding the form in the *not registered* state MUST go through a `y`-only
  confirmation that names the server as not registered and says its attach-by-default mark will be
  dropped. In either failure state, the developer MUST be told by name which secrets this save already
  stored or overwrote. Retry, restore and cancel of a pending replace are daemon operations available
  from every client, not only the form that ran the save (FR-426).
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
  the stored value*, except after a URL change: the runtime binds header secrets to the server's
  endpoint, so when an edit changes the URL, every placeholder field MUST be marked *must be re-entered*,
  and saving with any of them blank MUST warn that the header will not be sent until that secret is set
  again.
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
  before the stores. The secrets this save created MUST be removed again (best effort, with any failure
  reported), and the secrets it overwrote MUST be named as changed, because their old values cannot be
  restored. That a secret set outside this save (from the Secrets view or the host CLI, neither of which
  takes the per-name lock) in the window between the list and the stores could be rolled back is
  accepted.
- **FR-413**: When an edit stops using a stored secret (a placeholder removed from the headers, or the
  client ID cleared), the TUI MUST name that secret after the save and offer to remove it behind a
  confirmation. Switchboard MUST never remove a secret silently.

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
  runtime will refuse to authorize until the new client's secret is entered. A later save that stores a
  client secret for a server marked *re-bind pending*, including a secrets-only save (FR-403), MUST then
  clear the binding and the mark. The edit form MUST show the pending state next to the client-secret
  field. A *not found* answer from clearing the binding counts as success. Any
  other failure MUST be shown verbatim, without rolling back the registration.

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
  headers (FR-402), and whether `mcp load` attaches a server to a sandbox launched in exclusive mode
  (FR-425). The new client-facing operations (the save, retry, restore previous definition and cancel
  of a pending replace, the pending-replace state carried by the server list and the gateway-changed
  event (FR-426), the definition read for the edit form with its redacted-header flag (FR-423), the
  sandbox's skipped-server record (FR-425), scopes on add, one-time scopes on authorize) are additive
  to the existing daemon contract; older clients keep working unchanged.
- **FR-423**: A header is a **credential header** when its name is `Authorization`,
  `Proxy-Authorization` or `Cookie`, or when any of its name segments (the name split on `-` and `_`,
  case-insensitive) is `key`, `apikey`, `token`, `secret`, `password`, `auth` or `credential(s)`. Name
  segments, not substrings, are matched, so `Keycloak-Realm` is not a credential header while
  `X-Api-Key` is. A credential header's value MUST consist only of `${placeholder}`s, optionally led by
  one auth-scheme token (`Bearer`, `Basic`, `Token`, `Bot` or `Digest`, case-insensitive) and joined
  by literal separators (space, `:`, `;`, `=`, `,`); any other literal text is refused by the TUI and
  the daemon (FR-420), with a message saying which part is literal and why. When such a value is
  typed, the form MUST offer to convert it in one step: the value is replaced by the scheme token (if
  any) and a `${placeholder}`, and the rest of the typed value moves into that placeholder's masked
  secret field. The derived placeholder name is the header name lower-cased with `_` mapped to `-`, MUST
  satisfy FR-408, MUST NOT be `client_secret`, and MUST NOT collide with a placeholder already used by
  another header; on collision or an invalid name the form appends `-2`, `-3`, … and the field stays
  editable, so two credentials are never merged into one secret. For a header that matches the rule but
  carries no secret (for example `X-Auth-Mode: oauth`), the developer MAY confirm *not a credential*
  (`y`-only) for that header; the choice is recorded per header in the definition, shown on the form,
  and the value is then kept as typed. Other headers keep their values as typed, and the form says that
  those values are kept in switchboard's record and shown when the form opens. Runtime-reported headers
  (FR-402) go through the same rule **in the daemon, before the definition read returns them**: a
  credential header whose value holds literal text (for example one added by hand on the host) is
  returned redacted (scheme token kept, the rest replaced by `••••`) and flagged *literal credential on
  host*; the literal value is never sent to a client. The form MUST refuse to save while such a header
  is unconverted; the conversion needs the secret re-entered, since its value is not available. The
  stored definition and the definition read for the edit form therefore never hold a credential
  header's literal value. **URL query parameters and host-command arguments** are not covered by this
  rule, because the runtime offers no `${…}` substitution for host-command arguments and a URL cannot
  hold a header. When the URL has a query parameter, or the arguments a flag, whose name segment is one
  of the credential words above or `sig`/`signature`, the form MUST warn before saving that the value
  is stored in plain text in switchboard's record and served to every connected client, and point to
  placeholder headers (remote) or environment-based secrets (host command). It does not refuse.
- **FR-424**: While an authorization of a server name is in progress, the daemon MUST refuse, with no
  change and a message to finish or cancel it first (the existing `ErrAuthInProgress` pattern): a save
  that would remove the server (a replace); any save, including a secrets-only save (FR-403), that
  would store `mcp:<server>:client_secret` or clear `mcp:<server>:client_secret:identity` (FR-415); and
  a store of `mcp:<server>:client_secret` from the Secrets view. A save that only stores header
  secrets, without re-registering, is allowed: header secrets are read per request, not at the token
  exchange. **Authorize** and **Authorize with scopes…** MUST be refused while a save of that name
  holds the per-name lock, while a pending replace of that name exists (FR-406), or while its
  definition is marked *client secret mismatch*, each with its reason. The in-flight authorization
  record and the per-name lock MUST be checked and taken in one critical section, so neither side can
  start between the other's check and its start.
- **FR-425**: A launch that resolves attach-by-default servers while a replace of a marked server is
  running or pending MUST skip that server, keep its mark, and report it as *skipped: being replaced*,
  not as a stale mark. The skip MUST be recorded on the sandbox itself (a skipped-server list beside
  its attached servers, persisted with the sandbox record), so it survives a daemon restart and every
  client sees it on the sandbox's row and in its MCP overlay. Attaching the server from the overlay
  clears its entry. When the replace ends, whatever the ending (success, restore, cancel, expiry, or
  the daemon restarting with the pending replace gone), the daemon MUST raise one notification per
  affected sandbox naming the server and the ending. On success or restore, the FR-419 list MUST also
  include those sandboxes, with a note that the server was not attached at launch and can be attached
  from the sandbox's MCP overlay. For a sandbox launched in **exclusive** mode (`--static-mcp`), the
  note MUST instead say that the server is missing from its fixed set and the sandbox must be
  relaunched to include it, unless the real-host run shows `mcp load` works in that mode (FR-422).
- **FR-426**: A pending replace (FR-406) MUST be visible on every connected client, not only in the
  form that ran the save: the server list and the gateway-changed event carry it, and the gateway view
  shows it as a row `<server> — not registered (replace failed <age> ago)` with **Retry** (opens the
  edit form pre-filled from the held definition, whose save completes the pending replace),
  **Restore previous definition** and **Cancel** (the same `y`-only confirmation as discarding the
  form). When a pending replace expires or is dropped for any other reason than a successful retry or
  restore, the daemon MUST raise a notification naming the server and saying its attach-by-default
  mark was dropped. After a daemon restart, which drops pending replaces, the daemon MUST raise that
  notification at start-up for each one it had persisted a marker for.

### Key Entities

- **MCP server definition** (new, switchboard-owned, per daemon): what switchboard last registered for a
  server name: kind, URL or command/args/dir, headers (credential headers only as `${placeholder}`
  templates, never secret values, FR-423, plus the per-header *not a credential* confirmations), client
  ID, authorization server, default scopes, and the *client re-bind pending* (FR-415) and *client
  secret mismatch* (FR-406) flags. Kept beside the existing attach marks and removed when
  the server is removed. It is the source for the edit form. The host runtime stays the authority on what
  is registered: a definition whose name the runtime no longer lists is dropped, like a stale mark, except
  while a replace of that name is in progress or is a *pending replace* (FR-404, FR-406).
- **Pending replace** (new, transient, per daemon and server name): the mark, the definition being
  saved and the previous definition, held after a replace's add (or a store after its remove) failed,
  so a retry can finish it or a restore can undo it, from any client (FR-426). It is never applied at
  launch (FR-425) and ends as FR-406 states. Only a marker (server name and the sandboxes skipped for
  it) is persisted, so the endings after a daemon restart can still be notified (FR-425, FR-426).
- **Skipped server on a sandbox** (new, persisted with the sandbox record): an attach-by-default
  server skipped at launch because a replace was running or pending (FR-425), cleared when it is
  attached from the overlay.
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
  succeeds without the developer running any host command (the identity binding is handled), and the
  previous client's secret is never sent for the new client. A failed replace never lets switchboard
  authorize the old client with the new client's secret (a restore without the previous secret is
  marked *client secret mismatch* and refused authorization, FR-406), and never sends one endpoint's
  header secret to another endpoint.
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
  again after an endpoint change, or only checked again; and whether the scope flags on add and
  authorize treat a `,` in a scope literally (FR-417); whether the authorization status and inspect
  positively report scopes and headers, so an empty answer can be read as "none" (until then it is
  *not reported*, FR-402); and whether `mcp load` attaches a server to a sandbox launched with
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
