# Quickstart: validating Worker Lanes (SWI-18)

A manual walk-through of the user stories on a real setup, after the automated gates pass
(`make fmt-check vet lint test cover` and `make e2e`). Update **every** host's `sxbd` and the `sxb` client
together: the TUI only offers lanes against a daemon that advertises the `lanes` capability (research R14, R16).

## Setup

- One orchestrator `pdev-orch` on host A, with hosts A and B.
- Five workers: `w1`, `w2` on A, `w3`, `w4`, `w5` on B (attach with `o` on the sandbox list).
- Actions: `build-ui` (prompt), `review-pr` (prompt), `chore` (prompt), `think` (reason). Use a short body such as
  `echo {msg} and report completion`.

## Scenario 0 — upgrade (US6, FR-318, FR-318b, SC-005)

1. Before updating, give `build-ui` the worker tag `frontend` with the old client.
2. Update daemons and client. Open the orchestrator detail.
3. **Expect**: notice `worker tags dropped on upgrade: build-ui (#frontend) — D dismiss`, on the detail and on
   Jobs › Actions; the daemon log has one `dropped worker tag` line. The action form has no worker-tag field.
4. Call `build-ui`: it runs on an open-lane worker. Press `D`: the notice disappears in a second client too.
5. With an old client still installed, try saving an action: refused with `this client predates lanes`.

## Scenario 1 — exclusive lane (US1, US3, FR-311, FR-313, FR-315)

1. Detail › Lanes › `a`: create `frontend`. Try `open`, `Front End` and a duplicate: each refused with its reason.
2. `w` on `frontend`: tick `w1`, `w2`, `enter`. Workers view shows `lanes: frontend` on both; the sandbox list's
   Orchestrator Sandboxes tab (`O`) shows `lanes: frontend` on both and `open lane` on the others, within one
   refresh (SC-004).
3. Jobs › Actions › `e` on `build-ui`: tick `frontend`; mode preselected `exclusive`; save. The row shows
   `⇢ frontend · exclusive`.
4. Call `build-ui` 4 times and `chore` 4 times. **Expect**: `build-ui` runs only on `w1`/`w2`; `chore` only on
   `w3`–`w5`.
5. Stop `w1` and `w2` (`s`). Call `build-ui`. **Expect**: queued with `waiting for w1, w2, stopped or unreachable`;
   `orchestrator_check_run` from an agent names no workers (`waiting: every worker that could take it is stopped or
   unreachable`). Start them: delivered within one pass.
6. Remove both from `frontend` (confirm the guard if prompted). Queued `build-ui` shows `waiting for a worker in
   frontend`; `orchestrator_check_run` from an agent shows the same. Add `w1` back: delivered with no other action
   (SC-007).

## Scenario 2 — inclusive and reservations (US2, FR-312, FR-314, SC-002)

1. Create `reviewers` with `w3`. Make `review-pr` inclusive to `reviewers`, and add an action `review-strict`
   exclusive to `reviewers`.
2. Make every worker busy except `w3` and one open-lane worker (`w4`): call `chore` until they are taken.
3. Each call starts its own pass, so queue both while `w3` and `w4` cannot take work: break the orchestrator
   daemon's link to host B (both live there), for example by stopping `sxbd` on B. Disconnecting B in the TUI does not
   affect that link. Call `review-pr` then `review-strict`, then start `sxbd` on B again so one pass sees both after
   reconcile. **Expect**: `review-pr` → `w4`, `review-strict` → `w3`. (The daemon test in T026 pins this exactly.)
4. Mark `w3` busy, call `review-pr`: it goes to an open-lane worker.

## Scenario 3 — open-lane guard (US4.6, FR-308a, FR-308b)

1. Leave exactly one open-lane worker. Put it in `frontend` with `l` on the Workers view.
   **Expect**: confirm `No open-lane worker will be left to run: chore, review-pr …`. `n` changes nothing; `y` saves.
2. In a second client, with the confirm still open in the first, add another un-laned action; then press `y` in the
   first. **Expect**: a second confirm listing the new action.
3. Make the last open-lane worker one on host B. Disconnect host A (the orchestrator's host) in the client, then
   `o` (detach) that worker from host B's sandbox list. **Expect**: the confirm's best-effort warning from last-known
   data; with no data, `Cannot check open-lane workers`.

## Scenario 4 — attach and launch with lanes (US4.8, US4.9, FR-306a, FR-306b, L3, L4)

1. On the sandbox list, `o` on a new sandbox, choose `pdev-orch`, tick `frontend`, `enter`. **Expect**: the worker
   appears with `lanes: frontend` and never receives an un-laned run.
2. Launch wizard, `O` → `pdev-orch`, tick `reviewers`, launch on host B. **Expect**: adopted within moments of the
   launch (not at the next reconnect) with `lanes: reviewers`.
3. Ticket expiry and *lanes pending* adoption have no reproducible manual path; they are covered by daemon tests
   (T009, T039 with an injected clock), and recovering with `l` by T036 and T045.
4. In the launch wizard, pick `pdev-orch` with lanes, then change the host with `H`. **Expect**: the orchestrator
   and lanes are cleared. Delete a ticked lane from a second client before launching: the wizard stays open on the
   lane step with `lane "<id>" no longer exists`.

## Scenario 5 — Manage lanes from the action form (US5, FR-316, FR-316a, FR-317, SC-003)

1. Jobs › Actions › `a`. Fill Name, Body and Arguments. `ctrl+l`: create `docs`, add `w5`, `esc`.
   **Expect**: the form keeps every value; `docs` is offered. Tick it, save. Time it: under 2 minutes (SC-003).
2. Edit the action, tick `docs`; in a second client delete `docs`; save in the first.
   **Expect**: refused (`lane … no longer exists`), the form stays open with its values and no longer offers `docs`.
3. Switch Kind to `reason`: the lanes hide, with `lanes not saved: reason actions have no lanes`; switching back to `prompt` shows the ticks again, and a save as `reason` stores no lanes.

## Scenario 6 — renames, deletes, deleted actions (FR-304, FR-305, FR-314b, L7)

1. Rename `frontend` → `ui`. **Expect**: members and actions keep it; queued runs route as before; briefings and
   `orchestrator_list_actions` now say `ui`; descriptions never appear there.
2. Queue `build-ui` with no `ui` worker free, then delete the action. **Expect**: the queued run still waits for
   `ui` and later runs on a `ui` worker.
3. Delete `ui`. **Expect**: the confirm lists members, actions (with mode), actions left un-laned, and `N queued runs
   will be re-routed`.

## Scenario 7 — restart and reconnect (L1, L2, FR-308c, FR-322)

1. Restart host A's daemon with runs queued and workers in lanes. **Expect**: lanes and memberships unchanged; host B
   workers show `unreachable` until its link is up; runs that only they can take say `stopped or unreachable`.
2. Detach a worker. **Expect**: it stays detached. (Replaying an older `sandbox_changed` event is covered by the
   T040 and T009 tests, which drive a fake event stream; there is no manual hook.)
3. Destroy a worker on host B. **Expect**: it leaves the roster at once; no run is offered to it.
