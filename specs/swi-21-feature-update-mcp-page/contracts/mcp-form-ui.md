# UI contract: MCP gateway view and add/edit form (SWI-21)

Daemon screen › MCP gateway (`ui/mcp.go`, form in `ui/mcp_form.go`). New behaviour appears only when the
daemon advertises the `mcp-edit` capability (research R15); otherwise the view is as before.

## Gateway view keys

| Key | Row | Action | FR |
|-----|-----|--------|----|
| `a` | any | Add (form, add mode) | — |
| `e` | registered server | Edit (form, edit mode; loads `GetMcpServerDefinition` first, `loading…` status) | FR-401 |
| `A` | remote server | Authorize (recorded defaults) — refused with the reason while saving/pending | FR-424 |
| `S` | remote server | Authorize with scopes…: one line of space-separated scopes, or `ctrl+n` *no scopes*; note: "requested exactly as given; no refresh-token scope is added" | FR-418 |
| `R` | pending-replace row | Retry: choose `h` held definition / `p` previous definition, then the form | FR-426 |
| `X` | pending-replace row | Cancel: `y`-only confirm | FR-426 |
| `d`, `space`, `m`, `r` | unchanged | | |

Rows: a registered server shows `rebind pending` in its status column when marked. A pending replace shows
`<server> — not registered (replace failed <age> ago)`. The orchestrator per-worker servers stay hidden.

## Form

Group 1 — **Definition** (edit mode title: `Edit <server> @ <host>`; name shown in the title, not editable):

- Kind (remote / host command). Switching kind clears the remote-only fields with a status line.
- URL · Headers (multi-line; description lists derived secret names `mcp:<server>:<p>` live) · OAuth client
  ID · OAuth authorization server · Scopes (space-separated) — remote only.
- Command · Arguments · Working directory — host command only.
- Attach to every sandbox.
- Field descriptions carry flags: `changed on the host: was <recorded>, host now <reported>`,
  `literal credential on host — convert before re-registering`, `not recoverable from the host — re-enter`,
  `host value cannot be checked against the template`.

Group 2 — **Secrets** (rebuilt from group 1 each time it is entered; values kept by secret name):

- One masked input per distinct placeholder, title `mcp:<server>:<p>`, description one of `set (blank keeps
  it)`, `missing`, `must be re-entered (URL changed)`, `removal failed; will be removed unless re-entered`.
- Client secret (when a client ID is present): title `mcp:<server>:client_secret`, description "Needed only
  for a confidential client." + status; when the server is marked `rebind pending`: "The old client binding
  may still be in place. Saving with the client secret entered clears it and the mark (for a public client,
  remove the stored secret and its binding record instead)."
- If secret storage is unavailable: fields disabled with the reason; a save needing a new secret is refused.

## Literal-credential conversion (FR-423)

On leaving the headers field (and on save) each credential header with literal text opens a `y`-only
prompt: `Authorization: Bearer sk-…` → `Authorization: Bearer ${authorization}` with the literal moved into
that secret field. Derived names: header name lower-cased, `_`→`-`, suffixed `-2`, `-3`, … on a collision or
reserved name. Declining leaves the form open with the refusal message.

## Save sequence

Client-side rules (`mcprules`) → confirmations (each `y`-only, back-out returns to the form with every
value): URL/argument credential warning · dropped unsupported settings · local execution (FR-407) ·
authorization will be discarded · missing / removal-failed / must-re-enter secrets · FR-415 left-in-place
warning → `SaveMcpServer`.

Outcome panel (modal, any key closes): state line (`registered`, `registration unchanged`, `still
registered, authorization may be lost`, `not registered`, plus `the add may still complete on the host`),
host messages verbatim, secrets created / overwritten / rolled back / rollback failed, identity outcome
text, FR-419 sandbox list (`<sandbox> picks up the new definition after a restart or a re-attach from its MCP
overlay`; skipped-at-launch and exclusive-mode notes). Then, in order: FR-413 removal offer (`y`-only, names
each secret), FR-405 authorize offer (`y` starts the existing authorization modal).

On a failure the form stays open with the edits. On a refusal (`STALE_*`) the form reloads in place and the
status line names every dropped secret.

## Sandbox list / MCP overlay (FR-425)

Overlay lists `mcp_skipped` entries: `skipped: being replaced` or `not attached at launch — a to attach`
(exclusive: `missing from this sandbox's fixed set — relaunch to include it`). The sandbox row's third line
appends `· mcp skipped: <n>`.
