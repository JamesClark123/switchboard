# Contract: in-sandbox invocation channel (`/escape-hatch/run` + wrapper)

Feature 005's AI-invocation path is **not** gRPC. The AI runs inside the sandbox and can only reach the
host over the daemon's **agent-hook HTTP server** (`SWITCHBOARDD_HOOK_ADDR`, default `0.0.0.0:8765`),
which sandboxes reach at `http://host.docker.internal:<port>` — the same server that today receives the
fire-and-forget status hooks at `/hook` (`internal/agent/hooks.go`, `cmd/sxbd/main.go:176-183`). This
doc is the contract for the new sibling route and the injected wrapper. gRPC/client-side additions are
in `switchboard-escape-hatch.proto`.

## Injected wrapper — `<workspace>/.switchboard/escape-hatch`

Written by the daemon at every bring-up (`Launch`/`Refresh`/`AddSandboxKit`), mode `0755`, alongside
the existing `.switchboard/session.json` marker. Because the workspace is bind-mounted into the
container at the same path, the agent invokes it by that absolute path. It embeds this sandbox's id and
the callback URL (mirroring how `agent.BuildSettings` embeds them in the status-hook `curl`).

```sh
#!/bin/sh
# Usage: escape-hatch <command-name> [--workspace <dir>] [-- <args...>]
# Sends the command name and, when the command allows them, a workspace selector and
# argument string — never a command string. The daemon resolves the name against this
# sandbox's allowlist and validates the workspace/args against that command's declared
# constraints before running the fixed, pre-authorized prefix on the host. Async: this
# returns immediately; the result is delivered back to the agent when the run finishes.
name="$1"; shift; workspace=""; args=""
# ...parse --workspace / -- <args>, JSON-escape...
curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"sandbox_id\":\"<embedded-id>\",\"name\":\"$name\",\"workspace\":\"$workspace\",\"args\":\"$args\"}" \
  http://host.docker.internal:<port>/escape-hatch/run
```

- It sends **only** `{sandbox_id, name, workspace, args}` — never a command string. `workspace` and
  `args` are agent-supplied but human-gated: the daemon rejects any argument not matching the command's
  `subcommands`/`args_pattern` and any workspace not matching its `workspaces` allowlist. Agent
  arguments are run as **positional parameters** (`sh -c '<command> "$@"' … <args>`), never re-parsed as
  shell syntax (SC-004).
- Removed (together with the `CLAUDE.md` rule block) when the sandbox's resolved command set is empty.

## `POST /escape-hatch/run`

Request body:

```json
{ "sandbox_id": "<uuid>", "name": "pnpm", "workspace": "src/apps/web", "args": "install" }
```

`workspace` and `args` are optional (empty when the command declares no workspaces / no argument
matching).

Daemon behavior:

1. **Validate** `name` against `Sandbox.escape_hatch_commands` (the persisted resolved allowlist). An
   unknown name (or unknown sandbox) ⇒ **`404`** with `{"error":"command not available"}` and **nothing
   runs** (edge case "command not on the allowlist"; the wrapper prints the error, which the agent sees).
1a. **Validate `args` and `workspace`** against that command's declared constraints (`subcommands` /
   `args_pattern`; `workspaces`). A rejected argument or workspace ⇒ **`400`** with the reason and
   **nothing runs** (the command exists; only the agent's inputs are rejected, so the agent can correct
   and retry).
2. **Enqueue** an `EscapeHatchRun`:
   - `AUTO_RUN` ⇒ status `RUNNING`, execution starts immediately.
   - `REQUIRES_APPROVAL` ⇒ status `PENDING_APPROVAL`; emit a `NEEDS_APPROVAL` notification + an
     `Event.escape_hatch_run`; block the run on a 5-minute decision window (deny-by-default).
3. **Respond immediately** (async — does not wait for the run to finish):

   ```json
   { "run_id": "ehr-42", "status": "running" }      // or "pending_approval"
   ```

4. **On terminal outcome** (SUCCEEDED / FAILED / TIMED_OUT / CANCELLED / DENIED): push the result into
   the agent via `agent.Registry.Prompt(sandbox_id, spec, message)` — a single injected turn such as:

   ```
   [escape-hatch] "install-deps" finished: SUCCEEDED (exit 0).
   --- output (truncated at 1 MiB if noted) ---
   <captured stdout+stderr>
   ```

   and emit a `RUN_COMPLETE` notification + a terminal `Event.escape_hatch_run`. This delivery does not
   depend on any terminal being attached (the agent PTY persists; feature 003).

Notes:
- The endpoint is **name-only**; it never accepts or runs a caller-supplied command string. The daemon
  runs `sh -c "<stored command>"` with `cmd.Dir = workspace_path[/working_dir]`.
- The HTTP response is intentionally tiny and fast; long runs never hold the connection open (avoids the
  agent's Bash-tool timeout — research R1).
- Method other than `POST`, malformed JSON, or missing `sandbox_id`/`name` ⇒ `400`.

## Client-facing flow (gRPC, for reference)

- Approval: daemon `Event.escape_hatch_run` (PENDING_APPROVAL) + `NEEDS_APPROVAL` notification →
  client approval modal → `DecideEscapeHatchRun(run_id, approved)` → daemon proceeds or denies.
- Observability: live `Event.escape_hatch_run` badges on the sandbox row; `ListEscapeHatchRuns(sandbox_id)`
  for session review; `RUN_COMPLETE` notifications in the inbox.
