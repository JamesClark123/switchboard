# Quickstart & Validation: MCP Gateway Manager & Daemon Screen (+ kit schema v2)

Runnable scenarios proving the feature end-to-end, mapped to the spec's user stories and success
criteria. Mechanics: [research.md](./research.md); entities: [data-model.md](./data-model.md); wire
and CLI contracts: [contracts/](./contracts/).

## Prerequisites

- `sxbd serve` on each target host and `sxb` connected, both built from this feature's branch.
- For the real path: Docker + `sbx` ≥ 0.36 **signed in** (`sbx login`) on each daemon host. Without
  them, `make test` and `make e2e` cover everything except Scenario 0 via stub `sbx`.
- Two daemons help (local + one SSH host); a second one with a *different* `sbx` version is ideal
  for the baseline checks.
- A public MCP endpoint you can authorize against (the docs use `https://mcp.notion.com/mcp`) and a
  harmless local command (`npx @playwright/mcp@latest`).

## Scenario 0 — Reconciliation gate (research R1/R2/R9) ⚠ FIRST, on a host with real `sbx`

Record each answer in `research.md` before trusting anything below:

```bash
sbx --version                              # pin the output format the version parser must match
sbx mcp ls                                 # confirm the NAME/TYPE/URL-COMMAND header + spacing
sbx mcp add probe --url https://mcp.notion.com/mcp --skip-auth   # registers without a browser?
sbx mcp auth status probe --json           # confirm field names → AUTHORIZED/UNAUTHORIZED mapping
sbx mcp auth probe                         # prints an "Open this URL…" line, then blocks? Ctrl-C: registration intact?
sbx create --name probe-sb claude . --static-mcp probe           # composes with --kit? gateway static?
sbx mcp load probe --sandbox probe-sb      # live load on a running sandbox
sbx mcp rm probe                           # prints `sbx secret rm` hints? error when absent?
sbx start probe-sb || sbx run -d --name probe-sb                 # which one exists (R9 inventory)
sbx ls --json ; sbx options --json         # exist? (R9 inventory)
sbx kit add probe-sb <dir-with-startup>    # exact rejection text for blocked sections (FR-095)
```

Expect: every argv in [contracts/sbx-mcp-cli.md](./contracts/sbx-mcp-cli.md) confirmed or amended,
and `sbxkit.MinSbxVersion` confirmed as the first schema-2 release.

## Scenario 1 — Register, authorize, remove on one daemon (US1; SC-001, SC-003, SC-004)

1. From the sandbox list press **shift+→**: the daemon screen lists every known daemon with its
   state; highlight a connected one, `enter`, choose **MCP gateway**.
2. `a` → name `notion`, remote, `https://mcp.notion.com/mcp`, leave *attach by default* off.
   Expect the row within seconds with kind *remote*, state *unauthorized*; on the host
   `sbx mcp ls` shows it; **the other daemon's list is unchanged** (SC-003).
3. `A` (authorize) on the row: a URL appears **in the TUI** with a 10-minute countdown. Open it
   where you are; on completion the row turns *authorized*. Repeat and press `esc` mid-wait:
   the row stays, state *unauthorized* (SC-013).
4. `a` → `pw`, local, command `npx`, args `@playwright/mcp@latest`: the isolation warning must be
   accepted before anything is sent (FR-076).
5. `d` on `pw`: it disappears from the TUI and from `sbx mcp ls`; any `sbx secret rm` hint is shown.
6. Negative: add a name with a space → the host's diagnostic verbatim, list unchanged (SC-004).

## Scenario 2 — Attach by default at launch, both modes (US2; SC-002, SC-008)

1. In the gateway view mark `notion` attach-by-default (`space`). Mode shows *additive*.
2. Launch a sandbox on that daemon (`n`). In the launch log (`l`) expect `mcp load notion …`; the
   sandbox row shows `mcp: notion`; inside the sandbox the agent lists Notion's tools **and** can
   still discover other registered servers (additive).
3. Switch the daemon to *exclusive* (`m` in the gateway view). Launch another sandbox: the create
   argv carries `--static-mcp notion`; the agent has Notion and no discovery tools. The first
   sandbox is unchanged (FR-100).
4. Remove `notion` on the host directly (`sbx mcp rm notion`) and launch again: the launch
   **succeeds**, the log says the stale mark was skipped and the mark is gone from the view (SC-008).
