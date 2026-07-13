# Phase 0 Research: Terminal Session Persistence & Sandbox Tags

> **Implementation update (T001/T002 resolved during /speckit-implement):**
> **VT library decision — NONE adopted daemon-side.** `charmbracelet/x/vt` was
> evaluated first (per R2) but its only available version is an unstable
> pseudo-version that `go doc` flags with typecheck errors and that **hangs at
> runtime** when fed input. Rather than adopt a hanging dependency (or the also
> low-activity `hinshun/vt10x`), the daemon uses **raw byte-ring replay** (the
> rtach model from R1): it keeps a bounded ring of raw PTY bytes and replays them
> on attach; the client's **real terminal** performs VT interpretation natively,
> so no VT-emulation dependency is needed daemon-side. A client-side VT emulator
> is only needed for the in-TUI viewport render (US2), deferred with that work.
> **T002 (exec child lifetime) — RESOLVED (verified against real `sbx` v0.33.0 /
> Docker 29.4.3, 2026-07-09).** An in-container child of `sbx exec` **survives**
> both a hard SIGKILL of the host exec client and a controlling-PTY hangup — with
> and without a `setsid` wrap (see R4 "Verification result"). `docker exec` does
> not propagate host-client death or TTY hangup into the container. Host-side
> session persistence (R4 option A) therefore satisfies FR-002/003 as-is, and
> **T019 needs no `setsid`/`nohup` wrapping** — wrapping is unnecessary for
> survival and would only risk the interactive job-control breakage the plan
> warned about, so the launch command is left unwrapped.



Feeds the plan's Technical Context. Every "NEEDS CLARIFICATION" candidate is resolved here. The
web-research foundation is `docs/research/terminal-session-persistence.md` (19 sources, adversarially
verified); this document turns those findings into switchboard-specific decisions grounded in the
existing code (`agent.Session`, `AttachAgent`, `ui/terminal.go`, the bbolt registry).

---

## R1 — Persistence & replay model: VT-snapshot broadcaster over the existing PTY

**Decision**: Keep the existing `agent.Session` PTY exactly as-is (it already spawns `sbx exec -it`
under `creack/pty` and survives detach because `Registry` only `Close()`s on explicit teardown).
Wrap it in a new daemon-side **broadcaster** that reads the PTY **once** and tees every byte into:
1. a **headless VT emulator** holding current screen state (cursor, cells, modes),
2. a **bounded scrollback ring buffer** (default 5 MB), and
3. a **fan-out** to each attached client's send channel.

On attach, the broadcaster renders the VT screen to an ANSI redraw + recent scrollback and sends it
as a one-shot **snapshot**, then streams live bytes. This is the zmx/pilotty/rtach pattern the report
recommends over bare-`dtach` byte replay, because the snapshot yields a correct current-screen redraw
regardless of where in the stream the client (re)connects.

**Rationale**: Directly fixes the two concrete gaps in today's code — (a) a single `io.Reader`
consuming PTY bytes means only one client can attach and detached bytes are lost; (b) a reconnecting
client sees nothing until the next output (the dtach "blank screen"). The broadcaster makes fan-out,
snapshot-on-attach, and "AI keeps running while detached" fall out of one mechanism.

**Alternatives considered**:
- *Raw byte ring replay only (rtach-style)*: simpler, but replaying an arbitrary byte offset can cut
  an escape sequence and can't cheaply produce "the current screen" — rejected as the primary
  redraw, though the ring is retained for scrollback history.
- *Bare dtach model (no buffer)*: rejected outright — it is exactly the blank-screen behavior the
  spec forbids (FR-003).
- *tmux/abduco as an external dependency inside each sandbox*: rejected — pulls a second session
  manager into every sandbox, contradicts owning the model in `sxbd`, and complicates the contract.

---

## R2 — VT emulator library: `charmbracelet/x/vt` (fallback `hinshun/vt10x`)

**Decision**: Use **`github.com/charmbracelet/x/vt`** as the pure-Go VT emulator, on **both** ends:
daemon-side as the screen-state model that produces the snapshot, and client-side to render raw PTY
bytes into the Bubble Tea terminal viewport. Default scrollback ring = **5 MB per session**;
snapshot = current screen (always) + up to a bounded tail of scrollback.

**Rationale**: The report names `hinshun/vt10x` as the pure-Go muxing backend but flags it as
low-activity (~47 stars) with real maintenance risk. switchboard is already deep in the Charm
ecosystem (Bubble Tea/Bubbles/Lipgloss), and Charm's `x/vt` is an actively-maintained pure-Go
terminal emulator in that same org — lower supply-chain risk and one library serving both the
daemon's screen model and the client's renderer (symmetry the report calls out as desirable). A
single VT library both ends also guarantees the snapshot the daemon computes renders identically in
the client.

**Verification note (not blocking)**: Confirm `x/vt`'s exact API for (a) writing bytes
(`io.Writer`), (b) reading back the screen grid/cursor, and (c) emitting an ANSI redraw of current
state during the first implementation task. If `x/vt` cannot emit a self-contained redraw, fall back
to `hinshun/vt10x` (whose `View` interface exposes `Cell/Size/Cursor/CursorVisible/Title/Mode`, from
which a redraw is straightforward), or synthesize the redraw from the grid. Either satisfies the
model; this is a library-selection detail with a documented fallback, not an open scope question.

