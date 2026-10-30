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
  The runtime offers an **inspect** command for one server that shows its registration (header templates
  and whether their secrets are set, never their values). Switchboard does not keep a copy of what it
  registered today.
- Header placeholders `${name}` read the host secret `mcp:<server>:<name>`. A confidential client's secret
  is the host secret `mcp:<server>:client_secret`, and it must be stored **before** the server is
  registered, or the runtime silently skips authorization. When a server is re-registered with a
  **different client**, the runtime refuses the old secret until the developer stores the new client's
  secret **and** clears the binding record `mcp:<server>:client_secret:identity`.
- Registration accepts a repeatable **scope** setting that records the default OAuth scopes requested at
  authorization. Authorization accepts scopes that override the defaults for one run.
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
  up the new definition after a restart or a re-attach. Nothing is stopped or reloaded (FR-425).
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
   the form first warns that the server will need to be authorized again and proceeds only on
   confirmation. After the replace, the developer is offered the authorize step at once (FR-404,
   FR-405).
5. **Given** the replace's add step fails on the host, **When** the error comes back, **Then** the
   runtime's message is shown verbatim, the form stays open with the developer's edits, and the
   developer is told whether the server is still registered as before or is now **not registered**
   (FR-406).
6. **Given** a host-command server, **When** the developer edits its command, arguments or working
   directory, **Then** the local-execution warning (008 FR-076) is shown again before anything is
   registered (FR-407).

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
   secret is stored on that daemon's host **before** the server is registered, and the server is
   registered only if every store succeeded (FR-410).
3. **Given** a placeholder whose secret already exists, **When** the developer leaves its field blank,
   **Then** the stored value is kept unchanged (FR-409).
4. **Given** a placeholder whose secret is missing and no value is entered, **When** the developer saves,
   **Then** the form names the missing secret and asks whether to register anyway (the header will not
   work until it is set) or go back (FR-411).
5. **Given** a secret store fails part-way through a save, **When** the error comes back, **Then**
   nothing is registered, the error is shown, secrets this save **created** are removed again, and
   secrets it **overwrote** are reported by name as changed (FR-412).
6. **Given** an edit removes a header whose placeholder secret is stored, **When** the save completes,
   **Then** the developer is told that the secret is no longer used and is offered its removal. It is
   never removed silently (FR-413).

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
   `mcp:<server>:client_secret` on that daemon's host before the server is registered (FR-410, FR-414).
3. **Given** an edit changes the client ID, the authorization server, or the URL of a server with a
   pre-registered client, **When** the save completes, **Then** switchboard clears the binding record
   `mcp:<server>:client_secret:identity`, so the next authorization binds the new client, and the
   developer is told this happened (FR-415).
4. **Given** the client ID is changed but no new client secret is entered while one is stored, **When**
   the developer saves, **Then** the form warns that the stored secret belongs to the previous client
   and asks for confirmation (FR-415).
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
the authorization link requests exactly those scopes.

**Acceptance Scenarios**:

1. **Given** a remote server form, **When** the developer enters scopes, **Then** they are recorded as
   the server's default scopes on registration, one runtime scope setting per scope (FR-416).
2. **Given** scopes with whitespace, control characters, a leading `-`, duplicates or an over-long value,
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
- **Concurrent edit from two clients**: the second save is a replace of whatever is registered at that
  moment. The gateway view of every client refreshes on the existing `mcp_gateway_changed` event.
- **Kind change on edit** (remote ↔ host command): allowed as a replace. Every remote-only field (headers,
  client ID, authorization server, scopes, secrets) is cleared from the form, and the local-execution
  warning is shown.
- **Sandboxes that already loaded the server**: the runtime keeps the old definition in sandboxes that
  already loaded it. FR-425 covers what the developer is told; nothing is stopped, unloaded or reloaded.
- **Secret values**: never shown, logged, persisted by switchboard, echoed in errors, or placed in a
  process argument (008 FR-104 applies unchanged). They are single-line, and a multi-line value is
  refused.
- **Placeholder names**: a `${…}` placeholder whose name would not make a valid secret name is refused
  with the same message the daemon's header validation gives today.
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
  command/arguments/working directory, headers, client ID, authorization server, scopes, and the
  attach-by-default mark. Switchboard MUST remember the definition of every server it registers or
  replaces (per daemon, beside the existing attach marks) and use that first. For a server it has no
  record of, it MUST pre-fill from what the host runtime reports for that server. Any field that cannot
  be recovered MUST be left empty and named in the form, so that saving does not silently drop it.
- **FR-403**: Saving an edit that changes only switchboard-side settings (the attach-by-default mark)
  MUST NOT re-register or re-authorize the server (008 FR-077).
- **FR-404**: Saving an edit that changes the registration MUST be carried out as a replace: remove the
  registration, then add the edited definition, always registering without authorizing. Before
  anything is sent, the form MUST warn when the server is authorized and the replace will discard that
  authorization, and MUST proceed only on confirmation.
- **FR-405**: After a successful replace of an OAuth-capable remote server, the TUI MUST offer to start
  authorization immediately, using the existing authorization flow (008 FR-075).
- **FR-406**: If the replace fails, the host's message MUST be shown verbatim (008 FR-078), the form MUST
  stay open with the developer's edits, and the developer MUST be told the resulting state: *unchanged*
  (the remove failed) or *not registered* (the remove succeeded and the add failed). In the
  *not registered* case the form's save MUST then act as an add.