5. Launch on a daemon with no marks: no `mcp` lines, row shows no servers, gateway dynamic (FR-085).
6. Stop/start and refresh a sandbox launched in step 2: `mcp: notion` persists (FR-082).

## Scenario 3 — Kit schema v2 (US3; SC-009, SC-010, SC-011, SC-012)

1. Open the kit editor (`K`), save a kit with one entry in every section. `cat
   ~/.config/switchboard/kits/<id>/spec.yaml` → `schemaVersion: "2"`, `permissions.network`,
   `setup.*`, `agentInstructions`, `credentials[]`. Validate (`v`) on a baseline host: no warnings.
2. Drop a v1 kit into `kits/legacy/spec.yaml` (use the previous release's grammar incl.
   `credentials.sources.github.env: [GITHUB_TOKEN, GH_TOKEN]` and `environment.proxyManaged`). Open
   it: a *migrated* banner lists carried sections and the drop of `GH_TOKEN` with its reason; the
   file is unchanged until you save; after save it is v2 (SC-010).
3. `A` a kit with a startup command onto a running sandbox: refused immediately, naming
   `setup.startup`, no RPC made (check the daemon `--debug` log) (SC-011). Launch with the same kit:
   applies in full.
4. On the host with an older `sbx` (< 0.36): the daemon screen shows the version and *below
   baseline*; `A`/`v` are refused with both versions named; launching a kit-less sandbox works
   (SC-012). Attach a `schemaVersion: "3"` kit: refused with the v3 reason (FR-099).

## Scenario 4 — Daemon screen navigation (US4; SC-005)

1. Sandbox list, cursor on the third row → **shift+→** → daemon screen; **shift+←** → the third row
   is still selected. Reverse direction preserves the daemon cursor.
2. A disconnected row shows its reason and offers `c` connect (password prompt for SSH; blank =
   key auth); after success the row turns connected and `enter` reveals *MCP gateway*. `x`
   disconnects; an open gateway view for that daemon closes with a message (FR-071).
3. Help footers on both screens show `shift+←/→` and `,` settings; sandbox keys (`s`, `d`, …) do
   nothing on the daemon screen.

## Scenario 5 — Startup sign-in (US5; SC-006, SC-007)

1. Save two SSH hosts: one key-auth, one password. Settings (`,`) shows *automatic host sign-in:
   on*. Restart `sxb`: the key host connects silently; a **centered** masked prompt names the
   password host; enter the password → both hosts' sandboxes appear (SC-006). At most one prompt per
   host; the local daemon is never prompted.
2. Restart; enter a wrong password: the failure card names the host and reason within 30 s; `r`
   re-prompts, `esc` skips; the list is usable with the other host connected (SC-007). Connect the
   skipped host from the hosts screen afterwards — unchanged behavior.
3. Settings → toggle off → restart: zero prompts; `cat ~/.config/switchboard/settings.toml` shows
   `auto_connect_hosts = false`. Delete the file → defaults (on) with a notice on the settings screen.

## Automated coverage map

| Layer | Covers |
|---|---|
| `switchboardd/internal/mcp` unit (stub `sbx`) | argv pinning (R1), `ls`/`auth status` parsing, reconcile, settings bucket, authorize URL capture + 10-min kill, load-after-create ordering |
| `switchboardd/internal/sandbox` unit | `McpAttach` resolution in `Launch`, `--static-mcp` argv, skip-on-failure logging, record fields fixed across restart/refresh |
| `switchboardd/internal/grpc` | new RPC round-trips, FAILED_PRECONDITION on baseline/gateway unavailability, `Event.mcp_gateway_changed` emit |
| `switchboardd/internal/sbxkit` | `MinSbxVersion` compare, version parsing, `--help`-only manifest |
| TUI `store` | v1→v2 migration table, `AttachBlockers`, `settings.toml` defaults + atomic save |
| TUI `ui` (`teatest`) | shift+←/→ with preserved cursors, daemon rows + connect/disconnect, gateway view ops + countdown, settings toggle, startup sequence (scripted dial fake), kit editor v2 sections + migration banner, attach refusal |
| E2E (stub `sbx` ≥ baseline) | launch with marks in both modes shows `mcp` argv in `sbx.log`; daemon screen reachable; settings persisted across a restart of the TUI |
