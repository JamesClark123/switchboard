---

description: "Task list for Port Forwarding implementation"
---

# Tasks: Port Forwarding

**Input**: Design documents from `/specs/006-port-forwarding/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/), [quickstart.md](./quickstart.md)

**Tests**: Test tasks ARE included. Rule VI (Multi-Level Testing Discipline) is NON-NEGOTIABLE and the repo enforces ≥90% per-module coverage via `make test`/`make cover`; `quickstart.md` enumerates the required assertions. Go unit tests are colocated as `*_test.go` (Rule IV).

**Organization**: Tasks are grouped by user story so each can be implemented, tested, and delivered independently.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US5)
- Exact file paths are included in every task

## Path Conventions

Go monorepo (`go.work`), paths relative to repo root:

- Daemon: `src/services/switchboardd/`
- TUI client: `src/apps/switchboard-tui/`
- Contract: `src/libs/switchboard-proto/`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Land the additive contract revision and create both new package skeletons.

- [X] T001 [P] Add port-forwarding enums and messages (`ServiceLocation`, `ServiceState`, `ServiceFailureReason`, `KitService`, `ServiceInstance`, `SandboxService`, `ListSandboxServicesRequest/Response`, `StartSandboxServiceRequest/Response`, `StopSandboxServiceRequest/Response`, `PortForwardFrame` with its `Open`/`Opened`/`Closed` nested messages) to `src/libs/switchboard-proto/proto/switchboard.proto` per `specs/006-port-forwarding/contracts/switchboard-port-forwarding.proto`
- [X] T002 Add additive fields to existing messages in `src/libs/switchboard-proto/proto/switchboard.proto`: `KitSpec.services = 4`, `Sandbox.services = 20`, `Event.service_instance = 5`, `NOTIFICATION_KIND_SERVICE_FAILED = 5`, plus the four new `rpc` entries on `service Switchboard` (`ListSandboxServices`, `StartSandboxService`, `StopSandboxService`, and the bidirectional `ForwardPort`) (depends on T001, same file)
- [X] T003 Regenerate Go bindings with `make proto` and commit `src/libs/switchboard-proto/gen/switchboard.pb.go` + `switchboard_grpc.pb.go` (depends on T002)
- [X] T004 [P] Create the daemon package skeleton at `src/services/switchboardd/internal/portforward/doc.go` with a package doc citing research decisions R1–R9 and the two invariants that must never regress: **the start endpoint accepts a service NAME only, resolved against the sandbox's persisted `services` allowlist**, and **`RUNNING` is set only after a successful dial of the host endpoint**
- [X] T005 [P] Create the client package skeleton at `src/apps/switchboard-tui/internal/forward/doc.go` documenting that the client is the sole allocator of ports on the developer's machine (research R1) and that listeners bind `127.0.0.1:0` only

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Resolution, persistence, instance storage, the bounded output buffer, the `sbx` surface, and event plumbing that every story depends on.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T006 [P] Implement resolution in `src/services/switchboardd/internal/portforward/resolve.go`: merge attached client-authored kits' `services` lists in attach order with **later-kit-wins** on name collision (FR-044); reject entries with `SERVICE_LOCATION_UNSPECIFIED`, an empty name or command, or a `listen_port` outside 1–65535
- [X] T007 [P] Unit tests in `src/services/switchboardd/internal/portforward/resolve_test.go`: later-kit override of a same-named service, attach-order preservation, rejection of unspecified location and out-of-range ports
- [X] T008 [P] Implement the in-memory, session-scoped instance store in `src/services/switchboardd/internal/portforward/instances.go`: `svc-<seq>` ids, mutex-guarded map, `Create/Get/Update/ListBySandbox`, and the **at-most-one non-terminal instance per (sandbox, service name)** invariant that makes start idempotent race-free (data-model.md)
- [X] T009 [P] Unit tests in `src/services/switchboardd/internal/portforward/instances_test.go`: id sequencing, list filtering by sandbox, concurrent `Create` for the same (sandbox, name) yields exactly one instance
- [X] T010 [P] Implement the bounded **tail-retaining** ring buffer in `src/services/switchboardd/internal/portforward/output.go`: 1 MiB cap keeping the *last* bytes (deliberately the opposite retention from `escapehatch.boundedBuffer`, research R9), safe for concurrent stdout+stderr writers, reporting truncation
- [X] T011 [P] Unit tests in `src/services/switchboardd/internal/portforward/output_test.go`: under-cap passthrough, over-cap keeps the tail and sets truncated, concurrent writers
- [X] T012 Add `PublishPort`, `UnpublishPort`, and `Exec` to the `Runner` interface and `SbxRunner` in `src/services/switchboardd/internal/sandbox/runner.go` per `specs/006-port-forwarding/contracts/sbx-ports-cli.md`, each carrying the ⚠-unverified-argv NOTE comment the existing `AddKit`/`AllowNetwork` methods use (depends on T003)
- [X] T013 [P] Argv-asserting tests in `src/services/switchboardd/internal/sandbox/runner_ports_test.go` using stub scripts: `sbx ports <ref> --publish 127.0.0.1:P:L/tcp`, the exactly-mirrored `--unpublish`, and `sbx exec <ref> -- <argv...>`
- [X] T014 Persist the resolved set in `src/services/switchboardd/internal/sandbox/manager.go`: populate `Sandbox.Services` during `Launch`, `AddKit`, and `Refresh`, and replay it on container recreate (depends on T003, T006)
- [X] T015 [P] Unit test in `src/services/switchboardd/internal/sandbox/manager_services_test.go`: `Sandbox.services` round-trips through the bbolt registry and pre-feature records decode with an empty set (no migration)
- [X] T016 Implement the supervisor scaffolding in `src/services/switchboardd/internal/portforward/supervisor.go`: dependency struct (sandbox lookup, `Runner`, instance store, hub emitter, clock), the state-machine transition helpers, and the single `publish` choke-point that emits exactly one `Event.service_instance` per transition and a `NOTIFICATION_KIND_SERVICE_FAILED` notification **only** on entry to `FAILED` (FR-052) (depends on T008)
- [X] T017 [P] Unit tests in `src/services/switchboardd/internal/portforward/supervisor_test.go`: every transition emits exactly one event; `FAILED` emits one notification; successful start and developer-initiated stop emit none

**Checkpoint**: Resolution, persistence, instance storage, output bounding, the `sbx` surface, and event emission are ready — user stories can begin.

---

## Phase 3: User Story 1 - Declare services on a kit (Priority: P1)

**Goal**: A developer can author, validate, save, and reopen a kit's services section.

**Independent Test**: Open the kit editor, add one service of each execution location with every field, save, reopen — all services and all fields persist and the kit still validates.

### Implementation for User Story 1

- [X] T018 [P] [US1] Add `Services []KitService` (with `yaml:"-"`) and the `KitService` struct to `src/apps/switchboard-tui/internal/store/kit.go`, plus `servicesFile`/`saveServices`/`loadServices` for the `kits/<id>/services.yaml` sidecar and the `Kit.ToSpec` projection into `pb.KitSpec.Services` — mirroring the existing `escape-hatch.yaml` sidecar exactly (research R8)
- [X] T019 [US1] Implement authoring validation in `src/apps/switchboard-tui/internal/store/kit.go`: non-empty `kebab-case` name unique within the kit, non-empty command, `listen_port` in 1–65535, explicitly-set location, and `working_dir` containment via the same `filepath.Rel` check `escapehatch.resolveWorkdir` uses — each rejection naming the offending field (FR-043)
- [X] T020 [US1] Add the itemized **Services** section to `src/apps/switchboard-tui/internal/ui/kit_editor.go` (a `secServices` entry plus a per-item `huh` form for name, command, listen port, location, is-website, working dir, readiness timeout), following the existing escape-hatch section's add/edit/remove shape
- [X] T021 [US1] Re-validate services daemon-side at attach in `src/services/switchboardd/internal/portforward/resolve.go` so a hand-edited sidecar cannot bypass the client's checks (defence in depth, mirroring the escape-hatch workdir precedent)

### Tests for User Story 1

- [X] T022 [P] [US1] Unit tests in `src/apps/switchboard-tui/internal/store/kit_services_test.go`: sidecar round-trips every field, a missing sidecar yields no services, an emptied list removes the file, and `ToSpec` projects into `pb.KitSpec.Services`
- [X] T023 [P] [US1] Validation tests in `src/apps/switchboard-tui/internal/store/kit_services_validate_test.go`: missing name/command/port, duplicate name within one kit, out-of-range port, unset location, and a `working_dir` that escapes the workspace — each rejected with a message naming the field, and the stored kit unchanged
- [X] T024 [P] [US1] Kit-editor tests in `src/apps/switchboard-tui/internal/ui/kit_editor_services_test.go`: add/edit/remove persist through save and reopen, each execution location keeps its own listen port, and an abandoned edit (`esc`) mutates nothing on disk

**Checkpoint**: Kits can declare services end-to-end in the client — a reviewable, shareable declaration of how to bring a project's stack up.

---

## Phase 4: User Story 2 - Start a sandbox service and open it locally (Priority: P1) 🎯 MVP

**Goal**: Start an in-sandbox service from the client and reach it in a browser on a free local port, with no port plumbing.

**Independent Test**: Attach a kit declaring one in-sandbox service to a running sandbox, start it from the client, and confirm the command runs inside the sandbox and that requests to the displayed local address reach it — with the port chosen without user input.

### Implementation for User Story 2

- [X] T025 [US2] Implement daemon-host port allocation and publishing in `src/services/switchboardd/internal/portforward/ports.go`: bind `127.0.0.1:0` to obtain a free port, close, then `Runner.PublishPort`, retrying the whole allocation up to 3× on a bound-port rejection (research R2); store the published triple on the instance so `UnpublishPort` mirrors it exactly
- [X] T026 [US2] Implement the in-sandbox launcher in `src/services/switchboardd/internal/portforward/launch_sandbox.go`: run the command through `Runner.Exec` wrapped as `setsid` announcing `swb-pgid:$$` on stderr, parse and record that PGID on the instance, and stream the remaining stdout+stderr into the bounded buffer with the marker line excluded (research R3)
- [X] T027 [US2] Implement readiness probing in `src/services/switchboardd/internal/portforward/probe.go`: dial the host endpoint with 250 ms backoff capped at 2 s until the readiness window (per-service override, else 60 s) elapses; success is the **only** path to `RUNNING` (FR-047, research R5)
- [X] T028 [US2] Implement the supervisor `Start` path in `src/services/switchboardd/internal/portforward/supervisor.go`: refuse an in-sandbox start with `SANDBOX_NOT_RUNNING` and **no port allocated** when the sandbox is not running (FR-046), drive `STOPPED → STARTING → RUNNING`, and map early terminations to `LAUNCH_FAILED` / `EXITED_EARLY` / `NOT_LISTENING` (depends on T025, T026, T027)
- [X] T029 [US2] Implement the `ForwardPort` stream handler in `src/services/switchboardd/internal/portforward/relay.go`: require `Open` as the first frame (else `INVALID_ARGUMENT`), reject an instance that is not `RUNNING` (`FAILED_PRECONDITION`), dial `127.0.0.1:<host_endpoint_port>`, reply `Opened`, then pipe 32 KiB chunks both ways until either side closes
- [X] T030 [US2] Implement `ListSandboxServices`, `StartSandboxService`, and `StopSandboxService` in `src/services/switchboardd/internal/grpc/service_rpcs.go`, joining `Sandbox.services` with current instances and returning `NOT_FOUND` for a name absent from the sandbox's persisted set
- [X] T031 [US2] Wire the `ForwardPort` RPC in `src/services/switchboardd/internal/grpc/forward_rpc.go` and register the supervisor on the server in `src/services/switchboardd/internal/grpc/server.go`
- [X] T032 [US2] Emit `Event.service_instance` and the `SERVICE_FAILED` notification through the hub in `src/services/switchboardd/internal/grpc/subscribe.go`, and construct the supervisor (Runner, emitter, clock) in `src/services/switchboardd/cmd/sxbd/main.go`
- [X] T033 [P] [US2] Add `ListSandboxServices`, `StartSandboxService`, `StopSandboxService`, and the `ForwardPort` stream opener to `src/apps/switchboard-tui/internal/client/sandbox.go`
- [X] T034 [US2] Implement the client forward manager in `src/apps/switchboard-tui/internal/forward/manager.go`: open a `127.0.0.1:0` listener per running instance, report the bound port back to the daemon for display, and close the listener (and every relayed connection) on any terminal transition or client exit
- [X] T035 [US2] Implement the client relay in `src/apps/switchboard-tui/internal/forward/relay.go`: an accept loop that opens one `ForwardPort` stream per accepted TCP connection, sends `Open{instance_id, local_port}` first, and pipes bytes both ways (depends on T033, T034)
- [X] T036 [US2] Add the per-sandbox services screen in `src/apps/switchboard-tui/internal/ui/services.go`: list every declared service with its state and local address, start/stop the selected one, offer the browser-open action for `is_website` services and a copyable address otherwise (FR-045, FR-050)
- [X] T037 [US2] Bind `p` to the services screen in `src/apps/switchboard-tui/internal/ui/keys.go` and route `screenServices` (plus the forward manager's lifetime) in `src/apps/switchboard-tui/internal/ui/app.go`

### Tests for User Story 2

- [X] T038 [P] [US2] Unit tests in `src/services/switchboardd/internal/portforward/ports_test.go`: allocation returns a genuinely free port, the publish triple is mirrored on unpublish, and a bound-port rejection retries then fails cleanly
- [X] T039 [P] [US2] Unit tests in `src/services/switchboardd/internal/portforward/launch_sandbox_test.go` with a fake `Runner`: the `sbx exec` argv shape, PGID parsed from the marker line, and the marker excluded from captured output
- [X] T040 [P] [US2] Unit tests in `src/services/switchboardd/internal/portforward/probe_test.go` against a real local listener: dials succeed once listening, the window elapses to `NOT_LISTENING` when nothing binds, and no `RUNNING` transition happens without a successful dial (SC-007)
- [X] T041 [P] [US2] Integration test in `src/services/switchboardd/internal/portforward/supervisor_start_test.go` with a fake `Runner`: full `STOPPED → STARTING → RUNNING`, and an in-sandbox start against a stopped sandbox refused with `SANDBOX_NOT_RUNNING` and zero ports allocated
- [X] T042 [P] [US2] Integration test in `src/services/switchboardd/internal/portforward/relay_test.go` against a local echo server (no `sbx` required): bytes round-trip both ways, a non-`Open` first frame is `INVALID_ARGUMENT`, and a non-`RUNNING` instance is `FAILED_PRECONDITION`
- [X] T043 [P] [US2] Integration test in `src/apps/switchboard-tui/internal/forward/forward_test.go` against a fake daemon stream: a TCP client connecting to the local listener reaches the far end, and closing the listener tears down live connections
- [X] T044 [P] [US2] UI tests in `src/apps/switchboard-tui/internal/ui/services_test.go`: nothing is running on first open, states render, a website service offers the browser action and a non-website service offers a copyable address, and the local address is displayed for running services

**Checkpoint**: The headline capability works — a service running inside an isolated sandbox is reachable from the developer's browser with zero port plumbing. **US1 + US2 together are the MVP.**

---

## Phase 5: User Story 3 - Start a service that runs outside the sandbox (Priority: P2)

**Goal**: A declared on-host service starts on the supervising daemon's host, in that sandbox's workspace, and is reachable the same way — with two sandboxes able to run their own copy concurrently.

**Independent Test**: Attach a kit declaring one out-of-sandbox service, start it, confirm the command runs on the supervising host in that sandbox's workspace and is reachable at its assigned local port; start the same service on a second sandbox and confirm both run on distinct local ports.

### Implementation for User Story 3

- [X] T045 [US3] Implement the on-host launcher in `src/services/switchboardd/internal/portforward/launch_host.go`: `exec.CommandContext` in the sandbox workspace (or its declared `working_dir`) with `SysProcAttr{Setpgid: true}`, streaming into the bounded buffer — reusing the workdir-containment check from `escapehatch.resolveWorkdir`
- [X] T046 [US3] Implement effective-port resolution in `src/services/switchboardd/internal/portforward/ports.go`: substitute a freshly allocated free host port for a `{{port}}` token in an on-host command, otherwise use the declared `listen_port`; export `PORT` and `SWITCHBOARD_SERVICE_PORT` set to the effective port in both cases (research R4)
- [X] T047 [US3] Probe the effective port **before launch** for an on-host service with no `{{port}}` token and fail with `PORT_IN_USE` when it is already held, so the failure is reported rather than the developer reaching someone else's process (US3-4)

### Tests for User Story 3

- [X] T048 [P] [US3] Unit tests in `src/services/switchboardd/internal/portforward/launch_host_test.go`: the command runs on the host in the sandbox's workspace, the process group is set, and `PORT`/`SWITCHBOARD_SERVICE_PORT` carry the effective port
- [X] T049 [P] [US3] Unit tests in `src/services/switchboardd/internal/portforward/ports_effective_test.go`: `{{port}}` is substituted with a free host port, absent `{{port}}` yields the declared port, and in-sandbox services always use the declared port
- [X] T050 [P] [US3] Integration test in `src/services/switchboardd/internal/portforward/supervisor_host_test.go`: two sandboxes running the same `{{port}}` service coexist on distinct ports and both remain reachable (US3-3), while the same service without `{{port}}` fails the second start with `PORT_IN_USE` (US3-4)

**Checkpoint**: Stacks that cannot be fully sandboxed are brought up from the same menu, with the collision behaviour the author chose.

---

## Phase 6: User Story 4 - Manage running services (Priority: P2)

**Goal**: Lifecycle control and failure visibility — stop releases everything, crashes are announced and diagnosable, and sandbox teardown leaves nothing behind.

**Independent Test**: Start services, confirm states and addresses are shown; stop one and confirm the process ends and the port is released; kill a service out of band and confirm it shows failed with its output; stop the sandbox and confirm all of its services stop.

### Implementation for User Story 4

- [X] T051 [US4] Implement stopping in `src/services/switchboardd/internal/portforward/stop.go`: signal the **whole process group** (`syscall.Kill(-pgid, SIGTERM)` on-host; `Runner.Exec … kill -TERM -<pgid>` in-sandbox), wait the 10 s grace period, force-kill survivors with `SIGKILL`, then release the local port and unpublish **only once the listen port is observed free** (clarification Q4, research R6)
- [X] T052 [US4] Detect unexpected exits in `src/services/switchboardd/internal/portforward/supervisor.go`: a process that exits after reaching `RUNNING` moves to `FAILED` with `EXITED_UNEXPECTEDLY`, retains its captured output, and releases its port (FR-047, FR-048)
- [X] T053 [US4] Cascade teardown from `src/services/switchboardd/internal/sandbox/manager.go`: stopping, destroying, or refreshing a sandbox stops every instance of that sandbox and releases every port, attaching to the same non-RUNNING teardown hook `escapehatch.Service.Cancel` uses (FR-048, US4-5)
- [X] T054 [US4] Handle `Event.service_instance` and the `SERVICE_FAILED` notification in `src/apps/switchboard-tui/internal/ui/notifications.go` so a failure that happens while the developer is elsewhere in the client reaches the inbox (FR-052)
- [X] T055 [US4] Add the running-services indicator to each sandbox row in `src/apps/switchboard-tui/internal/ui/sandbox_list.go` (FR-045 — this is the only cross-sandbox surface; there is no global services screen)
- [X] T056 [US4] Surface a service's captured output (with truncation made evident) from the services screen in `src/apps/switchboard-tui/internal/ui/services.go`, so a failure is diagnosable without leaving the client (FR-051)

### Tests for User Story 4

- [X] T057 [P] [US4] Unit tests in `src/services/switchboardd/internal/portforward/stop_test.go`: a wrapper command whose **child** is the real listener has the child killed too, a process ignoring `SIGTERM` is force-killed after the grace period, and the port is released only after the listen port goes free
- [X] T058 [P] [US4] Integration test in `src/services/switchboardd/internal/portforward/supervisor_lifecycle_test.go`: an out-of-band kill produces `EXITED_UNEXPECTEDLY` with output retained and the port released; starting an already-running service is idempotent (same instance, same local address, no second process)
- [X] T059 [P] [US4] Integration test in `src/services/switchboardd/internal/sandbox/manager_services_cascade_test.go`: stopping and destroying a sandbox with running services leaves no orphaned process and no held port (SC-004)
- [X] T060 [P] [US4] UI tests in `src/apps/switchboard-tui/internal/ui/services_lifecycle_test.go`: a failure raises exactly one inbox notification while a successful start and a developer-initiated stop raise none, and the sandbox row indicator tracks the running count

**Checkpoint**: Started services are no longer a one-way door — lifecycle, diagnosis, and cleanup are all in the client.

---

## Phase 7: User Story 5 - Reach services on remote-host sandboxes (Priority: P3)

**Goal**: The workflow is identical for a sandbox on a remote host — the address is on the developer's machine and reaches the remote service.

**Independent Test**: With a sandbox on a remote host, start a declared service and confirm the address shown is on the client machine and that traffic to it reaches the service on the remote host.

### Implementation for User Story 5

- [X] T061 [US5] Handle a lost host path in `src/apps/switchboard-tui/internal/forward/manager.go` and `src/apps/switchboard-tui/internal/ui/services.go`: on host disconnect, close the affected listeners and show the service as **unreachable** rather than presenting a dead address as working (FR-050, US5-2)
- [X] T062 [US5] Re-establish forwards on reconnect in `src/apps/switchboard-tui/internal/ui/app.go`: instances still `RUNNING` on the daemon get fresh local listeners (a possibly different local port), since the service itself is daemon-owned and never restarted by a client reconnect

### Tests for User Story 5

- [X] T063 [P] [US5] Integration test in `src/apps/switchboard-tui/internal/forward/forward_remote_test.go`: with a stdio-bridged fake remote connection, the local address is on the client machine and traffic reaches the far end; dropping the connection marks the service unreachable and reconnecting re-establishes a working forward without restarting the service

**Checkpoint**: All five user stories are independently functional.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [X] T064 [P] Bring both modules to the ≥90% floor with `make cover`, adding targeted tests for uncovered branches in `src/services/switchboardd/internal/portforward/` and `src/apps/switchboard-tui/internal/forward/`
- [X] T065 [P] Extend the TUI E2E suite in `src/apps/switchboard-tui-e2e/` with a stub-`sbx` walkthrough of declare → start → address shown → stop, gated behind the existing `e2e` build tag
- [ ] T066 **BLOCKED — `sbx` is not installed in this environment.** Walk `specs/006-port-forwarding/quickstart.md` scenarios 1–4 end to end against a real `sbx` runtime and record any argv divergence against the checklist in `specs/006-port-forwarding/contracts/sbx-ports-cli.md`. The three assumed argv mappings (`ports --publish`, `ports --unpublish`, `exec`) remain stub-asserted only; `sbx exec` is the highest-risk of them (research R3).
- [X] T067 Run the full gate defined in `Makefile` — `make fmt-check vet lint test cover env-check` — and confirm `scripts/env-check.sh` still reports **no new environment variables** (research R9)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)** → blocks everything. T001 → T002 → T003 is strictly serial (same file, then codegen).
- **Phase 2 (Foundational)** → blocks all user stories. Depends on T003 for generated types and T012 for the `sbx` surface.
- **Phase 3 (US1)** → depends on Phase 2 only.
- **Phase 4 (US2)** → depends on Phase 2. Can start alongside US1 by constructing kits through the store API directly; needs US1 only for the authoring UI path.
- **Phase 5 (US3)** → depends on Phase 4 (reuses the supervisor, probing, relay, and UI).
- **Phase 6 (US4)** → depends on Phase 4; independent of US3.
- **Phase 7 (US5)** → depends on Phase 4.
- **Phase 8 (Polish)** → depends on everything.

### User Story Dependencies

- **US1 (P1)**: independent — authoring only.
- **US2 (P1)**: needs a declared service, which Foundational's resolution + a store-level fixture supply; the full path is nicer with US1.
- **US3 (P2)**: builds on US2's allocation and access path.
- **US4 (P2)**: builds on US2's lifecycle; independent of US3.
- **US5 (P3)**: extends US2's reach path only; adds no new lifecycle.

### Within Each User Story

Implementation tasks precede their test tasks in listing order, but the two sets are interleaved in practice — Rule VI expects tests colocated and written with the code, not after the story closes.

### Parallel Opportunities

- **Phase 1**: T001, T004, T005 in parallel (three different files); T002/T003 serial after T001.
- **Phase 2**: T006, T008, T010 are three independent files, each with its own `[P]` test task; T012 is independent of all of them.
- **Phase 4**: T025, T026, T027 are independent files that T028 then composes; the whole client side (T033–T037) can proceed in parallel with the daemon side once the proto types exist.
- **Across stories**: US3, US4, and US5 touch mostly disjoint files once US2 lands, so all three can run concurrently.

---

## Parallel Example: User Story 2

```text
# Three independent daemon-side files, then compose:
T025 ports.go            ─┐
T026 launch_sandbox.go   ─┼─► T028 supervisor Start path
T027 probe.go            ─┘

