# Contract: the orchestrator as seen from a worker sandbox

The sandbox-facing surface of feature 009 is **not** gRPC. A worker's agent reaches its
orchestrator through the host runtime's MCP gateway, which the sandbox's **owning daemon** points
at an MCP endpoint it serves on the agent-hook HTTP server (`SWITCHBOARDD_HOOK_ADDR`, default
`0.0.0.0:8765`, the server behind `/hook` and `/escape-hatch/run`). Tool calls are attributed to
the sandbox by the owning daemon and relayed to the orchestrator over the link it opened
(`OrchestratorLink`, see `switchboard-orchestrator.proto`). This document is the contract for the
registration, the endpoint, the four tools, the briefing and the run frame. Decisions: research
R4, R5, R6, R12.

## Per-worker gateway registration (owning daemon, `sbx`)

| Moment | argv | Notes |
|---|---|---|
| Attach / launch-attach (FR-107/FR-110/FR-150) | `sbx mcp add sxb-orch-<sandbox8> --url http://127.0.0.1:<hookport>/orchestrator/w/<token>/mcp --skip-auth` | `<sandbox8>` = first 8 hex of the sandbox id; `<token>` = 32 random bytes, hex, minted per attach. The SSRF warning `sbx` prints for a loopback URL is logged, not treated as failure. **Documentation-derived — quickstart Scenario 0.** |
| then | `sbx mcp load sxb-orch-<sandbox8> --sandbox <ref>` | Live tool-list update (008 reconciled on dynamic and static sandboxes). At launch-attach it runs where additive marks are loaded: after `create`, before the agent PTY starts. Skipped (and retried at next bring-up) when the sandbox is not running. |
| Detach / destroy / orchestrator removal | `sbx mcp rm sxb-orch-<sandbox8> --force` | The token is revoked first, so the endpoint refuses even if `rm` fails. |

Rules: the `sxb-orch-` prefix is **hidden** from `ListMcpServers` and never appended to
`Sandbox.mcp_servers`; the name is stable for the sandbox's life; the token lives only in the
owning daemon's local `worker_tokens` bucket (keyed by sandbox id, not exposed on the wire) and in
the host's registration. Reattach to the same orchestrator re-mints the token and re-registers.

## Endpoint

`POST http://<gatewayHost>:<hookport>/orchestrator/w/<token>/mcp` — MCP **streamable HTTP**,
served by `github.com/modelcontextprotocol/go-sdk` with `Stateless: true` and
`DisableLocalhostProtection: true`. `GET`/`DELETE` → 405 (stateless). Unknown or revoked token →
404 (`getServer` returns nil). Server implementation: `{name: "switchboard-orchestrator",
version: <daemon version>}`; `instructions` = the briefing summary (below). Every call is
attributed to the sandbox the token belongs to **before** any handler runs; the handler relays
`{sandbox_id, tool, args}` over the link and returns the orchestrator's result, or the error
below when the link is down.

Link-down error (returned as an MCP tool error, FR-150 edge cases):

```
orchestrator "<name>" is unreachable right now; retry in a moment — a failed report does not end the run
```

## Tools

All inputs and outputs are JSON objects (schemas generated from Go structs). Names are fixed so
the briefing can cite them.

### `orchestrator_list_actions`

Input: `{}`. Output:

```json
{ "orchestrator": "review-bot",
  "actions": [ { "name": "review-pr", "description": "Review a pull request and leave comments",
                 "type": "prompt", "worker_tag": "",
                 "arguments": [ { "name": "number", "description": "PR number", "required": true, "default": "" },
                                { "name": "repo",   "description": "owner/name", "required": true, "default": "" },
                                { "name": "focus",  "description": "what to look at", "required": false, "default": "" } ] } ] }
```

### `orchestrator_call_action`

Input: `{ "action": "review-pr", "arguments": { "number": "412", "repo": "acme/web" } }`.
Output on success: `{ "run_id": "run-1a2b3c4d-000017", "position": 3, "state": "queued" }`.
Refusals are tool errors with the validation message (missing required argument, undeclared
argument, oversized value, unknown action, caller not a current worker). A TRIGGERED event with
origin = the calling sandbox (FR-144). A `reason` action is allowed and runs on the reasoning
sandbox; a `prompt` action whose tag excludes the caller runs elsewhere (edge cases).

