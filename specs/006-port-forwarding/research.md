# Phase 0 Research: Port Forwarding

**Feature**: `006-port-forwarding` | **Date**: 2026-08-02 | **Spec**: [spec.md](./spec.md)

Every unknown surfaced by the Technical Context is resolved below. Each decision records what was
chosen, why, and what was rejected. Decisions marked **⚠ unverified argv** depend on `sbx` subcommands
that cannot be exercised in this environment (`sbx` is still not installed — the standing caveat from
features 004 and 005); those are pinned by argv-asserting stub tests and listed under
[Reconciliation risks](#reconciliation-risks).

---

## R1 — Where the developer-machine port lives, and how traffic crosses hosts

**Decision**: The **client** owns every developer-machine listener. It binds `127.0.0.1:0` (the OS
picks a free port), and each accepted TCP connection is relayed to the daemon over a **new
bidirectional streaming RPC, `ForwardPort`**, carried by the client's *existing* daemon connection.
The daemon dials the service's host-side endpoint and pipes bytes both ways until either side closes.

This is one code path for both host kinds:

```
 developer's machine                    daemon host                      sandbox
 ┌─────────────────┐   existing conn   ┌──────────────┐   sbx publish   ┌──────────────┐
 │ 127.0.0.1:49221 │◄─────────────────►│ ForwardPort  │◄───────────────►│ :3000        │
 │  (client listener)│  unix sock  OR  │  → dial      │                 │ (in-sandbox) │
 └─────────────────┘  ssh dial-stdio   │ 127.0.0.1:P  │────────────────►│ or host proc │
                                        └──────────────┘
```

**Rationale**:

- **Remote hosts work with no new transport, no second authentication.** The alternative
  (`ssh -N -L local:127.0.0.1:remote`) needs a *second* SSH session. The client's SSH password is
  deliberately transient — `HostEntry.SSHPassword` "is NEVER persisted… only populated on the copy the
  dialer receives" (`internal/client/manager.go`) — so a password-authenticated host could not open a
  second session without re-prompting. Relaying over the already-authenticated `dial-stdio` transport
  sidesteps this entirely.
- **FR-049's uniqueness guarantee becomes trivial.** One allocator (the client) owns every port on the
  developer's machine, so "unique across all running services, including services from different
  sandboxes" holds by construction — even across sandboxes on *different* hosts, which no daemon-side
  allocator could guarantee.
- **`localhost` means the client machine** (Key Decision 4), including for remote sandboxes (US5,
  SC-008), with no special case.
- Precedent: this is the `kubectl port-forward` / `docker attach` stream-multiplexing shape, and the
  repo already relays interactive byte streams over gRPC (`AttachAgent`, feature 003).

**Alternatives rejected**:

| Alternative | Rejected because |
|---|---|
| Daemon publishes on its own host; client uses it directly (local hosts only) | Zero-overhead locally, but needs a *second*, different mechanism for remote hosts — and that mechanism is the relay anyway. Two paths, split port allocation, and FR-049 uniqueness spanning both. |
| `ssh -N -L` child process per remote service | Re-authentication problem above; a second process tree to supervise; no benefit over reusing the live connection. |
| `golang.org/x/crypto/ssh` in-process tunnel | New third-party dependency, and re-implements the auth/config handling (`~/.ssh/config`, agents, askpass) the `ssh` binary already gives feature 001. |

**Consequence to accept**: the developer-machine endpoint is owned by the client process, so it dies
when the client exits — for local hosts too, and the port may differ after a reconnect. The spec's
edge case already allows this ("A remote-host access path may need to be re-established"); this design
widens it to all hosts. The **service itself is unaffected** — it is daemon-owned and keeps running,
which is what the edge case actually protects.

---

## R2 — Reaching an in-sandbox listener from the daemon ⚠ unverified argv

**Decision**: When an in-sandbox service becomes startable, the daemon allocates a free port `P` on its
own host and runs
`sbx ports <sandbox> --publish 127.0.0.1:P:<listen_port>/tcp`, then treats `127.0.0.1:P` as the
service's **host endpoint** (what `ForwardPort` dials). On stop it runs the matching
`--unpublish 127.0.0.1:P:<listen_port>/tcp`.

**Rationale**: This is the documented mechanism for crossing the sandbox network boundary (parent
`CLAUDE.md`, "Publishing ports to the host"), and it is the only one that does not require injecting
tooling into the sandbox image. Binding the host side to `127.0.0.1` — rather than the default
all-interfaces bind — is what keeps the spec's "not exposed to the wider network" assumption true; the
publish syntax `[[HOST_IP:]HOST_PORT:]SANDBOX_PORT[/PROTOCOL]` supports the explicit host IP.

**Port allocation on the daemon host**: bind `127.0.0.1:0`, read the assigned port, close, then publish.
This is a TOCTOU window; it is mitigated by publishing immediately and retrying the whole allocation up
to 3 times when the publish is rejected for a bound port. A deterministic scan of a fixed range was
rejected — it collides with unrelated host software and needs a persisted cursor.

**Alternative rejected**: run a relay *inside* the sandbox and have it dial out. Requires a binary in
every sandbox image and inverts the connection direction for no gain.

---

## R3 — Running a long-lived command inside the sandbox ⚠ unverified argv

**Decision**: The daemon starts in-sandbox services with

```sh
sbx exec <sandbox> -- /bin/sh -c 'cd "<workdir>" && exec setsid /bin/sh -c '\''echo "swb-pgid:$$" >&2; exec <command>'\'''
```

The `setsid` child is a session leader, so its PID equals its process-group ID; it announces that PGID
on stderr as its first line, which the daemon parses and records on the instance. Everything after the
marker line is ordinary captured output.

**Rationale**: The daemon-side `sbx exec` child is *not* the service — killing it does not necessarily
kill the process tree inside the sandbox. Recording the in-sandbox PGID is what makes FR-048's
"terminate the command **and every process it spawned**" enforceable from outside (see R6). Announcing
the PGID over stderr needs no extra tooling, no shared filesystem convention, and no second exec.

**Alternatives rejected**:

| Alternative | Rejected because |
|---|---|
| Write the PGID to a file in the bind-mounted workspace | Pollutes the developer's tree, races on refresh, and needs cleanup on every abnormal exit. |
| Kill by port (`fuser -k`) or by command pattern (`pkill -f`) | `fuser`/`pkill` are not guaranteed present, and pattern matching kills unrelated processes on a hit. |
| Run the service through the existing agent PTY (feature 003) | That session belongs to the agent; a service would steal its terminal and its output stream. |

---

## R4 — On-host services that declare the same port (US3-3 vs US3-4)

**Problem**: The spec asserts *both* that two sandboxes running the same on-host service each get a
distinct local port and "both remain reachable" (US3 scenario 3), *and* that an on-host service whose
declared port is occupied fails (US3 scenario 4). In-sandbox services have no tension here — each
sandbox has its own network namespace, so the declared port never collides. On-host services share the
daemon host's namespace, where a second `pnpm dev` binding `3000` simply cannot start.

**Decision**: Introduce an **effective port** per instance and a `{{port}}` substitution token.

- **In-sandbox**: effective port = declared listen port, always. No allocation, no collision possible.
- **On-host, command contains `{{port}}`**: the daemon allocates a free host port, substitutes it into
  the command string, and that is the effective port. Concurrent instances across sandboxes get
  distinct ports and coexist → **US3-3 holds**.
- **On-host, no `{{port}}`**: effective port = declared listen port. A second concurrent instance is
  refused before launch (the daemon probes the port first) with `PORT_IN_USE` → **US3-4 holds**.

In both on-host cases the daemon also exports `PORT` and `SWITCHBOARD_SERVICE_PORT` set to the effective
port, which many frameworks honour without any command change.

**Rationale**: Which of the two spec scenarios you get becomes the **author's explicit choice**, visible
in the command they wrote, rather than an accident. It is additive to the authoring model in the spec
(a token inside a field that already exists) and requires no new declaration field.

**Alternatives rejected**: always allocating and relying on `$PORT` (silently wrong for commands that
ignore it — the service binds 3000 while the daemon probes 51000 and reports a bogus readiness
failure); per-sandbox network namespaces for host processes (defeats the point of running on the host);
declaring the conflict unsupported (contradicts US3-3).

---

## R5 — Readiness detection, and diagnosing a loopback-only bind

**Decision**: Readiness is a **TCP dial of the host endpoint**, retried with backoff (250 ms, capped at
2 s) until the readiness window elapses. A service is `RUNNING` only when a dial succeeds — which is
exactly what FR-047 and Key Decision 6 demand ("running is a claim about reachability").

When the window elapses with the process still alive, the daemon distinguishes two failures:

- **In-sandbox**: it reads `/proc/net/tcp` and `/proc/net/tcp6` inside the sandbox
  (`sbx exec <sandbox> -- /bin/sh -c 'cat /proc/net/tcp /proc/net/tcp6'`) and looks for the declared
  port in state `0A` (LISTEN). A match whose local address is `0100007F` (IPv4 `127.0.0.1`) or the IPv6
  loopback, with no all-interfaces (`00000000` / `::`) entry, is a definitive
  **`NOT_LISTENING_LOOPBACK`** verdict, reported with the remedy FR-047 requires. No match at all is
  **`NOT_LISTENING`**.
- **On-host**: loopback binding is *not* a defect — the host endpoint is dialled over loopback on that
  same host, and the remote case tunnels to the remote host's own loopback. So there is no loopback
  diagnosis to make; failure to become ready is simply `NOT_LISTENING`.

**Rationale**: `/proc/net/tcp` is present on every Linux container and needs no tooling in the image —
unlike `ss`, `netstat`, `lsof`, or `nc`, none of which can be assumed. This turns FR-047's requirement
that the failure "MUST name loopback-only binding as the cause" from a guess into an observation.

**Note this narrows nothing in the spec**: the bind-address requirement is stated generally, and it
remains true generally; it is only *enforceable and diagnosable* where it matters, which is the
sandbox network boundary.

---

## R6 — Stopping: process trees, grace, and when the port is released

**Decision**, implementing the Q4 clarification:

1. **Signal the whole group**, not the launched process.
   - On-host: `SysProcAttr{Setpgid: true}` at launch, then `syscall.Kill(-pgid, SIGTERM)` — the idiom
     `internal/escapehatch/process_unix.go` already uses (`setPgid`/`killPgid`), extended here with a
     graceful first phase.
   - In-sandbox: `sbx exec <sandbox> -- /bin/sh -c 'kill -TERM -<pgid>'` using the PGID recorded in R3.
2. **Wait a bounded grace period** (`stopGracePeriod`, 10 s).
3. **Force-kill survivors**: `SIGKILL` to the same group, by the same route.
4. **Release the local port only once the listen port is observed free** — the daemon re-probes the host
   endpoint until the dial fails (bounded by the grace period again), then unpublishes (in-sandbox) and
   tells the client to close its listener.

**Rationale**: The realistic declared command is a wrapper (`pnpm dev`, `npm start`) whose child is the
real listener. Killing only the launched process satisfies "the process was terminated" while leaving
the listener alive and the port held — silently violating SC-004's 100% claim. Step 4 is what makes
"released… only once nothing holds it" true rather than aspirational.

---

## R7 — Where instance state lives

**Decision**: **Declared** services are persisted; **running** state is not.

- `Sandbox.services` — the resolved declaration set — is persisted in the bbolt registry, exactly as
  `Sandbox.escape_hatch_commands` is (additive proto field, no migration). This is what makes the
  startable set enforceable and replayable across a container recreate (FR-044).
- `ServiceInstance` records live **in memory only**, for the daemon's lifetime, mirroring
  `escapehatch.RunStore`. A daemon restart therefore leaves nothing running and nothing claiming to be
  (FR-045, SC-005, and the spec's "Daemon restarts" edge case) — which is not a limitation here but the
  required behaviour.

**Rationale**: Persisting instances would create exactly the failure the spec forbids: a registry row
saying `RUNNING` after a restart that killed the process. Consistency with feature 005's session-scoped
model also means one mental model for "runs" across the two features.

---

## R8 — Authoring surface on the kit

**Decision**: A second switchboard-owned sidecar, `kits/<id>/services.yaml`, plus
`KitSpec.services` on the wire — mirroring feature 005's `escape-hatch.yaml` / `KitSpec.escape_hatch`
precisely (`store.Kit.Services []KitService` with `yaml:"-"`, saved/loaded by
`KitStore.saveServices`/`loadServices`, projected by `Kit.ToSpec`).

Services are **never** rendered into the opaque Docker `spec.yaml`: the host `sbx` has no concept of
them, switchboard owns their validation and execution, and keeping them out preserves feature 004's
property that `spec.yaml` is whatever `sbx kit validate` accepts.

Resolution onto a sandbox is the union of its attached kits' services with **later-kit-wins** on a name
collision — the same rule, and the same code shape, as `escapehatch.Resolve` (FR-044, and the spec's
two-kits edge case).

---

## R9 — Bounds, constants, and the client keybinding

**Decision**: All bounds are daemon package constants. **No new environment variables** — Rule VIII's
surface stays unchanged, as in feature 005.

| Bound | Value | Notes |
|---|---|---|
| Readiness window | 60 s default | Per-service override `readiness_timeout_seconds`; a cold `pnpm dev` on a first run needs more than a few seconds. |
| Stop grace period | 10 s | Then `SIGKILL` (R6). |
| Captured output cap | 1 MiB | Same constant and `boundedBuffer` shape as escape hatch; long-running services are noisy, so the buffer is a **ring** that keeps the *tail*, not the head — the last output before a crash is the diagnostic one. |
| Relay chunk size | 32 KiB | Matches the existing stream reader sizing. |
| Host-port allocation retries | 3 | TOCTOU mitigation (R2). |

**Keybinding**: `p` (services/ports) on the sandbox list — verified free against `internal/ui/keys.go`
(taken: `r F K A E n c C h g v t T i s d R # u a x` and space).

**Output buffer divergence worth noting**: escape hatch keeps the *head* of the output and marks
truncation, because a run-to-completion command's failure is usually at the start. A long-running
service inverts this — the interesting bytes are the last ones before it died — so this feature keeps
the tail. Same cap, opposite retention.

---

## Reconciliation risks

`sbx` is still not installed in this environment, so three argv mappings are documentation-derived and
pinned only by stub-script tests. They are the first call-sites to reconcile against a real `sbx`:

| Call site | Assumed argv | Source |
|---|---|---|
| `SbxRunner.PublishPort` / `UnpublishPort` | `sbx ports <ref> --publish 127.0.0.1:P:L/tcp` / `--unpublish …` | Parent `CLAUDE.md`, "Publishing ports to the host" — documented syntax, exact flag spelling unverified. |
| `SbxRunner.Exec` | `sbx exec <ref> -- <argv…>` | **Assumed by analogy only** — no documentation consulted for this subcommand. Highest-risk of the three; if `sbx` spells it differently (`sbx run`, `sbx shell -c`), R3, R5's loopback probe, and R6's in-sandbox kill all move with it. |
| Publish accepting an explicit `127.0.0.1` host IP | `[[HOST_IP:]HOST_PORT:]SANDBOX_PORT` | Documented form; if the host-IP segment is unsupported, the published port binds all interfaces and the "not exposed to the wider network" assumption needs a firewall note or a relay fallback. |

Each is isolated behind the `sandbox.Runner` interface, so reconciliation is a change to one method
body plus its argv test — not a redesign.
