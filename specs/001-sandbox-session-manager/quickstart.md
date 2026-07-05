# Quickstart & Validation: Sandbox Session Manager (Switchboard)

A run/validation guide proving the feature works end-to-end. Implementation details live in
`tasks.md` (Phase 2) and the code; this file shows how to build, run, and **verify each user story**.
Entity shapes: [data-model.md](./data-model.md). Wire contract:
[contracts/switchboard.proto](./contracts/switchboard.proto). Integration specifics:
[research.md](./research.md).

## Prerequisites

- Go 1.26+ (`go version`).
- Docker running on each host that will run sandboxes (`docker version`).
- The host's sandbox CLI (`sbx`) on PATH for any host that launches sandboxes.
- SSH access (key/agent auth) to any remote host you want to connect to.
- VS Code with `ms-vscode-remote.remote-containers` (+ `remote-ssh` for remote) for the open-in-VSCode story.
- `protoc` + `protoc-gen-go`/`protoc-gen-go-grpc` to regenerate `src/libs/switchboard-proto/gen`.

## Build

```bash
# From repo root (go.work ties the three modules together)
go build ./src/services/switchboardd/...      # produces `sxbd`
go build ./src/apps/switchboard-tui/...        # produces `sxb`
```

## Run the daemon (per host)

```bash
# Local host: listen on a Unix socket (no network port opened)
sxbd serve --socket "$XDG_RUNTIME_DIR/switchboard.sock" --workspace-root ~/.local/share/switchboard/workspaces

# Remote hosts run the same `serve`; the client reaches them via:
#   ssh <host> sxbd dial-stdio    (bridges stdio <-> the remote socket)
```

## Run the TUI

```bash
sxb            # connects to the local daemon socket by default; add hosts from within the UI
```

---

## Story-by-story validation

### US1 — Fan out by duplicating directories (P1) — the MVP
1. In the TUI, start a launch; select two local directories; keep mode = **duplicate** (default).
2. Launch. Watch the copy-progress indicator (FR-028), then a `running` sandbox appears.
3. **Verify**: `ls <workspace-root>/<sandbox-id>` shows verbatim copies; `git -C <original> status`
   and a checksum of the originals are **unchanged** (SC-002). Launch 3 more from the same source;
   confirm 4 independent `running` sandboxes (SC-001).
4. Stop one → it shows `stopped` and its copy still exists (FR-012a). Restart it → back to `running`
   from the retained copy (FR-012b, SC-011). Destroy it → copy deleted (FR-012c).
5. Re-run with mode = **clone** on a repo source; confirm it seeds via `sbx` clone, not a copy.

### US2 — Save & reuse configurations (P2)
1. Open the config editor; confirm **every** option from `GetOptionManifest` is settable (FR-014).
2. Save as "feature-work"; launch from it without re-entering options (SC-003, < 30s interaction).
3. Edit + re-save; a new launch reflects changes while a still-running one does not (FR-016).

### US3 — Multi-host over SSH (P2)
1. Add a remote host (SSH target) in the TUI; connect. Its sandboxes appear within 10s (SC-005).
2. Launch on each host; switch to host-grouped view — every sandbox attributed to the right host
   (SC-006). Drop the SSH connection → host shows `disconnected`, sandboxes non-actionable; restore →
   same set returns, none lost/duplicated (SC-010, FR-021).
3. Restart the remote `sxbd` while a sandbox runs → it is re-adopted, still managed (SC-012).

### US4 — Prompt agents + notifications (P3)
1. Prompt a sandbox's agent from the TUI (FR-022); confirm it reaches the agent.
2. Let a task finish → a `task_complete` notification appears in the in-TUI list **and** as an OS
   desktop notification within 5s (SC-008, FR-024/026a). Trigger an input-wait → `needs_prompting`
   notification (FR-025). Selecting it navigates to the sandbox (FR-026).
3. Disconnect during a task; reconnect → the missed notification is replayed (FR-026b).
4. "Open agent in another terminal" attaches a PTY to the running agent (FR-023).

### US5 — Organize, navigate, open in VSCode (P3)
1. Create a group spanning sandboxes on two hosts (FR-002c); assign members; navigate by keyboard
   (SC-007, < 2s switch).
2. "Open in VS Code" on a **local** sandbox → opens the in-container workspace via
   `vscode-remote://attached-container+...` (FR-027; see research.md §VSCode).
3. "Open in VS Code" on a **remote** sandbox → opens via `DOCKER_HOST=ssh://…` + the same URI.

---

## Automated test gates (mirror the constitution's verification intent)

```bash
gofmt -l ./src && golangci-lint run ./...      # format + lint (Biome substitute)
go vet ./...                                    # static checks (strict-type intent)
go test ./... -coverprofile=cover.out          # unit + integration; 90% floor enforced in CI
go tool cover -func=cover.out | tail -1         # assert >= 90% (Rule VI threshold preserved)
# TUI golden tests via teatest; E2E via the PTY/vhs harness in src/apps/switchboard-tui-e2e
```

**Done = every story above validated and all gates green.**
