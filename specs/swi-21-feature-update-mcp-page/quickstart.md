# Quickstart: MCP Page Quality of Life (SWI-21)

Validation scenarios for the implemented feature. Rules, shapes and flags are in
[contracts/](./contracts/) and [data-model.md](./data-model.md).

## Prerequisites

- `sxbd` and `sxb` built from the implement branch (`make build`).
- A host with `sbx` ≥ the runtime baseline, signed in (`sbx login`), for Scenario 0 and the host scenarios.
- A test remote MCP server you control (or Notion / Linear) and, for US3, a provider app with a
  pre-registered confidential client.
- Automated gates: `make fmt-check vet lint test cover` (≥ 90% per module), `make e2e`.

## Scenario 0 — Real-host reconciliation (FR-422)

Run each row of [contracts/sbx-cli-delta.md](./contracts/sbx-cli-delta.md) by hand, record the outcome in
that file, and flip each behaviour flag in `internal/mcp/hostflags.go` that the run settles. Expected: every
argv runs as documented; flags recorded with the date.

## Scenario 1 — Edit a remote server (US1)

1. Add `demo` with URL A and header `Accept: application/json`. Open Edit (`e`).
   **Expect** URL, header, mark pre-filled; name not editable.
2. Change the URL to B, add `X-Trace: 1`, save, confirm the authorization warning.
   **Expect** outcome `registered (replaced)`, the FR-419 list, the authorize offer; `sbx mcp inspect demo`
   shows URL B and both headers; the mark is unchanged.
3. Edit again and toggle only *Attach to every sandbox*. **Expect** `registration unchanged`, no `mcp rm` in
   the daemon log.

## Scenario 2 — Secrets on the form (US2)

1. Add `acme` with `Authorization: Bearer ${api-key}`; enter a value in `mcp:acme:api-key`; save.
   **Expect** Secrets view lists `mcp:acme:api-key` masked; no visit to Secrets.
2. Type `Authorization: Bearer sk-live-123` instead. **Expect** the conversion prompt; accepting moves the
   literal to `mcp:acme:authorization`.
3. Edit, remove the header, save. **Expect** the FR-413 offer naming `mcp:acme:api-key`.

## Scenario 3 — Pre-registered client (US3)

1. Add `slack` with client ID C1 and its client secret. **Expect** the secret is stored before the add
   (daemon log order) and authorization reaches the consent page.
2. Edit: client ID C2 with C2's secret. **Expect** identity outcome `cleared`; the next authorization
   succeeds.
3. Edit: client ID C3, no secret. **Expect** the left-in-place warning, row shows `rebind pending`, and the
   public-client removal offer (research R9).

## Scenario 4 — Scopes (US4)

1. Add `scoped` with scopes `read write`; authorize. **Expect** the link's `scope` parameter holds both
   (plus `offline_access` if advertised).
2. `S`, enter `read`. **Expect** exactly `read`; Edit still shows `read write`.
3. `S`, `ctrl+n`. **Expect** no `scope` parameter.

## Scenario 5 — Failed replace and pending replace (FR-406, FR-426)

Use the e2e stub `sbx` (`stubSbx` in `switchboard-tui-e2e/harness.go`, whose `mcp add` fails for a URL
containing `refuse.invalid`) or a real host where the new URL is refused.

1. Edit `demo` to a refused URL with a new header secret. **Expect** `not registered`, the secret named as
   rolled back, the form still open.
2. Quit the TUI, reopen. **Expect** the pending-replace row with age; `R` › previous definition › save
   restores it; secrets must be re-entered.
3. Repeat the failure, then `X`. **Expect** the `y`-only confirmation; the row and mark are gone.
4. Launch a sandbox while a replace of a marked server is pending. **Expect** `skipped: being replaced` in
   the launch log and the overlay; after a successful Retry the entry reads `not attached at launch`.

## Scenario 6 — Concurrency (FR-404, FR-424)

1. Open Edit on two clients; save on one, then on the other. **Expect** the second is refused and reloads
   in place with the host values.
2. Start an authorization, then save a client-ID change from another client. **Expect** refused: finish or
   cancel the authorization first. A header-secret-only save is allowed.
3. Restart `sxbd` with a form open; save. **Expect** *stale (daemon restarted)*, form reloaded.
