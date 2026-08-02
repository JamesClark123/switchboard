# Quickstart & Validation: Port Forwarding

**Feature**: `006-port-forwarding` | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

How to exercise the feature end to end and confirm each success criterion. Scenarios 1–4 run against a
real sandbox runtime; scenario 5 needs a second host. The automated gates in
[Verification](#verification) run anywhere, `sbx` or not.

---

## Prerequisites

```bash
make build            # all three Go modules
```

- `switchboardd` (`sxbd`) running on the host, and `sxb` (the TUI) able to reach it.
- A working `sbx` + Docker runtime for scenarios 1–5. **Note**: `sbx` is not installed in the dev
  environment; without it, only the automated gates below apply, and the argv-asserting stub tests are
  what stand in for the real CLI (see [contracts/sbx-ports-cli.md](./contracts/sbx-ports-cli.md)).

---

## Scenario 1 — Declare services on a kit (US1, FR-043)

1. `sxb` → `K` (kits) → create or edit a kit → open the **Services** section.
2. Add two entries:

   | name | command | port | location | website |
   |---|---|---|---|---|
   | `web` | `pnpm dev --host 0.0.0.0` | 3000 | in sandbox | ✅ |
   | `worker` | `pnpm worker --port {{port}}` | 7000 | on host | ✕ |

3. Save, quit the editor, reopen the kit.

**Expect**: both entries persist with every field intact, and the kit still validates.

**Also check** (US1-3/4/5):

- Clearing `name`, `command`, or the port, or setting a duplicate name, is rejected on save with a
  message naming the field — and the stored kit is unchanged.
- Escaping the workspace in `working_dir` (`../elsewhere`) is rejected.
- Abandoning an edit with `esc` leaves `kits/<id>/services.yaml` untouched.

> `--host 0.0.0.0` on the in-sandbox entry is deliberate — see scenario 4.

---

## Scenario 2 — Start an in-sandbox service and open it locally (US2, SC-001/002/007)

1. Launch a sandbox with that kit attached (`n`, pick the kit) — or attach to a running one with `A`.
2. On the sandbox list, press **`p`**.

**Expect**: both services listed as `stopped`. **Nothing is running** — SC-005's first case.

3. Select `web`, start it.

**Expect**: `starting` → `running`, with a local address such as `127.0.0.1:49221`. Two actions, zero
port numbers typed (SC-002).

4. Open it (browser action, since `is_website` is set).

**Expect**: the sandbox's dev server loads in your browser (SC-001).

```bash
# the address is on YOUR machine, and it was free beforehand
curl -sS http://127.0.0.1:<local_port>/ | head -5
```

---

## Scenario 3 — On-host service, and two sandboxes at once (US3, SC-003)

1. Start `worker` on sandbox A. Confirm it runs **on the daemon host**, not in the sandbox:

   ```bash
   pgrep -af 'pnpm worker'        # visible on the host
   ```

2. Attach the same kit to sandbox B and start `worker` there too.

**Expect**: both `running`, on **two different local ports**, both reachable — because the command uses
`{{port}}`, so each instance binds a different host port (research R4).

3. Now edit the kit so the command is plain `pnpm worker` (no `{{port}}`) and repeat with two sandboxes.

**Expect**: the second start fails with `PORT_IN_USE` rather than silently shadowing the first (US3-4).
That the outcome differs is the point — it is the author's choice, visible in the command.

---

## Scenario 4 — Lifecycle, failure, and the loopback trap (US4, SC-004/006/007)

**Stop releases everything**

```bash
ss -ltn | grep <local_port>      # before: held
# stop the service from the list
ss -ltn | grep <local_port>      # after: gone
pgrep -af 'pnpm dev'             # after: gone — including the child of the wrapper
```

**Crash is reported, not hidden**: kill the service out of band (`pkill -f 'pnpm dev'`).

**Expect**: state → `failed`, output retained and readable from the client, local port released, and —
because you are looking at a different sandbox — a **notification** announcing it (FR-052, SC-006).

**Loopback trap**: edit `web`'s command to drop `--host 0.0.0.0` (Vite and friends then bind
`127.0.0.1` only) and start it.

**Expect**: it does **not** show as running. After the readiness window it reports
`NOT_LISTENING_LOOPBACK` and names binding to all interfaces as the fix (clarification Q1, SC-007).

**Sandbox teardown cascades**: with services running, stop the sandbox.

**Expect**: every service stops, every port is released, no orphan remains (US4-5, SC-004).

**Idempotence**: start an already-running service.

**Expect**: same instance, same address, no second process.

**Daemon restart**: restart `sxbd` with services running.

**Expect**: nothing running afterwards and nothing claiming to be (SC-005).

---

## Scenario 5 — Remote-host sandbox (US5, SC-008)

With a sandbox on an SSH host added via `h`:

1. Start its `web` service exactly as in scenario 2.

**Expect**: the address shown is on **your** machine and reaches the remote service — no extra steps
versus a local sandbox (SC-008). The bytes travel over the existing `dial-stdio` connection; no second
SSH session, no `-L` flag, no re-authentication (research R1).

2. Break the connection (`x` to disconnect, or drop the network).

**Expect**: the service is shown **unreachable** — never a dead address presented as working (US5-2).

---

## Verification

Automated gates, all of which run without `sbx`:

```bash
make fmt-check vet lint          # Rule I/II analogues
make test                        # unit + integration
make cover                       # ≥90% per module (Rule VI floor)
make env-check                   # must show NO new env vars (research R9)
make e2e                         # TUI via PTY with a stub sbx; daemon suite skips without Docker
```

Layers that matter most for this feature:

| Layer | What it pins |
|---|---|
| Unit | `{{port}}` substitution, `/proc/net/tcp` loopback parsing, later-kit-wins resolution, the state machine's terminal-release invariants, tail-retaining bounded buffer. |
| Integration | `ForwardPort` relay against a local echo server (no `sbx` needed); start→ready→stop against a fake `Runner`; at-most-one-instance under concurrent starts. |
| Argv stub | `PublishPort`/`UnpublishPort`/`Exec` argv, asserted against stub scripts — the standing substitute for a real `sbx`. |
