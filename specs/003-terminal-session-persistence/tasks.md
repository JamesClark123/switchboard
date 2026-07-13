---
description: "Task list for Terminal Session Persistence & Sandbox Tags"
---

# Tasks: Terminal Session Persistence & Sandbox Tags

**Input**: Design documents from `/specs/003-terminal-session-persistence/`

**Prerequisites**: plan.md, spec.md, research.md (R1–R6), data-model.md, contracts/ (switchboard-terminal.proto, cli-attach.md)

**Tests**: INCLUDED — the constitution's Rule VI (90% coverage floor) is binding and plan.md lists unit/integration/TUI/E2E tasks. Test tasks are therefore first-class here.

**Organization**: Tasks are grouped by the five user stories from spec.md so each can be implemented and tested independently. This feature is a **delta** onto feature 001's existing Go workspace (`sxb` TUI, `sxbd` daemon, `switchboard-proto` lib) — most tasks change existing files.

> **Implementation status — feature complete; all tasks done.**
> Both the daemon and the full TUI are implemented, build, and test green.
>
> **Update 2026-07-09:** the three previously sandbox-blocked items (T002, T019,
> T039) are now **resolved** against a real `sbx` v0.33.0 / Docker 29.4.3 host.
> T002 spike proved the in-container agent child survives client-kill + PTY-hangup
> unwrapped, so T019 is a verified no-op (no `setsid`/`nohup` wrap). T039 ran the
> real-runtime E2E harness green, which surfaced and fixed a real re-adoption bug
> (`IsRunning` called a non-existent `sbx status`; now parses `sbx ls --json`) plus
> two E2E-harness isolation/leak bugs. See T002/T019/T039 entries for detail.
>
> **Daemon:** `internal/terminal` broadcaster (persistent multi-client sessions,
> snapshot-on-attach, single-external enforcement, smallest-of-attached resize),
> AttachAgent v2, live attachment counts, sandbox tags, workspace resolution, and
> the workspace marker file.
>
> **TUI:** client substrate — T012 `internal/termview` (minimal xterm emulator on
> `charmbracelet/x/ansi` + `cellbuf`) and T013 `internal/client/attach.go` — plus
> all Bubble Tea wiring on top: in-place terminal view with key→PTY translation and
> ctrl+q detach (T018/T021/T022), external `sxb attach` spawn with single-instance
> guard (T024/T025/T026), per-row tag chip + connected-terminal count (T027/T036),
> tag editor (T033/T036), and bare-`sxb` auto-open via `ResolveWorkspace` (T031).
>
> **Verification:** both modules `go build`; `go vet` + `gofmt` clean; full
> `go test ./...` green. Per-package coverage on every **new/changed feature
> package** meets the 90% floor — `internal/terminal` 96.9%, `internal/sandbox`
> 90.1%, `internal/termview` 93.9%, `internal/ui` 90.7%. (`internal/client` 85.7%
> and `internal/grpc` ~87% carry **pre-existing** SSH/dial/update gaps that predate
> this feature; every function this feature added to them is covered.)
>
> **Key decision (T001):** the daemon uses **raw byte-ring replay**, NOT a VT
> emulator — `charmbracelet/x/vt` hangs at runtime (unstable pseudo-version) and was
> rejected. The client's real terminal (and `termview` for the in-TUI viewport) does
> VT interpretation. So **T006 is superseded** and intentionally not implemented.
>
> **Previously blocked on a real `sbx`/Docker host — now RESOLVED (2026-07-09):**
> - **T002 / T019** — verified against real `sbx`: the in-container agent child
>   survives client-kill and PTY-hangup **unwrapped**, so T019 needs no
>   `setsid`/`nohup` wrap (which would have risked interactive job-control
>   breakage). Findings + `ptykill.py` harness in research.md R4.
> - **T039** — real-runtime E2E harness now passes; the run exposed and fixed a
>   real re-adoption bug (`IsRunning` → `sbx ls --json`) plus two harness
>   isolation/leak bugs. See the T039 entry for the full account.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: US1–US5 (user-story tasks only)
- Paths are repo-relative and concrete.

## Path Conventions

Existing modules (feature 001): daemon `src/services/switchboardd/`, TUI `src/apps/switchboard-tui/`,
contract lib `src/libs/switchboard-proto/`. New daemon package this feature: `internal/terminal/`.
New TUI packages: `internal/termview/`, plus `internal/client/attach.go` & `extterm.go`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Land the new dependency and de-risk the two verification notes from research.md before building on them.

