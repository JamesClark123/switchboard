# Contract: host CLI surface for the MCP gateway (documentation-derived)

The daemon's gateway manager drives the host runtime's CLI. Every argv below is taken from the
Docker Sandboxes docs current at planning time (CLI 0.46.0) and is **pinned by an argv-asserting
test**; `sbx` is not installed in dev, so quickstart Scenario 0 reconciles each row against a real
host before anything else (plan "Risks"). Columns: the operation, the exact argv, what the daemon
reads back, and the timeout class (research R11).

| Op | argv | Reads | Timeout |
|---|---|---|---|
| list | `sbx mcp ls` | **Confirmed 2026-10-10**: table `NAME  TRANSPORT  STATUS  GATEWAY  DETAIL` (e.g. `notion  remote http  needs auth  LOCAL, managed by you  sbx mcp auth notion`) — no URL column. Header-driven parse; `TRANSPORT` starting with `remote` ⇒ remote kind; `STATUS` is the auth fallback | 60 s |
| auth state | `sbx mcp auth status <name> --json` | **Confirmed 2026-10-10**: `[{"server_name":"notion","status":"unauthorized"}]` — a JSON array; the entry for the name decides (`authorized`/`unauthorized`); parse failure ⇒ UNKNOWN, then the `ls` STATUS column | 60 s |
| add remote | `sbx mcp add <name> --url <url> [--header 'Name: value' …] [--client-id <id>] [--oauth-authorization-server <file\|url>] --skip-auth` | FR-103: one `--header` per validated entry, before `--skip-auth`; FR-105 (SWI-20): `--client-id`/`--oauth-authorization-server` after the headers, both `--url`-only per the docs; a confidential client's secret is the host secret `mcp:<name>:client_secret`, set before adding (no `--client-secret` flag). Their combination with `--skip-auth` is **assumed, not verified on a real host**; credential headers use `${placeholder}` + the secret `mcp:<name>:<placeholder>` (contracts/sbx-secret-cli.md).  **Confirmed 2026-10-10**: `--skip-auth` registers without opening a browser. exit status + combined output (verbatim on error) | 2 min |
| add local | `sbx mcp add <name> --command <cmd> [--args a,b,c] [--dir <dir>] --skip-auth` | same | 2 min |
| remove | `sbx mcp rm <name> --force` | **Confirmed 2026-10-10**: without `--force` it prompts `Remove MCP server "<name>" and its OAuth credential? (y/N)`, so the daemon always passes `--force`. Output `No local OAuth token found for MCP server "<name>"` + `MCP server "<name>" removed`; every line but the bare confirmation → `notes` | 2 min |
| authorize | `sbx mcp auth <name>` | **Confirmed 2026-10-10**: prints OAuth scope lines, then `Open this URL to authorize MCP server "<name>":` and the bare URL on the **next** line (every line is scanned); first `https://…` token → `url`; remaining lines → `message`; exit 0 ⇒ AUTHORIZED | 10 min, killable |
| load (additive) | `sbx mcp load <name> --sandbox <sandboxName>` | **Confirmed 2026-10-12**: on a sandbox fresh from `sbx create` (no agent attached) prints `MCP server "notion" loaded into sandbox "probe-c" (live)`, rc 0 — on a dynamic *and* a `--static-mcp` sandbox alike. Unregistered name: rc 1, `error: add "ghost" to sandbox "probe-c": add MCP gateway server: request failed: 404 Not Found: MCP server "ghost" is not registered (run 'sbx mcp add ghost ...')`. exit status; failure ⇒ skip + launch log line | 60 s per server |
| pre-load (exclusive) | `sbx create --name <N> claude <ws> [--kit …] --static-mcp a,b` | **Confirmed 2026-10-12** on sbx 0.47: `--static-mcp` composes with `--kit` (rc 0 with both; `--help`: "Accepts a comma-separated list … repeated flags … or a mix; all forms accumulate into the same set. The set is chosen once at creation time. Local sandboxes take names registered with 'sbx mcp add'"). A `needs auth` server is accepted at create. Usage is `sbx create [flags] AGENT\|SANDBOX_KIT [PATH...]`; the banner ends with `To connect to this sandbox, run: sbx run --name <N>` | existing |
| version | `sbx version` | prints `sbx version: v0.47.0 <commit>`; first `\d+\.\d+\.\d+` is taken (leading `v` ignored). **Confirmed on a real host 2026-10-10** — there is no `--version` flag | 10 s |

Rules:

- `--skip-auth` is **always** passed on `add`; authorization is always the separate `auth` step
  (research R2), so no timeout can lose a registration.
- The sandbox name passed to `load`/`--sandbox` is the `--name` the daemon assigned at create (the
  runtime addresses sandboxes by name).
- Argument lists for local servers are joined with commas exactly as the docs show; a value that
  itself contains a comma is refused at the RPC boundary with a clear message (the docs document no
  escaping).
- The daemon never passes a flag that does not appear in this table.

Scenario 0 is fully reconciled as of 2026-10-12 (research R9): every row above was run on a real
host. `sbx start` is absent and the daemon resumes a stopped sandbox with `sbx exec <N> true`
instead. Residual, non-blocking checks: whether a killed `sbx mcp auth` leaves the registration
intact, and the `kit add` text for a kit whose *only* blocked section is `credentials`,
`agentInstructions` or `setup.files` (FR-095 refuses them regardless). Settled 2026-10-10: the version command, `--skip-auth`, the `ls` table,
the `auth status --json` shape, the `auth` transcript, and `rm --force`. Settled 2026-10-12:
`--static-mcp` composes with `--kit` on `sbx create`; `mcp load` works on a just-created sandbox in
both gateway modes and names an unregistered server with a 404.
