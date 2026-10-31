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

If the definition read for `R` or `X` shows the replace has ended elsewhere (`GONE`, or the server is registered
again with no pending replace), no form or confirm opens: the TUI re-lists and says "the pending replace of
<server> has ended (retried, cancelled or registered by another path)". A definition read (`e`, `R`, `X`) that
lands after another form or modal opened (`a`, an outcome panel, the Retry chooser) is dropped.
| `space`, `m` | registered server | Toggle the mark (unchanged), except on a `saving…` or `recovering interrupted save` row: shows that status and does not call `SetMcpServerDefault` | FR-425 |
| `d`, `r` | unchanged | | |

Rows: a registered server shows `rebind pending` in its status column when marked, `saving…` while a save of it
runs or its result is still being recorded (`save_in_progress`; this client's own save after `UNAVAILABLE`
too), and `recovering interrupted save` while a leftover in-progress
record from before a daemon restart waits for recovery (`recovering`; Edit, Remove, Authorize and the mark
toggle are refused until it clears, so `e` shows the status instead of opening the form). `e` and `R` on a
`saving…` row are refused **locally**, like `m`/`space`: the TUI shows "a save of this server is still running;
try again later" and does not open the form or call `GetMcpServerDefinition`, because that read takes no lock and
would pre-fill the form from a definition that is changing. If the row was not yet `saving…` when `e`/`R` was
pressed and the read reports `save_in_progress`, the TUI shows the same message instead of opening the form
(research R11). Only `d` and `X` on a `saving…` row reach the daemon, which refuses them at once
(`BEING_REPLACED`, same message); the TUI shows it on the status line. The message is neutral on purpose: the row
may be the developer's own save after `UNAVAILABLE`, whose result can take until the settings store recovers. A pending replace shows
`<server> — not registered (replace failed <age> ago)`. The orchestrator per-worker servers stay hidden.

## Form

Group 1 — **Definition** (edit mode title: `Edit <server> @ <host>`; name shown in the title, not editable):

- Kind (remote / host command). Switching to host command clears the remote-only fields with a status line;
  switching back to remote restores them, unless remote values were typed in between (huh's Select sets the
  value as the highlight moves, so passing over the option must not lose them).