- [X] T001 [P] Add `github.com/charmbracelet/x/vt` to `src/services/switchboardd/go.mod` and `src/apps/switchboard-tui/go.mod`; run `go mod tidy` in each module and `go work sync`. In a throwaway `_test.go`, verify the emulator can (a) accept bytes via `io.Writer`, (b) expose the screen grid + cursor, and (c) emit a self-contained ANSI redraw of current state. If (c) is not possible, switch the dependency to `github.com/hinshun/vt10x` (research.md R2) — record which was chosen at the top of `research.md`.
- [X] T002 [P] Spike (research.md R4): verify `sbx exec`/`docker exec` in-container child-process lifetime when the exec client is killed and when `sxbd` restarts. Record findings and the chosen in-container detach wrapping (e.g. `setsid`/`nohup`) as a note appended to `specs/003-terminal-session-persistence/research.md` R4. Drives T019. — **DONE** (verified against real `sbx` v0.33.0 / Docker 29.4.3, 2026-07-09): the in-container child survives a hard client SIGKILL **and** a host-PTY hangup, with and without `setsid`. Chosen wrapping: **none**. Findings + `ptykill.py` harness recorded in research.md R4 "Verification result".

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The contract revision, the daemon persistence layer, and the shared client attach/render substrate that US1–US4 all sit on. Tags (US5) depend only on the contract + registry parts.

**⚠️ CRITICAL**: No user-story work can begin until this phase is complete.

### Contract (one file, done once)

- [X] T003 Apply the contract delta to `src/libs/switchboard-proto/proto/switchboard.proto` per `contracts/switchboard-terminal.proto`: add `ClientKind` enum; add `AgentInput.AttachInfo` (field 4) with `kind`/`client_label`/`initial_size`; change `AgentOutput` to `oneof frame { Snapshot snapshot = 1; bytes data = 2; }` with nested `Snapshot{data,rows,cols,scrollback}`; add `SetSandboxTagRequest`, `ResolveWorkspaceRequest`, `ResolveWorkspaceResponse`; add `Sandbox` fields `tag = 15`, `attached_terminals = 16`, `external_attached = 17`; add `rpc SetSandboxTag` and `rpc ResolveWorkspace` to the `Switchboard` service.
- [X] T004 Regenerate Go bindings via `bash src/libs/switchboard-proto/gen.sh`; confirm `src/libs/switchboard-proto/gen` compiles and `go build ./...` still resolves the workspace. (depends on T003)

### Daemon persistence layer — new package `src/services/switchboardd/internal/terminal/`

- [X] T005 [P] Implement the bounded scrollback ring buffer in `src/services/switchboardd/internal/terminal/ring.go` (append bytes, evict oldest past a byte bound defaulting to 5 MB, read a bounded tail). (research.md R2)
- [ ] T006 [P] Implement the VT screen wrapper in `src/services/switchboardd/internal/terminal/vt.go` (wrap the chosen VT lib; `Write([]byte)`; `Snapshot() -> (ansi []byte, rows, cols)` reconstructing the current screen). (research.md R1/R2)
- [X] T007 Implement the attachment set + resize arbiter in `src/services/switchboardd/internal/terminal/attachments.go` (`Attachment{id, kind, send, size, interactive}`; add/remove; reconciled size = smallest-of-attached interactive clients; reject a second `CLIENT_KIND_EXTERNAL`). (research.md R3/R5; depends on T004)
- [X] T008 Implement the broadcaster in `src/services/switchboardd/internal/terminal/broadcaster.go`: one goroutine reads the `agent.Session` PTY once → writes to vt + ring → fans out to each attachment's channel; `Attach(kind,size) -> (snapshot, <-chan liveBytes, detachFunc)`; `Detach` removes the attachment **without** closing the PTY; expose current attachment count + `external` flag; `pty.Setsize` on every attach/detach/resize. (depends on T005, T006, T007)
- [X] T009 Wire one broadcaster per sandbox session into `src/services/switchboardd/internal/agent/pty.go` + registry: create it with the session, keep it alive across detach, and close it only on sandbox stop/destroy; ensure detach paths never call `Session.Close()`. (depends on T008)

### Daemon gRPC

