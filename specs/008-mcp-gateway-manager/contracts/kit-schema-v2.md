# Contract: kit schema version 2 (what switchboard writes, reads, and refuses)

Switchboard authors **only** `schemaVersion: "2"` mixins (FR-091). The YAML shape is the client
model in [data-model.md](../data-model.md#kit-schema-2--client-model-storekit). The host runtime's
validator (`sbx kit validate`) remains the authority (FR-094).

## Legacy (v1) → v2 mapping applied on load (FR-092)

| v1 | v2 | Loss? |
|---|---|---|
| `network.allowedDomains` | `permissions.network.allow` | none |
| `network.deniedDomains` | `permissions.network.deny` | none |
| `commands.install[]` | `setup.install[]` | none |
| `commands.initFiles[]` | `setup.files[]` | none |
| `commands.startup[]` | `setup.startup[]` | none |
| `agentContext` / `memory` | `agentInstructions.content` | none |
| `environment.variables` | `environment.variables` | none |
| `credentials.sources.<id>.env[0]` | `credentials[].{service: <id>, apiKey.name: env[0]}` | **inject domains unknown** → note |
| `credentials.sources.<id>.env[1..]` | — | **dropped** (v2 declares no host source) |
| `environment.proxyManaged[]` | `credentials[].apiKey.proxyManaged = true` for the matching `apiKey.name` | unmatched names **dropped** |
| anything else | — | **dropped**, reported by path |

The report (`Migration`) is shown in the editor; nothing on disk changes until the kit is saved.

## Attach to an existing sandbox (FR-095)

`sbx kit add` applies only `environment.variables`, `setup.install` and `permissions.network.allow`.
A kit declaring any of the following is **refused before any RPC**, with the sections named and
"apply at launch instead" offered:

`permissions.network.deny` · `setup.files` · `setup.startup` · `credentials` · `agentInstructions`

(A bundled `files/` tree is never authored by the editor; a hand-authored kit that has one is
likewise refused for attach.)

## Schema 3

Refused for attach and launch with: "the runtime's built-in agents are version-2 environments and
do not compose with version-3 kits" (FR-099). Detection: `schemaVersion: "3"` or `kind: workload |
set`.
