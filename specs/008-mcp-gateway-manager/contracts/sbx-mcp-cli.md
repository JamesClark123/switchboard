# Contract: host CLI surface for the MCP gateway (documentation-derived)

The daemon's gateway manager drives the host runtime's CLI. Every argv below is taken from the
Docker Sandboxes docs current at planning time (CLI 0.46.0) and is **pinned by an argv-asserting
test**; `sbx` is not installed in dev, so quickstart Scenario 0 reconciles each row against a real
host before anything else (plan "Risks"). Columns: the operation, the exact argv, what the daemon
reads back, and the timeout class (research R11).

| Op | argv | Reads | Timeout |
|---|---|---|---|
| list | `sbx mcp ls` | table: `NAME  TYPE  URL/COMMAND` (header-offset parse; extra columns ignored) | 60 s |
| auth state | `sbx mcp auth status <name> --json` | JSON; mapped to AUTHORIZED/UNAUTHORIZED; parse failure ⇒ UNKNOWN | 60 s |
| add remote | `sbx mcp add <name> --url <url> --skip-auth` | exit status + combined output (verbatim on error) | 2 min |
| add local | `sbx mcp add <name> --command <cmd> [--args a,b,c] [--dir <dir>] --skip-auth` | same | 2 min |
| remove | `sbx mcp rm <name>` | output lines mentioning `sbx secret rm` → `notes` | 2 min |
| authorize | `sbx mcp auth <name>` | first `https://…` token → `url`; remaining lines → `message`; exit 0 ⇒ AUTHORIZED | 10 min, killable |
| load (additive) | `sbx mcp load <name> --sandbox <sandboxName>` | exit status; failure ⇒ skip + launch log line | 60 s per server |
| pre-load (exclusive) | `sbx create --name <N> claude <ws> [--kit …] --static-mcp a,b` | existing create handling | existing |
| version | `sbx --version` | first `\d+\.\d+\.\d+` | 10 s |

Rules:

- `--skip-auth` is **always** passed on `add`; authorization is always the separate `auth` step
  (research R2), so no timeout can lose a registration.
- The sandbox name passed to `load`/`--sandbox` is the `--name` the daemon assigned at create (the
  runtime addresses sandboxes by name).
- Argument lists for local servers are joined with commas exactly as the docs show; a value that
  itself contains a comma is refused at the RPC boundary with a clear message (the docs document no
  escaping).
- The daemon never passes a flag that does not appear in this table.

Known-unknowns to settle in Scenario 0 (research R9): the exact `--version` output format; whether
`--static-mcp` composes with `--kit`; `sbx mcp ls` column spacing; `auth status --json` field names;
`sbx start` and `sbx ls --json` and `sbx options --json` existence.