# Client side, concurrently (needs only T003's generated types):
T033 client/sandbox.go → T034 forward/manager.go → T035 forward/relay.go
T036 ui/services.go + T037 ui/keys.go, app.go

# Test tasks, all parallel once their subject lands:
T038 T039 T040 T041 T042 T043 T044
```

## Parallel Example: Foundational

```text
T006 resolve.go     + T007 resolve_test.go
T008 instances.go   + T009 instances_test.go
T010 output.go      + T011 output_test.go
T012 runner.go      + T013 runner_ports_test.go
```

---

## Implementation Strategy

### MVP (User Stories 1 + 2)

Phases 1 → 2 → 3 → 4. That delivers the headline loop: declare a service on a kit, start it from the client, open it in a browser on a free local port. Everything after it extends reach (US3, US5) or hardens lifecycle (US4).

### Incremental Delivery

1. **Setup + Foundational** — contract, resolution, persistence, instance store, `sbx` surface. Nothing user-visible.
2. **US1** — kits can declare services; reviewable and shareable on its own.
3. **US2** — 🎯 MVP. Ship here if you need to ship.
4. **US3** — host-run services and the `{{port}}` collision story.
5. **US4** — stop semantics, crash visibility, cascade cleanup, notifications.
6. **US5** — remote-host sandboxes (mostly free from the R1 relay design; this phase is the unreachable-state handling).
7. **Polish** — coverage floor, E2E, quickstart walkthrough, full gate.

### Parallel Team Strategy

After Phase 2, one contributor can take US1 (client store + editor) while another takes US2's daemon side (T025–T032) and a third takes US2's client side (T033–T037). Once US2 lands, US3/US4/US5 are largely disjoint and can be split three ways.

---

## Notes

- **The `sbx exec` argv is the highest-risk assumption in the feature** (research R3, contracts/sbx-ports-cli.md). T012/T013 pin it behind one `Runner` method with a stub-asserted argv; T026, T051, and the loopback probe all move together if the real spelling differs. Reconcile it first when `sbx` becomes available.
- **`RUNNING` must never be set from a successful launch** — only from a successful dial (T027, T028). This is the invariant SC-007 depends on and the easiest one to regress under time pressure.
- **The loopback-only diagnosis** (`NOT_LISTENING_LOOPBACK` via `/proc/net/tcp` inside the sandbox, research R5) is folded into T027's probe path and asserted by T040; it applies to in-sandbox services only, since on-host services are reached over their own host's loopback.
- **Output retention is tail-first here** (T010), the opposite of `escapehatch.boundedBuffer`. A long-running service's diagnostic bytes are the last ones before it died.
- **No new environment variables** — every bound is a package constant (research R9). T067 guards this.
