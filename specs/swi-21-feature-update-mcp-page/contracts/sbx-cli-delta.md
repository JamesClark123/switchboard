# Host CLI delta: SWI-21 (extends `specs/008-mcp-gateway-manager/contracts/sbx-mcp-cli.md` and `sbx-secret-cli.md`)

Every row is **documentation-derived** (docs.docker.com `reference/cli/sbx/mcp/*` and
`ai/sandboxes/mcp-gateway`, read 2026-10-30 and 2026-10-31) until quickstart Scenario 0 runs it on a real
host (FR-422). Each argv is spelled only in `internal/mcp/cli.go` / `internal/secret/cli.go` and pinned by an
argv-asserting test.

| Operation | argv | Notes | Bound |
|-----------|------|-------|-------|
| add remote (extended) | `sbx mcp add <name> --url <url> [--header 'N: v' …] [--client-id <id>] [--oauth-authorization-server <x>] [--scope <s> …] --skip-auth` | one `--scope` per default scope, after the authorization server (FR-416). | 2 min, process group killed and reaped on timeout (R7) |
| remove (in a save) | `sbx mcp rm <name> --force` | unchanged argv; now reaped before the re-check (R7) | 2 min |
| authorize, one-time scopes | `sbx mcp auth <name> --scope <s> …` | requested exactly as given, no `offline_access` (FR-418) | 10 min, killable |
| authorize, no scopes | `sbx mcp auth <name> --no-scope` | never combined with `--scope` | 10 min, killable |
| inspect | `sbx mcp inspect <name> --json` | JSON schema undocumented; parsed tolerantly (R11); failure ⇒ nothing reported | 60 s |
| auth status (scopes) | `sbx mcp auth status <name> --json` | existing argv; scope keys parsed when present (R11) | 60 s |
| remove identity binding | `sbx secret rm mcp:<name>:client_secret:identity -f` | existing secret-rm argv; failure re-checked against `sbx secret ls` (FR-415) | 60 s |
| store a form secret | `sbx secret set mcp:<name>:<p> --force` (value on stdin) | existing argv, global scope | 60 s |

## Behaviour flags (`internal/mcp/hostflags.go`)

| Flag | Default | Decides | Scenario 0 check |
|------|---------|---------|------------------|
| `inspectReportsHeaders` | false | an empty header section from inspect is *not reported* | register a server with no headers; inspect it |
| `authStatusReportsScopes` | false | an empty scope list from auth status is *not reported* | register with and without `--scope`; read auth status |
| `inspectReturnsTemplates` | true | inspect shows `${…}` templates, not resolved values (FR-423) | register `Authorization: Bearer ${k}` with `k` set; inspect |
| `scopeCommaLiteral` | false | `,` refused in scopes (FR-417) | add `--scope 'a,b'`; read auth status / consent URL |
| `secretListShowsIdentity` | false | the `:identity` record is visible to the re-check (FR-415) | authorize a pre-registered client; `sbx secret ls` |
| `killedAddMayComplete` | true | a failed add leaves the host state unknown (FR-406) | kill `sbx mcp add` mid-run; list |
| `mcpLoadWorksInExclusive` | false | FR-425 exclusive-mode note | `mcp load` into a `--static-mcp` sandbox |

Also recorded by Scenario 0: whether inspect reports URL, client ID and authorization server (Assumptions),
and the placeholder grammar.
