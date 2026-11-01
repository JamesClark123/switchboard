# Contract: `sbx secret` as driven by the daemon (FR-104)

Every argv the daemon's `internal/secret` package runs, with its documented source. All rows are
**documentation-derived** (Docker Sandboxes *configuration/credentials*, *security/credentials* and
the `sbx secret set` reference, read 2026-10-12) — `sbx` is not installed in dev — and are pinned by
`TestArgvTable` in `internal/secret/manager_test.go`. Quickstart Scenario 0 reconciles each row on a
real host first.

| Step | argv | Parsed / assumed | Bound |
|---|---|---|---|
| list | `sbx secret ls` | table `SCOPE  TYPE  NAME  SECRET` (older docs: `SCOPE  SERVICE  SECRET`); `(global)` ⇒ scope `global`, otherwise the sandbox name; missing TYPE ⇒ `service`; the SECRET cell is the runtime's masked value and is relayed verbatim | 30 s |
| store | `sbx secret set <name> [--sandbox <sandbox>] --force`, value on **stdin** + `\n` | the documented non-interactive form (`echo "$TOKEN" \| sbx secret set -g github`); global is the runtime's default scope; `--force` overwrites without a prompt (documented beside `--token`, assumed to apply to stdin too — **reconcile**). The value never enters argv, logs or errors | 60 s |
| remove service | `sbx secret rm <name> [--sandbox <sandbox>] -f` | `-f` is documented on registry removals; assumed accepted for service removals — **reconcile** (drop it if rejected; if `rm` prompts without it, keep it) | 60 s |
| remove registry | `sbx secret rm --registry <host> [--sandbox <sandbox>] -f` | documented form | 60 s |
| store a form secret (SWI-21) | `sbx secret set mcp:<name>:<placeholder> --force`, value on stdin | **Documentation-derived (SWI-21, 2026-10-31)**: the store row above, global scope, run by `SaveMcpServer` before the `mcp add` (FR-410); no `secrets_changed` per call, one per save | 60 s |
| remove identity binding (SWI-21) | `sbx secret rm mcp:<name>:client_secret:identity -f` | **Documentation-derived (SWI-21)**: the service-removal row above; a failure is re-checked against `sbx secret ls` (FR-415, flag `secretListShowsIdentity`) | 60 s |
| custom secrets | — | `sbx secret set-custom` entries are listed but never removed from switchboard (hint to the host) | — |

Rules:

- Names: service ids (`anthropic`, `github`, a kit's `credentials[].service`), gateway header
  placeholders `mcp:<server>:<placeholder>`, registry hosts. Validated as
  `^[A-Za-z0-9][A-Za-z0-9._:@/-]*$` before any call.
- Values are single-line (the runtime reads one line from stdin); multi-line secrets stay a host
  operation.
- Every successful mutation emits `Event.secrets_changed{host_id}`; nothing is persisted by the
  daemon — the host keychain is the only store.
- Availability: the daemon answers `FAILED_PRECONDITION` when its `sbx` is missing or below
  `sbxkit.MinSbxVersion` (same probe as the gateway; the gateway's own "signed in" state is not
  required).

Known-unknowns to settle in Scenario 0: the `ls` column set on the installed version (`TYPE` vs the
older `SERVICE`), whether `set` reads stdin when piped from a non-TTY daemon, whether `--force` is
accepted beside stdin, whether `rm` accepts `-f` for service secrets, and the exact "not found" text.
