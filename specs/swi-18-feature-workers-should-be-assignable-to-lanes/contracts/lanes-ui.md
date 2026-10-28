# Contract: TUI for lanes (SWI-18)

Paths are relative to `src/apps/switchboard-tui/internal/ui/`. All lane names, worker names and action names are
drawn through `sanitizeLine`/`fitCol` (FR-309). Every operation goes through `connectedDaemon(host)` for the
orchestrator's host (010 R12). Lane UI appears only when that daemon advertises the `lanes` capability; otherwise
the Lanes view is omitted, `l` on a Workers row shows that message instead of a picker, and action saves are refused
locally with `the daemon on <host> predates lanes — update with u` (FR-318a; `u` on the Sandboxes list updates every
connected daemon, matching today's `errTooOld` text). `CheckLaneChange` is only ever called against a daemon with
the capability; against an older daemon a detach goes ahead as today. If the capability disappears while the Lanes
view is showing (the daemon was replaced by an older one), the detail falls back to the Workers view.

The attach and launch lane steps also need the capability on the **target host's** daemon (an older owning daemon
drops the attach ticket, research R11); without it the step is skipped and the status reads
`lanes need the daemon on <host> updated — attaching to the open lane`.

The lane list, lane form, delete confirm and picker are one self-contained `lanesPanel` carrying its own target and
op state. It reads the orchestrator detail through a getter on its host screen (`func() *pb.OrchestratorDetail`), so
the host's reloads are seen at once and the panel never holds a stale copy. The orchestrator detail hosts it as the
Lanes view; the Jobs screen hosts it as the Manage lanes dialog over the action form. While the panel is open, its
host screen routes keys to it first; `update` returns `handled bool`, and unhandled keys (`←`/`→`, `tab`, `q`, the
host's own shortcuts) go on to the host.

## Orchestrator detail › Lanes (FR-301–FR-305, FR-307)

Fourth view after Workers, Reasoning and Hosts; `←`/`→` cycle as today.

```text
 Workers   Reasoning   Hosts  [Lanes]
 worker tags dropped on upgrade: build-ui (#frontend) — D dismiss
> frontend     UI work on the big boxes
    members: w-alpha, w-beta        actions: build-ui (exclusive), lint (inclusive)
  reviewers
    members: (none)                 actions: review-pr (inclusive)
```

| Key | Effect |
|-----|--------|
| `a` | lane form (create): Name, Description |
| `e` | lane form (rename/describe), pre-filled |
| `w` | member picker for the lane (below) |
| `d` | delete: `CheckLaneChange{delete_lane}` → confirm listing workers, actions with mode, actions left un-laned (and, if the guard fires, "no open-lane worker will be left for: …"), and "N queued runs will be re-routed"; an unused lane gets a plain confirm; `y` sends `DeleteLane` with the shown guard list |
| `D` | dismiss the dropped-tag notice (`DismissDroppedWorkerTags`) |
| `r` | reload |
| empty view | `no lanes yet — a to add one` |

Lane form errors (daemon text) show under the form as today; the form stays open.

## Lane picker overlay (shared)

One overlay type, `lanePicker`, with four uses. Rows toggle with `space`; `enter` saves; `esc` closes without saving;
`up/down/k/j` move. It records the value it opened with and sends it as the expected value.

| Use | Opened by | Rows | Save |
|-----|-----------|------|------|
| Worker's lanes (FR-306) | `l` on a Workers row (not reasoning: status `reasoning sandbox — no lanes`) | the orchestrator's lanes; header shows `none ticked = open lane` | `SetWorkerLanes` |
| Lane's members (FR-307) | `w` on a Lanes row | non-reasoning workers, `name @ host` | `SetLaneMembers` |
| Attach (FR-306a) | after choosing an orchestrator with `o` on the sandbox list, when it has lanes and both daemons have the capability | lanes | passed as `AttachWorker.lane_ids` |
| Launch (FR-306a) | after choosing an orchestrator with `O` in the launch wizard, when it has lanes and both daemons have the capability | lanes | passed to `GetWorkerBundle.lane_ids` with `host_id` = `daemonHostID(target)`; the wizard title shows `orchestrator: X (lanes: a, b)` |

- With no lanes on the orchestrator, the worker picker shows `no lanes yet — a to add one`; `a` opens the lane form,
  and the picker reopens with the new lane after it saves (FR-306). Attach and launch skip the step.
- **Launch**: the bundle (and so the ticket) is fetched **before the wizard closes**; a refusal (`NotFound` `lane "<id>"
  no longer exists`, `InvalidArgument` "unknown host") keeps the wizard open on the lane step with the daemon's
  message and the picker reloaded. Changing the target host with `H` clears the chosen orchestrator and lanes, since
  the ticket is bound to the host.
- **Attach**: `NotFound` for a lane reopens the picker reloaded, with the daemon's message; nothing is attached.
- Rows show `sanitizeLine(name)` cut with `fitCol`; the picker scrolls (no cap on the number of lanes it can show).
- **Guard (FR-308a)**: before saving, the picker calls `CheckLaneChange`. If the impact lists un-laned work, it opens
  one confirm: `No open-lane worker will be left to run: build, deploy (and 2 queued runs). Save anyway?` (each name
  sanitised; more than 5 names end `, and N more`) `y` saves
  with that list as `guard`. If the daemon still refuses (`FailedPrecondition` with a new list), a second confirm
  shows the new list.
- **Stale (FR-307a)**: `Aborted` → the picker reloads from the fresh detail, keeps the cursor on the same row id, and
  shows the daemon's message.

## Workers view (FR-306, FR-309, L5)

Row: today's cells, then the lane cell, cut to the remaining width with `fitCol`: `lanes: frontend, reviewers` |
`open lane` | `⚠ lanes pending` | `detaching…`. Reasoning rows show no lane cell. New key `l` (lanes; only with the
capability). Help gains `l lanes`.

**Detach `x`** through the orchestrator: `CheckLaneChange{detach_worker}` first; the existing detach confirm adds
the guard line when it fires, and `y` sends `DetachWorker` with that guard list.

## Sandbox list (FR-308b, FR-309)

- Third line (`thirdLine`): `… · orchestrator: pdev-orch · lanes: frontend, reviewers` | `· open lane` |
  `· lanes pending`, from `SandboxController.lane_names`/`lanes_pending`, shown only when `lanes_known` is set (no
  lane text otherwise); reasoning and non-workers unchanged. The MCP and orchestrator names on this line are now
  sanitised too.
- **Detach through the orchestrator** (`o` on a worker whose orchestrator daemon **is** connected): guarded like `x`
  on the Workers view. `CheckLaneChange{detach_worker}` runs first, its list is folded into the existing detach
  confirm, and `y` sends `DetachWorker` with that guard list; a refusal with a new list reopens the confirm.
- **Direct detach** (`o` on a worker whose orchestrator daemon is not connected): the confirm adds, from the client's
  last-known orchestrator detail and actions,
  `Warning: this may leave no open-lane worker for: build, deploy` (sanitised, capped as above) when that data shows
  it would, or
  `Cannot check open-lane workers: pdev-orch's daemon is not connected` when the client has none.
- `events_dropped` on a host's stream → reload that host's sandbox list.

## Action form (FR-316–FR-318)

Fields (top to bottom): Name, Description, Kind, Body, **Lanes** (MultiSelect of lane names, hidden for `reason`),
**Mode** (Select `exclusive — only these lanes` / `inclusive — these lanes, else open-lane workers`, preselected
exclusive, hidden when no lane is ticked or for `reason`), Arguments. The worker-tag field is gone.

- `ctrl+l` (help: `ctrl+l manage lanes`) opens **Manage lanes**: the `lanesPanel` as a dialog on the Jobs screen
  (where the action form lives) for the same orchestrator, with the same keys. Its changes are saved as made. `esc` closes it and returns to the form with every value intact;
  lane options are rebuilt by id, a deleted lane's tick is dropped, a renamed lane shows its new name (FR-317).
- Switching Kind to `reason` clears the ticks and sets the form status `lanes cleared: reason actions have no lanes`.
- Save sends `PutAction{sets_lanes: true}`. If the action ends up un-laned, it first calls `CheckLaneChange{put_action}`
  and confirms as above.
- `NotFound` for a lane (FR-316a): the form stays open with every value, reloads its lane options, drops ticks for
  lanes that no longer exist, and shows the daemon's message.

## Jobs (FR-315, FR-318b, FR-320)

- **Actions** rows (`actionLine`): `name (prompt) description  ⇢ frontend, reviewers · exclusive  {args}`; no lanes →
  no lane cell. The `#tag` badge is gone. A dropped-tag notice line appears under the orchestrator's heading, with
  `D` to dismiss.
- **Overview › Queued** and **Queue** rows (`jobsRunLine`, QUEUED branch): the waiting reason is appended in the
  remaining width, dimmed and sanitised, e.g. `#3 build-ui  developer  received 2m ago  · waiting for a worker in
  frontend`. A `+` marks truncation; `enter` (the run prompt overlay) shows the full reason.

## Form guard (FR-323)

If SWI-9's implementation is on `main` when this is implemented, the lane form, the lane picker overlay and Manage
lanes register as guarded surfaces (`guardSurface`), with baselines taken when they open. Otherwise they close on
`esc` like today's forms. As of this plan it is not: `ui/formguard.go` does not exist on `main`.
