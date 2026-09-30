# Quickstart & Validation: Edit Sandbox Sources (Add & Remove Folders)

Runnable scenarios proving the feature end-to-end, mapped to the spec's user stories and success
criteria. Mechanics are specified in [research.md](./research.md) (R1–R8) and
[data-model.md](./data-model.md); wire additions in
[contracts/switchboard-edit-sources.proto](./contracts/switchboard-edit-sources.proto).

## Prerequisites

- `sxbd serve` on the target host(s); `sxb` connected. Both built from this feature's branch.
- Docker + `sbx` for the full path. Without them, the daemon E2E stubs assert everything except
  Scenario 0 (which exists precisely because `sbx` is absent in dev).
- Two throwaway git repos to seed with, e.g. `~/tmp/repo-a`, `~/tmp/repo-b` (plus `~/tmp/repo-c`
  for the collision case). Snapshot them for the originals-untouched checks:

```bash
mkdir -p ~/tmp/repo-{a,b,c} && for r in a b c; do (cd ~/tmp/repo-$r && git init -q && echo $r > README.md && git add -A && git commit -qm init); done
tar -C ~/tmp -cf /tmp/originals-before.tar repo-a repo-b repo-c
```

## Scenario 0 — Reconciliation gate: live mount propagation (research R1) ⚠ FIRST

The feature's one runtime assumption. Run this before trusting anything else, on a host with a
real `sbx`:

```bash
# launch a sandbox seeded with repo-a (TUI: `n`, select repo-a), then on the HOST:
WS=$(sxbd-workspace-of <sandbox>)        # i.e. $SWITCHBOARDD_WORKSPACE_ROOT/<name>
mkdir "$WS/live-probe" && echo hi > "$WS/live-probe/x"
# in the sandbox's terminal (`t`): ls /path/to/workspace   → live-probe MUST appear, no restart
rm -rf "$WS/live-probe"                  #                 → and vanish again
```

**Pass**: both changes visible live → option A holds. **Fail**: fall back to option B
(stop→mutate→bring-up) inside the same RPCs — no contract change (research risk 1).

> **Status (implementation pass, 2026-09-30): PENDING real-`sbx` reconciliation.** `sbx` is not
> installed in the development environment, so this scenario has not been run against a real
> runtime. Scenarios 1–5 below were exercised with the stub harness instead (manager unit tests
> on temp workspaces, gRPC round-trips over the in-process socket, `teatest` overlay/confirm
> tests, and the PTY-driven `TestTUIEditSourcesE2E` arc with a stub `sbx`). The option-B
> fallback stays untriggered until this scenario runs on a host with Docker Sandboxes installed.

## Scenario 1 — Add to a running sandbox (US1; SC-001/002/003/008)

1. Launch a sandbox seeded with `repo-a` (duplicate mode). Open its terminal (`t`), start the
   agent on any task, keep the terminal attached.
2. On the list, press `S` → the sources overlay lists `repo-a`. Press `a`, browse to
   `~/tmp/repo-b`, select, confirm. **Expect**: the row badges "adding repo-b — NN%" while the
   TUI stays fully usable (FR-055); interaction start-to-confirm under 30 s (SC-001).
3. When the badge clears: in the sandbox terminal, `ls` the workspace → `repo-b` present, no
   restart happened, the attached terminal was never dropped (SC-002); overlay + list row now
   show both folders.
4. `tar -C ~/tmp -cf /tmp/originals-after.tar repo-a repo-b repo-c && cmp /tmp/originals-before.tar /tmp/originals-after.tar`
   → identical (SC-003).
5. Mid-copy of a large add, check the workspace root inside the sandbox: the new folder MUST NOT
   be visible until complete (staging, R2) — it appears atomically.

## Scenario 2 — Remove, with every guard (US2; SC-003/006)

1. On the same sandbox (now `repo-a` + `repo-b`): `S`, mark `repo-b` with `space`, press `d`.
   **Expect**: a confirm naming the sandbox, `repo-b`, and the uncommitted-work consequence;
   `esc` and any unrecognised key cancel (SC-006).
2. Re-run and confirm. **Expect**: `repo-b` gone from workspace + overlay + row; sandbox still
   RUNNING; agent terminal intact; original `~/tmp/repo-b` untouched (SC-003).
3. **Last-source floor (FR-061)**: attempt to remove `repo-a` → refused, nothing deleted.
4. **Service guard (FR-062)**: attach a kit declaring a service with `working_dir: repo-a`,
   start it (`p`), then attempt to remove `repo-a` → refused naming the service; stop the
   service → removal proceeds.
5. **Already-gone copy (FR-063)**: `rm -rf <workspace>/repo-b` by hand after re-adding it, then
   remove `repo-b` via the TUI → succeeds, record cleaned.

## Scenario 3 — Record fidelity downstream (US3; SC-004)

1. With an edited sandbox (added `repo-b`, removed nothing): press `F` (refresh), confirm.
   **Expect**: the confirm lists **both** folders; after refresh the workspace holds fresh copies
   of exactly `repo-a` + `repo-b` (the edit was re-seeded; nothing resurrected, nothing lost).
2. Remove `repo-b`, then refresh again → confirm lists only `repo-a`; workspace ends with only
   `repo-a` (SC-004).
3. Stop (`s`) then start the sandbox → it comes back with the edited set.
4. Escape-hatch scoping (spec US3-4): with a command whose `workspaces` glob matches `repo-b`,
   verify the workspace picker offers it only while `repo-b` exists.

## Scenario 4 — Stopped sandbox + remote host (US4; SC-007)

1. Stop a sandbox; `S` → add `repo-c`, remove `repo-a`. **Expect**: retained copy edited on
   disk immediately; start it → the sandbox sees the edited set.
2. On a connected SSH host: `S` on that host's sandbox → the browser lists the **remote**
   filesystem; add a remote folder; verify the copy landed in the remote daemon's workspace and
   the row updated (SC-007). A second connected client sees the updated row without reloading
   (FR-067).

## Scenario 5 — Refusals & failure atomicity (FR-056/058/066; SC-005)

```bash
# collision: add ~/tmp/repo-b twice / add a folder named like an agent-created dir
# reserved:  add a folder literally named .switchboard
# vanished:  select a folder, delete it on the host, confirm the add
# busy:      start a large add, immediately press F (refresh) on the same sandbox
```

**Expect**: each refused up front naming folder + reason; the busy case says an add is in
progress (FR-066). For atomicity (SC-005): fill the disk (or point at a huge source) so a copy
dies mid-flight → error surfaced, no partial folder at any seeded path, `.switchboard/staging`
empty, record unchanged, sandbox still RUNNING — never ERROR (FR-058). Low-disk warning +
override round-trip mirrors launch (FR-057).

## Verification

```bash
make all            # fmt-check + vet + lint + test  (Rule V fast gate)
make cover          # ≥90% per module (Rule VI floor)
make e2e            # TUI E2E (PTY + stub sbx, runs anywhere) + daemon E2E (auto-skips w/o Docker)
make env-check      # unchanged surface — feature adds no env vars (Rule VIII)
```

New tests ride the existing harnesses: manager add/remove unit tests against a temp workspace
root + fake Runner (staging, rename-commit, rollback, latch, guards), gRPC round-trips over the
in-process socket (stream shape, blocked/override, error codes), `teatest` overlay/confirm
interaction, and a stub-`sbx` E2E pass for the full add→refresh→remove arc.