**Alternatives considered**: `hinshun/vt10x` (fallback, per above); writing a minimal in-house VT
parser (rejected — VT100/xterm emulation is a large, bug-prone surface the report explicitly warns
against reinventing); cgo bindings to libghostty-vt / the Rust `vt100` crate (rejected — adds a cgo
build burden to a pure-Go workspace).

---

## R3 — Multi-viewer resize: smallest-of-attached interactive clients

**Decision**: The single PTY size is reconciled as the **minimum rows and minimum cols across all
currently-attached interactive clients** (tmux's default `smallest` policy). On every attach, detach,
or client `WindowSizeMsg`, the arbiter recomputes and issues `pty.Setsize` (TIOCSWINSZ), which also
raises `SIGWINCH` in the child so it redraws. Read-only/observer attachments (if later added) do not
influence the size.

**Rationale**: The report is explicit that this problem has no universally correct answer and MUST be
chosen deliberately. "Smallest" is the only policy that can never draw content outside a viewer's
viewport (correctness-first), which matters because the in-TUI viewer (US2) is frequently smaller
than a full external terminal window (US3) and the spec permits both attached at once. It is
deterministic and trivially testable. Since simultaneous attach is optional and usually 0–1
interactive clients are present, the "shrinks the big window" downside rarely bites.

**Alternatives considered**: *latest/active-client drives size* (abduco/rtach) — better ergonomics
for a single driver but can push content off a smaller co-viewer; deferred as a possible future
per-user option. *largest* — rejected, guarantees overflow on the smaller viewer.

---

## R4 — Persistence boundary vs. the sandbox (the report's open question)

**Decision**: Adopt **host-side persistence (option A)**: the persistent session lives in `sxbd` on
the host, owning the PTY to `sbx exec`. This fully satisfies the spec's stated guarantees for the
common cases — client detach, laptop sleep, switching machines, and the VSCode path — because the
daemon simply does not close the session when a client detaches. A full in-sandbox daemon (option B)
is **out of scope** for this feature.

To make "an AI prompt keeps running until complete after the terminal is closed" (FR-002) robust even
against a **daemon restart**, the in-sandbox agent command is launched **detached inside the
container** (e.g. wrapped with `setsid`/`nohup`-equivalent so it is not a child of the `sbx exec`
client). Feature 001's daemon **re-adoption** then re-establishes a session view after restart. The
spec already relaxes full replay across a daemon outage (edge case: "running work is never lost, but
output produced during the outage may not be fully replayable"), so this boundary is spec-compliant.

**Verification note**: During implementation, confirm `sbx exec` / `docker exec` child-process
lifetime when the exec client dies (does the in-container process survive?) and set the in-container
wrapping accordingly. This is a behavior to verify against the real `sbx`, tracked as an early task —
not an unresolved design choice.

**Verification result (T002 spike — `sbx` v0.33.0, Docker 29.4.3, sandbox `test-sandbox-3`, 2026-07-09):**
A heartbeat loop was launched inside the container via `sbx exec` and the host-side client was then
killed while the in-container log was watched for continued growth. Four cases, all runs on the real
sandbox:

| Case | Launch | Host teardown | In-container child |
|------|--------|---------------|--------------------|
| A | `sbx exec … sh -c <loop>` (no PTY) | `kill -9` client | **survived** (ran to completion; live proc observed) |
| B | `sbx exec … setsid sh -c <loop>` | `kill -9` client | **survived** |
| C | `sbx exec -it … sh -c <loop>` under a **host PTY** | `kill -9` client **+ close PTY master** (exact `ptySession.Close()` sequence) | **survived** (log kept advancing past teardown) |
| D | `sbx exec -it … setsid sh -c <loop>` under a host PTY | `kill -9` client + close PTY master | **survived** |

**Conclusion**: `sbx exec` (backed by `docker exec`) runs the process inside the container via the
Docker daemon; the host-side exec client is only an I/O relay. Killing that client — and hanging up
the host controlling PTY — does **not** deliver a signal to the in-container process, so it keeps
running regardless of any `setsid`/`nohup` wrap. `setsid` (cases B/D) changes nothing about survival.

**Decision for T019**: launch the agent command **unwrapped**. Wrapping in `setsid`/`nohup` is
unnecessary for FR-002 (the in-flight AI prompt already survives terminal-close and a daemon
restart's client-kill), and `setsid` — by stripping the controlling TTY — would risk the interactive
job-control breakage flagged in plan.md. Daemon-restart *replay* across the outage remains best-effort
(the surviving process is re-adopted by a fresh session view; output during the outage may not be
fully replayable), which the spec explicitly relaxes. Reproduction harness: `ptykill.py` (host-PTY
fork + kill + master close) in the T002 spike notes.

**Rationale**: Option A is a minimal delta on the existing exec-based design (no daemon shipped into
every sandbox, no nested attach path) and covers every user story. Option B's only advantage —
surviving a host-daemon crash with full replay — is exactly what the spec de-scopes.

**Alternatives considered**: Option B (daemon-in-sandbox) — rejected for this feature as heavy and
unnecessary given the relaxed durability requirement; recorded as future work.

---

## R5 — External terminal: `sxb attach` mode, spawn, and one-per-sandbox bring-to-front

**Decision**: Pressing **T** spawns the platform's terminal emulator running a new **`sxb attach
--host <h> --sandbox <id>`** process, which opens a full-screen attach to the persistent session.
Enforcement of "one external terminal per sandbox" is **two-layered**:
1. **Authoritative (daemon)**: `AttachAgent` tags each attachment with a `client_kind`
   (`IN_TUI` | `EXTERNAL`). The daemon rejects a **second** `EXTERNAL` attach for the same sandbox
   with a typed error. This holds even across different `sxb` processes / machines.
2. **Ergonomic (client)**: the TUI that spawned the external terminal tracks the child
   process/window per sandbox; a repeat **T** **brings that window to the foreground** instead of
   spawning a new one. If the window can't be located (closed out-of-band), it spawns a fresh one
   (spec edge case).

Terminal spawn + focus are platform-specific: macOS via `open -a <Terminal>` / AppleScript activate;
Linux via the user's `$TERMINAL` (or a detected emulator) and `wmctrl`/`xdotool` (or the emulator's
own focus) where available. Where focus tooling is absent, fall back to a clear "external terminal
already open" message.

**Rationale**: Reusing `sxb` itself as the external attach client means one renderer/protocol path
(no separate binary). The daemon-side kind check is the only place that can enforce the rule
globally (the report notes shared-attach lives in the daemon, not the transport); the client-side
focus is pure local window management and is best-effort by nature.

**Alternatives considered**: A dedicated tiny attach binary (rejected — duplicate renderer); relying
on client-only tracking (rejected — a second `sxb` process wouldn't know; the daemon must be
authoritative); embedding a terminal multiplexer (rejected — scope).

---

## R6 — Workspace→sandbox resolution & tag storage

**Decision (workspace resolution, FR-017/018)**: At launch, the daemon writes a marker
**`.switchboard/session.json`** into the root of each controlled workspace copy, containing
`{ host_id, sandbox_id, socket }`. `sxb`, on startup, walks up from `cwd` looking for that marker; if
found, it connects to that host and enters `sxb attach` for that sandbox directly (FR-017), including
from nested subdirectories (FR-018). No marker → normal TUI (FR-019). Marker present but sandbox
stopped/unknown → actionable message / fall back to TUI (FR-020). A daemon RPC
`ResolveWorkspace(path)` is included in the contract as an authoritative cross-check for hosts where
the client can't read the marker directly, but the marker walk-up is the primary, offline-capable
path.

**Decision (tag storage, FR-021–024)**: Store the tag **daemon-side in the bbolt registry**, as a new
`tag` field on the `Sandbox`, set via a new **`SetSandboxTag`** RPC — mirroring the existing
`RenameSandbox`/`display_name` pattern. This makes tags visible to any client connecting to that
daemon, persistent across TUI restarts and sandbox stop/restart, and keeps them next to the sandbox
they describe. Tags are free-text, non-unique, capped at **64 characters**, and never affect identity
or lifecycle (FR-022).

**Rationale**: `display_name` is already daemon-owned and mutable via `RenameSandbox`; a tag is the
same shape of data (mutable, per-sandbox, display-only) so it belongs in the same place for
consistency — even though the spec left storage "deferred to planning." Client-side storage (as 001
does for configs/groups/known-hosts) was considered, but those are *cross-daemon user* state; a tag
is *about one daemon's sandbox* and should travel with it, so the registry is the better home.

**Alternatives considered**: client-side TOML tag store (rejected — wouldn't survive switching
clients and duplicates the display-name decision already made daemon-side); a generic key/value
label map on the sandbox (rejected as over-engineering — the spec asks for a single purpose tag; a
scalar `tag` is enough and can grow to a map later without breaking callers).

---

## Resolved defaults summary

| Question (from spec Assumptions) | Resolution |
|---|---|
| Reconnect history scope | Current screen (always) + bounded scrollback tail; 5 MB ring per session (R1/R2) |
| VT library | `charmbracelet/x/vt`, fallback `hinshun/vt10x` (R2) |
| Multi-viewer resize policy | smallest-of-attached interactive clients (R3) |
| Persistence boundary | Host-side session in `sxbd` (option A); agent detached inside container; option B deferred (R4) |
| "One external terminal" scope | Per sandbox; daemon-authoritative via attachment `client_kind` (R5) |
| External spawn + bring-to-front | `sxb attach` in a platform terminal; client tracks & focuses; best-effort (R5) |
| Workspace detection | `.switchboard/session.json` marker walk-up, `ResolveWorkspace` RPC as cross-check (R6) |
| Tag storage | Daemon bbolt registry; `tag` field + `SetSandboxTag` RPC, mirroring `RenameSandbox` (R6) |