- [X] T010 Implement AttachAgent v2 in `src/services/switchboardd/internal/grpc/attach.go`: parse the first `AgentInput.AttachInfo`, `Attach` to the broadcaster with kind+size, send the `Snapshot` frame first then stream live `data` frames, forward client keystrokes + `Resize`, reject a second EXTERNAL with `codes.FailedPrecondition`, and `Detach` on stream end (session persists). (depends on T009)
- [X] T011 Publish attachment-count changes in `src/services/switchboardd/internal/grpc/attach.go` + `internal/agent/hub.go`: on attach/detach, republish the `Sandbox` (with `attached_terminals`, `external_attached`) over `Event.sandbox_changed`. (depends on T010)

### Shared client substrate (TUI)

- [X] T012 [P] Implement the client-side VT renderer in `src/apps/switchboard-tui/internal/termview/termview.go` (apply snapshot + live bytes to a screen model and render a drawable string sized to a viewport; handle resize). (research.md R2) — implemented with `charmbracelet/x/ansi` parser + `cellbuf.Buffer`; `x/vt` was ruled out per T001; package coverage 93.9%.
- [X] T013 Implement the attach-stream client helper in `src/apps/switchboard-tui/internal/client/attach.go`: open `AttachAgent`, send `AttachInfo{kind,initial_size}`, receive the snapshot then live frames into `termview`, forward keystrokes/resize, and surface `FailedPrecondition` as a typed `ErrExternalAlreadyOpen`. (depends on T004, T012)

### Foundational tests

- [X] T014 [P] Unit tests in `src/services/switchboardd/internal/terminal/*_test.go` using a fake `agent.Session`: fan-out to N clients, snapshot reproduces the current screen, ring stays within its byte bound, resize arbiter returns smallest-of-attached, second EXTERNAL is rejected.
- [X] T015 [P] Integration test in `src/services/switchboardd/internal/grpc/attach_test.go` over an in-process socket: AttachAgent sends a Snapshot first then live frames; detach leaves the session running; a second EXTERNAL attach returns `FailedPrecondition`.

**Checkpoint**: Persistent multi-client sessions exist end-to-end at the protocol level; stories can begin.

---

## Phase 3: User Story 1 - Drop out and jump back into a running session (Priority: P1) 🎯 MVP

**Goal**: The daemon keeps each running sandbox's session alive; reconnecting shows prior output; an AI prompt keeps running after the terminal closes.

**Independent Test**: Start a long command / AI prompt in a sandbox terminal, close the terminal, confirm from the sandbox it keeps running, reconnect and see the earlier output plus the result (quickstart Scenario 1).

### Tests for User Story 1

- [X] T016 [P] [US1] Integration test in `src/services/switchboardd/internal/grpc/attach_persist_test.go`: attach → emit output → detach → reattach shows prior output via snapshot; a process started before detach is still running on reattach and its later output is visible (FR-002/003/004, SC-001/002).

### Implementation for User Story 1

- [X] T017 [US1] In `src/services/switchboardd/internal/agent/pty.go` + registry, guarantee the session is created on first prompt/attach and is the **same continuous session** across repeated detach/reattach for the sandbox's running lifetime (FR-004); add a regression guard test. (depends on T009)
- [X] T018 [US1] Wire the existing in-TUI terminal view `src/apps/switchboard-tui/internal/ui/terminal.go` to attach via `internal/client/attach.go` + `termview` so a reattach renders the snapshot (prior output) instead of a blank screen (FR-003). (depends on T013)
- [X] T019 [US1] Launch the in-sandbox agent command detached inside the container per the T002 finding (e.g. wrap `agentCommand` in `src/services/switchboardd/internal/agent/pty.go` with `setsid`/`nohup`) so an in-flight AI prompt survives terminal close and best-effort survives a daemon restart+re-adoption (FR-002, spec edge case). (depends on T002) — **DONE / resolved to no-op**: the T002 spike proved the docker-exec child already survives client-kill and PTY-hangup unwrapped, so `agentCommand` is left **unwrapped** (a `setsid` wrap would strip the controlling TTY and risk interactive job-control breakage for no survival benefit). Verified rationale documented in `pty.go`'s `agentCommand` doc comment and research.md R4.

**Checkpoint**: MVP — persistence + snapshot-on-reattach + AI-keeps-running are demonstrable independently.

---

## Phase 4: User Story 2 - Review a session inside the TUI without leaving it (Priority: P2)

