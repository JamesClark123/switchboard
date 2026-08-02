# Contract: host `sbx` surface used by Port Forwarding

**Feature**: `006-port-forwarding` | **Research**: [research.md](./research.md) (R2, R3, R5, R6)

Feature 006 adds two methods to `sandbox.Runner` (`src/services/switchboardd/internal/sandbox/runner.go`).
`sbx` is **not installed in this environment**, so — exactly as with `AddKit`/`ValidateKit` in feature
004 and the policy call in 005 — the argv below is documentation-derived and pinned by argv-asserting
stub-script tests. This file is the contract those tests encode, and the checklist to reconcile against a
real `sbx`.

---

## `Runner.PublishPort` / `Runner.UnpublishPort`

```go
PublishPort(ctx context.Context, containerRef string, hostPort, sandboxPort uint32) error
UnpublishPort(ctx context.Context, containerRef string, hostPort, sandboxPort uint32) error
```

| | argv |
|---|---|
| publish | `sbx ports <ref> --publish 127.0.0.1:<hostPort>:<sandboxPort>/tcp` |
| unpublish | `sbx ports <ref> --unpublish 127.0.0.1:<hostPort>:<sandboxPort>/tcp` |

Source: parent `CLAUDE.md`, *Publishing ports to the host* —
`sbx ports <sandbox-name> --publish [[HOST_IP:]HOST_PORT:]SANDBOX_PORT[/PROTOCOL]`.

**Why the explicit `127.0.0.1`**: the spec assumes forwarded services are reachable only from the
developer's own machine. Omitting the host IP would bind all interfaces and expose every forwarded
sandbox service to the LAN.

**Unpublish must mirror publish exactly.** The daemon stores the published pair on the instance and
replays it verbatim; reconstructing it from the declaration would drift the moment `{{port}}`
substitution changes the effective port.

**Reconciliation checks**:

- [ ] Does `--unpublish` take the same triple, or just the host port?
- [ ] Is the `HOST_IP` segment accepted (see R2's fallback note if not)?
- [ ] Does publishing to an already-bound host port fail fast, or bind lazily and fail at connect time?
      The 3-retry allocation loop (R2) depends on a fast, distinguishable rejection.

---

## `Runner.Exec`

```go
Exec(ctx context.Context, containerRef string, argv []string) (*exec.Cmd, error)
```

Assumed argv: `sbx exec <ref> -- <argv...>`

⚠ **This is the highest-risk assumption in the feature.** Unlike the ports surface, no documentation
was consulted for it — it is assumed by analogy with `docker exec`. Three call sites depend on it:

| Call site | Command run inside the sandbox | Research |
|---|---|---|
| Start an in-sandbox service | `/bin/sh -c 'cd "<dir>" && exec setsid /bin/sh -c '\''echo "swb-pgid:$$" >&2; exec <command>'\'''` | R3 |
| Diagnose a loopback-only bind | `/bin/sh -c 'cat /proc/net/tcp /proc/net/tcp6'` | R5 |
| Stop an in-sandbox service | `/bin/sh -c 'kill -TERM -<pgid>'`, then `kill -KILL -<pgid>` | R6 |

If the real spelling differs (`sbx run`, `sbx shell -c`, an interactive-only variant), all three move
together and only this one method body plus its argv test change.

**Reconciliation checks**:

- [ ] Subcommand name and the `--` separator.
- [ ] Does it allocate a TTY by default? A TTY would merge stderr into stdout and break the
      `swb-pgid:` marker parse — a `--no-tty`/`-T` equivalent is required if so.
- [ ] Does it exit when the in-sandbox process exits, propagating its exit status?
- [ ] Does killing the host-side `sbx exec` child leave the in-sandbox process running? (R6 assumes it
      may, which is why the PGID is recorded; if `sbx exec` already reaps the tree, the explicit
      in-sandbox kill becomes belt-and-braces rather than load-bearing.)

---

## Behaviour required of both

- **Non-interactive**: no prompt, no tty requirement — the daemon has no terminal.
- **Deterministic argv**: no map iteration in flag rendering (the existing `flags()` sorts keys for
  exactly this reason).
- **Failure is diagnosable**: combined output is captured and surfaced in `ServiceInstance.failure_detail`
  rather than collapsed into a generic error, per FR-051.