- URL · Headers (multi-line; description lists derived secret names `mcp:<server>:<p>` live, as typed; with
  the add form's Name still blank, secret names use `<name>` as the server, and move when the name is set) · OAuth client
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
- If secret storage is unavailable: a note with the reason replaces the inputs (huh has no disabled input); a
  save needing a new secret is refused.
- Statuses come from `stored_secrets` (also read in add mode with `for_add`), so a placeholder typed after the
  form opened still shows *set* or *missing*.

## Literal-credential conversion (FR-423)

When focus leaves group 1 (tracked from the focused group; the form is suspended, then rebuilt with
`reopenForm`), and again on save, each credential header with literal text opens a `y`-only
prompt: `Authorization: Bearer sk-…` → `Authorization: Bearer ${authorization}` with the literal moved into
that secret field. Derived names: header name lower-cased, `_`→`-`, suffixed `-2`, `-3`, … on a collision or
reserved name. Declining leaves the form open with the refusal message. Accepting on save rebuilds the form, so
a save that is then refused or fails keeps the converted header. While secret storage is unavailable, a typed
literal is not offered conversion (its secret field does not exist): the form returns to Headers with a refusal
naming the reason.

A header that still equals the view's redacted `LITERAL_ON_HOST` value (`Bearer ••••`) is converted to the
template **without** moving the redacted text: the new secret field stays blank and `missing`, with the status
`re-enter the credential (held on the host)`. Declining for such a header continues into group 2 with the
`literal credential on host` warning, so a secrets-only or mark-only save is still possible. Only a save that
would re-register is blocked.

A save refused `UNVERIFIED` shows the refusal's `message` as sent by the daemon ("couldn't read the server
from the host; try again", or "recovering an interrupted save; try again" for a leftover), keeps the form as it
was (no reload) and re-lists the servers, so a leftover's row shows `recovering interrupted save`.

A save refused `BEING_REPLACED` (the form was opened just before another client's save of the server started)
keeps the form and every value as they were (no reload), shows the message on the status line and re-lists, so
the row shows `saving…`. A later save from the same form is refused `STALE_CHANGED` and reloads in place by the
FR-404 rules (edited fields keep the developer's values) **only when the other save re-registered the server**.
When the other save was switchboard-only (mark and/or secrets, `recording`), the fingerprint (R5) still matches,
so the later save goes through with the form's mark, which may revert the other save's mark change without a
warning. That is the same last-writer-wins on the mark as two saves in sequence (FR-404 covers the definition,
not the mark); fingerprinting the mark is a deliberate scope cut.

A save that fails `UNAVAILABLE` carrying the `ErrorInfo` detail `{reason: "MCP_SAVE_NOT_RECORDED", domain:
"switchboard"}` (the save ran, but the daemon has not recorded the result yet and keeps retrying the write; the
message says whether the host was changed) closes the form, because saving again is pointless, and shows the message as a status line.
The row shows `saving…` until the daemon records the result and emits `mcp_gateway_changed`; the view then
re-lists. The TUI does **not** open the outcome panel or its follow-up offers (FR-405 authorize, FR-413
removal) for this save when the result lands later. This is a deliberate scope cut for a rare registry failure:
the result shows on the row (`registered`, `rebind pending`, or a pending-replace row with Retry/Cancel), and
Authorize and the Secrets view stay available from the gateway view. A bare `UNAVAILABLE` (daemon unreachable,
connection dropped: the save may never have run) is a plain error: the form stays with every edit and typed
secret, and the row is not marked `saving…`.

## Save sequence

Client-side rules (`mcprules`) → confirmations (each `y`-only, back-out returns to the form with every
value): URL/argument credential warning · dropped unsupported settings · local execution (FR-407) ·
authorization will be discarded · missing / removal-failed / must-re-enter secrets · FR-415 left-in-place
warning → `SaveMcpServer`.

Outcome panel (modal, any key closes; titled `Saved <server>`, or `The save of <server> did not complete` for
`still registered` / `not registered`): state line (`registered`, `registration unchanged`, `still
registered, authorization may be lost`, `not registered`, plus `the add may still complete on the host`),
host messages verbatim, secrets created / overwritten / rolled back / rollback failed, identity outcome
text, FR-419 sandbox list (`<sandbox> picks up the new definition after a restart or a re-attach from its MCP
overlay`; skipped-at-launch and exclusive-mode notes). Then, in order: FR-413 removal offer (`y`-only, names
each secret), FR-405 authorize offer (`y` starts the existing authorization modal) — only for a registered remote
server whose auth state is not `authorized` or `no auth` (an unknown state still offers).

A save left with esc while running ("saving… in the background") is matched to its form by a per-form
generation, never by name alone, so its reply never touches a later form, even of the same name. If it lands
while another form is open, no outcome panel or follow-up opens over that form: the status line reads `saved
<server> in the background: <state>` (or an `error:` line when the replace did not complete) and the view
re-lists.

On a failure the form stays open with the edits. Leaving a form that holds a pending replace (after a
`NOT_REGISTERED` outcome, or in retry mode) opens the same `y`-only Cancel confirmation as `X`; accepting calls
`CancelMcpPendingReplace`, declining keeps the form. On a refusal (`STALE_*`) the form reloads in place and the
status line names every dropped secret.

## Sandbox list / MCP overlay (FR-425)

Overlay lists `mcp_skipped` entries: `skipped: being replaced` or `not attached at launch — a to attach`
(exclusive: `missing from this sandbox's fixed set — relaunch to include it`). The sandbox row's third line
appends `· mcp skipped: <n>`.