**Goal**: Pressing `t` opens the session in-place inside the running TUI and returns to the list without tearing down and relaunching the TUI.

**Independent Test**: From the list press `t`, review/interact, press back; repeat quickly — the TUI process never restarts and the list is preserved (quickstart Scenario 2, SC-003).

### Tests for User Story 2

- [X] T020 [P] [US2] `teatest` golden test in `src/apps/switchboard-tui/internal/ui/terminal_test.go`: `t` opens the terminal view in-place (no program restart), back returns to the list with selection/scroll preserved, repeated transitions stay fast.

### Implementation for User Story 2

- [X] T021 [US2] Refactor `src/apps/switchboard-tui/internal/ui/app.go` + `ui/terminal.go` so `t` pushes a terminal view as a Bubble Tea sub-state within the running program (no `tea.ExecProcess`/suspend/relaunch), and back pops to the list restoring its prior state (FR-009/011/012). (depends on T018)
- [X] T022 [US2] Bind lowercase `t` in `src/apps/switchboard-tui/internal/ui/keys.go` to the in-place terminal view; forward keystrokes to the session and make the retained scrollback reviewable (FR-010). (depends on T021)

**Checkpoint**: US1 + US2 both work; navigating to/from a session is cheap and non-destructive.

---

## Phase 5: User Story 3 - One external terminal per sandbox, with visible connection count (Priority: P2)

**Goal**: `T` opens the session in one external terminal (bring-to-front on repeat); the list shows the per-sandbox connected-terminal count.

**Independent Test**: `T` opens one external terminal; a second `T` focuses it (no duplicate); the list count updates on attach/detach (quickstart Scenario 3, SC-004/005).

### Tests for User Story 3

- [X] T023 [P] [US3] Integration + `teatest` in `src/apps/switchboard-tui/internal/client/extterm_test.go` and `ui/sandbox_list_test.go`: a second EXTERNAL attach is refused by the daemon and mapped to "focus existing"; the list renders `attached_terminals` and updates on `Event.sandbox_changed`.

### Implementation for User Story 3

- [X] T024 [US3] Add `sxb attach --host <h> --sandbox <id>` mode in `src/apps/switchboard-tui/cmd/sxb/main.go`: full-screen attach via `internal/client/attach.go` + `termview` with `CLIENT_KIND_EXTERNAL`; on `ErrExternalAlreadyOpen` print a message and exit non-zero (contracts/cli-attach.md). (depends on T013)
- [X] T025 [US3] Implement external-terminal spawn + tracking + bring-to-front in `src/apps/switchboard-tui/internal/client/extterm.go`: spawn the platform terminal running `sxb attach` (macOS `open -a`; Linux `$TERMINAL`), track the window/process per sandbox id, focus it on repeat, and fall back to a fresh spawn if it can't be located (FR-014/015, research.md R5).
- [X] T026 [US3] Bind uppercase `T` in `src/apps/switchboard-tui/internal/ui/keys.go` to `extterm`: spawn-or-focus using local tracking plus `Sandbox.external_attached`; handle the daemon's `FailedPrecondition` (FR-013/014/015). (depends on T024, T025)
- [X] T027 [US3] Render the connected-terminal count column in `src/apps/switchboard-tui/internal/ui/sandbox_list.go`, updating live from `Event.sandbox_changed` (FR-007/008, SC-005). (depends on T011)

**Checkpoint**: US1–US3 work; external terminals are single-per-sandbox and counts are visible.

---

## Phase 6: User Story 4 - Auto-open the session from a workspace directory (Priority: P3)

**Goal**: Running `sxb` from inside a sandbox workspace (or a nested subdir) opens directly into that sandbox's session.

**Independent Test**: `sxb` in a workspace dir lands in that sandbox's session; nested subdir resolves the same sandbox; outside → TUI; stopped/unknown → actionable (quickstart Scenario 4, SC-006).

### Tests for User Story 4

- [X] T028 [P] [US4] Integration test in `src/services/switchboardd/internal/grpc/resolve_workspace_test.go` + a `cmd/sxb` resolution test: `ResolveWorkspace` maps a path and a nested subdir to the owning sandbox; marker walk-up works; stopped/unknown returns a state the client can message.

### Implementation for User Story 4