- **FR-407**: An edit that results in a host-command definition MUST show the local-execution warning
  and require acceptance again (008 FR-076), even if the server was a host command before.

### Secrets on the MCP form

- **FR-408**: For a remote server, the form MUST list one masked value field for each distinct
  `${placeholder}` in its headers, each labelled with its full secret name
  `mcp:<server>:<placeholder>`. The list MUST follow the header field as it is edited.
- **FR-409**: Each secret field (placeholder secrets and the client secret) MUST show whether the secret
  is currently stored on the daemon's host (from the host's secret list; never the value). A blank field
  MUST mean *keep the stored value*.
- **FR-410**: On save, every secret with an entered value MUST be stored on that daemon's host, in the
  global scope and through the existing secret path (008 FR-104: request → CLI standard input only),
  **before** the server is added. The add MUST NOT run unless every store succeeded.
- **FR-411**: When a placeholder's secret is missing and no value was entered, saving MUST name each
  missing secret and ask the developer to register anyway or go back. It MUST NOT block a deliberate
  register.
- **FR-412**: If storing secrets fails part-way, nothing MUST be registered or removed. The secrets this
  save **created** MUST be removed again (best effort, with any failure reported), and secrets it
  **overwrote** MUST be named as changed, because their old values cannot be restored.
- **FR-413**: When an edit stops using a stored secret (a placeholder removed from the headers, or the
  client ID cleared), the TUI MUST name that secret after the save and offer to remove it behind a
  confirmation. Switchboard MUST never remove a secret silently.

### Pre-registered client secret

- **FR-414**: When a client ID is present, the form MUST show a masked **client secret** field that
  stores `mcp:<server>:client_secret`. The field MUST say it is needed only for a confidential client.
  The 008 FR-105 instruction to set the secret under Secrets first MUST be replaced by this field.
- **FR-415**: When a save changes the client ID or the authorization server of a server that has a
  stored client secret, or changes the URL of such a server, switchboard MUST remove the binding record
  `mcp:<server>:client_secret:identity` after the replace succeeds and tell the developer. If no new
  client secret was entered, the form MUST first warn that the stored secret belongs to the previous
  client and require confirmation.

### Scopes

- **FR-416**: A remote server registration MUST accept an optional list of **default OAuth scopes**,
  recorded with the registration (one runtime scope setting per scope) and shown and editable on the
  edit form.
- **FR-417**: Each scope MUST be a single token with no whitespace, no control or format characters,
  valid UTF-8, no leading `-`, and at most 256 characters, and there MUST be at most 32 distinct scopes.
  Duplicates MUST be refused. Scopes on a host-command server MUST be refused. The TUI and the daemon
  MUST apply the same rule, so a daemon refusal never discards the form (the SWI-20 pattern).
- **FR-418**: The gateway view MUST offer **Authorize with scopes…** for a registered remote server. It
  takes a one-time scope list (same rule as FR-417) or *no scopes*, both passed to that one
  authorization only. The recorded defaults are unchanged. The plain authorize action keeps today's
  behaviour, using the recorded defaults. The runtime's output about requested, advertised or refused
  scopes MUST be shown verbatim.

### Cross-cutting

- **FR-425**: After a successful replace, the TUI MUST list this daemon's sandboxes whose records show
  the server attached, and say that each picks up the new definition only after a restart or a re-attach
  from the sandbox's MCP overlay. Switchboard MUST NOT stop, unload or reload any sandbox as part of an
  edit, and the sandboxes' attached-server records MUST be unchanged.

- **FR-422**: Every validation rule the daemon applies to a definition (headers, client ID, authorization
  server, scopes, placeholder-derived secret names) MUST also be applied by the TUI before any request
  is sent, so the form stays open on an invalid value.
- **FR-423**: Every completed save, edit, secret store or secret removal MUST refresh the gateway view
  and the Secrets view, and MUST reach other connected clients through the existing
  `mcp_gateway_changed` and `secrets_changed` events.
- **FR-424**: Every new runtime command or flag this feature uses (scope on add and authorize, the
  per-server inspect, removing the identity record) MUST be pinned by an argv-asserting test and listed
  in the host CLI contract as *documentation-derived* until it is run on a real host.

### Key Entities

- **MCP server definition** (new, switchboard-owned, per daemon): what switchboard last registered for a
  server name: kind, URL or command/args/dir, headers (templates with `${placeholder}`, never values),
  client ID, authorization server, default scopes. Kept beside the existing attach marks and removed when
  the server is removed. It is the source for the edit form. The host runtime stays the authority on what
  is registered: a definition whose name the runtime no longer lists is dropped, like a stale mark.
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
- **SC-004**: After a client ID change, the next authorization succeeds without the developer running
  any host command (the identity binding is handled).
- **SC-005**: A server registered with default scopes requests exactly those scopes on every
  authorization, and a one-time override never changes the recorded defaults.
- **SC-006**: No secret value ever appears in a log line, error message, process argument, the
  switchboard registry, or the TUI's screen (masked entry only).

## Assumptions

- The runtime's *remove then add* is the only way to change a registration, and removing deletes the
  OAuth access token (documented). The client secret and its identity binding survive a remove
  (documented).
- The runtime's inspect command reports enough of a registration (URL, headers, client ID, scopes) to
  pre-fill an edit for servers switchboard did not register. Its exact output is **documentation-derived
  and must be reconciled on a real host**. If it does not report a field, FR-402's *named empty field*
  rule covers the gap.
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