### `orchestrator_check_run`

Input: `{ "run_id": "run-1a2b3c4d-000017" }`. Output:

```json
{ "run_id": "run-1a2b3c4d-000017", "action": "review-pr", "state": "completed", "reason": "",
  "summary": "Left 3 comments on #412; no blocking issues", "closed_by_developer": false }
```

States: `queued | running | awaiting_input | awaiting_review | completed | failed | cancelled`.
Unknown id → tool error.

### `orchestrator_report_completion`

Input: `{ "summary": "Left 3 comments on #412; no blocking issues" }` (1–2000 characters).
Output: `{ "ok": true, "run_id": "run-…", "state": "completed" }`. The run is **the one recorded
on the calling sandbox**; the agent never names a run id. Refused (tool error) when no run is in
flight on the sandbox, when the run already has a summary, or when the caller is not a current
worker/reasoning sandbox (FR-155). A report that arrives while the run is *awaiting review*
closes it normally.

## Briefing (managed `CLAUDE.md` block, R12)

Written by the owning daemon between `<!-- switchboard:orchestrator:begin -->` and
`<!-- switchboard:orchestrator:end -->` at attach and launch-attach, rewritten on
`UpdateWorker{briefing}` (rename, action changes), removed on detach. User content outside the
markers is preserved (the 005 rule-block mechanics). Template (rendered with the orchestrator's
name, the sandbox's role and the current actions; FR-154):

```markdown
## Orchestrator: review-bot

This sandbox is a **worker** of the switchboard orchestrator **review-bot**.
(Reasoning sandbox variant: This sandbox is the **reasoning sandbox** of … — runs here are
`reason` actions whose summary *is* the answer.)

How an orchestrated sandbox behaves:
- Runs arrive as prompts framed `[switchboard orchestrator "review-bot" · run <id> · action "<name>"]`.
  Do the run you are given. Ask for input through the normal conversation when you must; the
  developer sees it.
- **Every run ends with a completion report**: call `orchestrator_report_completion` with a short
  summary of what you did (1–2 sentences). Do this before you finish. If the call fails, retry; a
  failed report does not end the run — the developer will be asked to review it instead.
- Report once per run. Do not report for runs that are not yours.

Tools (MCP server `sxb-orch-…`, the orchestrator):
- `orchestrator_list_actions` — the actions you may delegate, with their arguments. Use it before
  calling an action you have not seen listed.
- `orchestrator_call_action` — delegate work by calling an action with arguments. You get a run
  id and queue position; the run executes on another sandbox (a `prompt` action on a free worker;
  a `reason` action on the reasoning sandbox), not here.
- `orchestrator_check_run` — the state, outcome and summary of a run you delegated.
- `orchestrator_report_completion` — end your current run with a summary.

Workflow per action type:
- `prompt`: the orchestrator delivers the composed prompt to a free worker that satisfies the
  action's requirements; that worker reports completion.
- `reason`: the same on the reasoning sandbox; its summary is the result you read back with
  `orchestrator_check_run`.

Actions currently offered:
- `review-pr` (prompt) — Review a pull request and leave comments. Arguments: `number` (required),
  `repo` (required), `focus`.
```

## Run frame (delivery, R12)

`DeliverWorkerPrompt` types exactly:

```
[switchboard orchestrator "<name>" · run <run_id> · action "<action>"]

<composed prompt>

[When this run is finished, call orchestrator_report_completion with a short summary. If the call fails, retry; a failed report does not end the run.]
```

The recorded `Run.prompt` is the composed prompt only (SC-006); the frame is this constant.

## Composition (R8, FR-139)

- Placeholder: `{name}`, `name` = `[A-Za-z_][A-Za-z0-9_]*`; `{{` is a literal `{`.
- Every placeholder occurrence is replaced by the argument's value; an optional argument with
  neither value nor default replaces with nothing.
- Each **supplied** argument whose name appears in no placeholder is appended, in declaration
  order, as `\n\n<name>: <value>`; a multi-line value is written as `<name>:` followed by the value
  on the next lines.
- Limits: 16 KiB per argument value, 64 KiB composed prompt.