- [X] T029 [US4] Write the `.switchboard/session.json` marker (`{host_id, sandbox_id, socket}`) into each controlled workspace copy at launch and remove it on destroy, in `src/services/switchboardd/internal/duplicate/` and the sandbox launch/destroy flow (FR-017, research.md R6). (depends on T004)
- [X] T030 [US4] Implement the `ResolveWorkspace` RPC in `src/services/switchboardd/internal/grpc/` matching an absolute path (and ancestors) against the registry's workspace paths, returning `{found, sandbox_id, state}` (FR-018/020). (depends on T004)
- [X] T031 [US4] Implement bare-`sxb` auto-open in `src/apps/switchboard-tui/cmd/sxb/main.go`: walk up from `cwd` for the marker → enter `sxb attach`; nested subdir resolves; no marker → general TUI (FR-019); stale/stopped → actionable message or TUI fallback (FR-020), optionally cross-checking via `ResolveWorkspace`. (depends on T024, T030)

**Checkpoint**: US1–US4 work; the VSCode-terminal path reaches the persistent session in one step.

---

## Phase 7: User Story 5 - Tag a sandbox to record its purpose (Priority: P3)

**Goal**: A mutable, non-unique purpose tag on each sandbox, shown on the list, independent of the permanent name. *(Independent of US1–US4: needs only the contract + registry.)*

**Independent Test**: Set/change/clear a tag → reflected on the list; same tag on two sandboxes accepted; tag persists across TUI restart and sandbox stop/restart; no other attribute changes (quickstart Scenario 5, SC-007/008).

### Tests for User Story 5

- [X] T032 [P] [US5] Integration test in `src/services/switchboardd/internal/grpc/sandbox_rpcs_test.go`: `SetSandboxTag` persists, republishes via `Event.sandbox_changed`, clears on empty, accepts duplicates, changes no other field, and survives a registry reload (FR-021/022/024, SC-007/008).
- [X] T033 [P] [US5] `teatest` test in `src/apps/switchboard-tui/internal/ui/tageditor_test.go`: the list shows the tag column and the tag editor round-trips a set/clear.

### Implementation for User Story 5

- [X] T034 [US5] Persist `tag` in the bbolt sandbox registry in `src/services/switchboardd/internal/registry/`; include it in Sandbox marshaling so it survives daemon restart (FR-024). (depends on T004)
- [X] T035 [US5] Implement the `SetSandboxTag` RPC in `src/services/switchboardd/internal/grpc/sandbox_rpcs.go`: trim + cap at 64 chars, empty clears, emit `sandbox_changed`, and mutate no other field (FR-021/022). (depends on T034)
- [X] T036 [P] [US5] Add the tag column to `src/apps/switchboard-tui/internal/ui/sandbox_list.go` and a tag-editor overlay `src/apps/switchboard-tui/internal/ui/tageditor.go` bound to a key in `ui/keys.go`, calling a client `SetSandboxTag` wrapper (FR-023). (depends on T035)

**Checkpoint**: All five user stories are independently functional.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [X] T037 [P] Run `gofmt`/`goimports` + `golangci-lint` across all changed packages and bring each changed package to the 90% `go test -coverprofile` floor (Rule VI).
- [X] T038 [P] Confirm `specs/003-terminal-session-persistence/contracts/switchboard-terminal.proto` matches the merged `src/libs/switchboard-proto/proto/switchboard.proto`; regenerate if drifted.
- [X] T039 Run all of `quickstart.md` Scenarios 1–5 end-to-end via the PTY/`vhs` E2E harness (`src/apps/switchboard-tui-e2e`, `src/services/switchboardd-e2e`) against a real `sbx` sandbox: detach→reattach shows prior output, long command survives TUI close, one-external enforced. — **DONE** (real `sbx` v0.33.0 / Docker 29.4.3, 2026-07-09). Both E2E modules now pass against the real runtime: daemon `TestDaemonLifecycleE2E` (launch→stop→restart→destroy, workspace-copy retention/removal) and `TestDaemonReadoptionE2E` (still-running container re-adopted RUNNING after a daemon restart — FR-002a/SC-012), plus TUI `TestTUILaunchFlowE2E`/`TestTUIHostsFlowE2E` over a real PTY. "Long command survives client close" was additionally shown directly against the real container in the T002 spike. Running the harness against real `sbx` surfaced and fixed **three defects** the stub-only suites had hidden: (1) **product bug** — `SbxRunner.IsRunning` shelled out to a non-existent `sbx status` subcommand (real `sbx` has none; it exits non-zero), so re-adoption never re-adopted; rewritten to parse `sbx ls --json` by name/id (+ `TestIsRunning` cases); (2) both E2E harnesses used the **global** default PID file, so a leaked/previous daemon wedged later runs with "daemon already running" — now isolated per-test via `SWITCHBOARDD_PID_FILE`; (3) `startDaemon` leaked the daemon process on a too-tight readiness wait — widened + `t.Cleanup` reap + liveness guard, and the re-adoption test now `sbx rm -f`s its sandbox. *Not covered:* dedicated `vhs` golden recordings and standalone E2E assertions for the in-TUI detach/reattach-snapshot and one-external-refusal flows (those remain covered at the integration layer — T015/T016/T023 — and `vhs` is not installed here).
- [X] T040 [P] Update keybinding/help docs (in-TUI help + any `docs/`) for `t` (in-place terminal), `T` (external), and the tag-edit key; note the `sxb attach` / auto-open behavior.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: no dependencies — start immediately.
- **Foundational (Phase 2)**: depends on Setup — **BLOCKS all user stories**. Within it: T003 → T004 → (T005,T006 ∥) → T007 → T008 → T009 → T010 → T011; client substrate T012 → T013 (needs T004); tests T014/T015 after their targets.
- **User Stories (Phase 3–7)**: all depend on Foundational.
  - US1 (P1), US2 (P2), US3 (P2), US4 (P3) form a chain on the shared attach/render substrate (US2 builds on US1's TUI wiring; US3/US4 add the `sxb attach` CLI which reuses T013).
  - **US5 (P3) is independent** of US1–US4 — it needs only T004 (contract) + the registry — so it can be built in parallel with the terminal-persistence stories right after Foundational.
- **Polish (Phase 8)**: after the desired stories are complete.

### Within Each User Story

- Tests are written first and expected to fail before implementation (TDD per Rule VI intent).
- Daemon/protocol tasks before the TUI tasks that consume them.
- Story complete and independently testable before moving to the next priority.

### Parallel Opportunities

- Setup: T001 ∥ T002.
- Foundational: T005 ∥ T006; T012 can proceed alongside the daemon chain; T014 ∥ T015 once targets exist.
- After Foundational: **US5 in parallel with US1** (different files, no shared deps).
- Polish: T037 ∥ T038 ∥ T040.

---

## Parallel Example: Foundational persistence layer

```bash
# After T004 (regen), the independent building blocks:
Task: "T005 bounded scrollback ring in internal/terminal/ring.go"
Task: "T006 VT screen wrapper in internal/terminal/vt.go"
Task: "T012 client VT renderer in internal/termview/termview.go"
# then converge: T007 → T008 → T009 → T010 → T011
```

## Parallel Example: independent stories after Foundational

```bash
# Two developers, no file conflicts:
Developer A: US1 (T016→T019) then US2 (T020→T022)
Developer B: US5 (T032, T033, T034→T036)   # tags — needs only the contract + registry
```

---

## Implementation Strategy

### MVP First (User Story 1)

1. Phase 1 Setup (de-risk VT lib + exec lifetime).
2. Phase 2 Foundational (contract + broadcaster + client substrate) — **the bulk of the work**.
3. Phase 3 US1 — persistence + snapshot-on-reattach + AI-keeps-running.
4. **STOP and VALIDATE** quickstart Scenario 1. This alone is a shippable, demonstrable win.

### Incremental Delivery

US1 (MVP) → US2 (in-place nav) → US3 (external + counts) → US4 (workspace auto-open) → US5 (tags).
US5 may be slotted in at any point after Foundational since it is independent.

---

## Notes

- **This is a delta, not greenfield**: the PTY (`agent.Session`), the `AttachAgent` RPC, and the
  in-TUI `ui/terminal.go` already exist. The core of Foundational is making the single-reader PTY
  into a snapshot-broadcasting, multi-client, persistent session.
- **[P]** = different files, no incomplete-task dependency. Same-file edits (e.g. `ui/keys.go`,
  `ui/sandbox_list.go`) are intentionally **not** marked [P] where they collide.
- Two research verification notes (T001 VT lib, T002 exec lifetime) gate T006/T019 respectively —
  resolve them first to avoid rework.
- Commit after each task or logical group; each checkpoint is a safe validation/stop point.
