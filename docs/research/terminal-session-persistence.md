# Terminal Session Persistence for Switchboard

> **Research question:** Can a daemon keep a terminal (PTY) session alive so that a
> TUI client and other terminal clients can attach/detach and resume where they left
> off — enabling "drop out and jump in" sessions without losing their place?
>
> **Context:** switchboard has a per-host Go daemon (`sxbd`) and a Bubble Tea TUI
> (`sxb`) that manages remote sandbox sessions.
>
> **Date:** 2026-07-07 · **Method:** multi-source web research (19 sources fetched,
> 24 of 25 extracted claims confirmed via 3-vote adversarial verification).

## Bottom line

**Yes — this is not only possible, it's a decades-proven pattern** (tmux/screen/dtach/
abduco), and it can be built in Go with off-the-shelf primitives. The daemon (`sxbd`)
spawns the shell under a **PTY master**, keeps that PTY alive independently of any
connected client, and fans its output out to N clients over Unix domain sockets.
Clients (`sxb` and any other terminal) attach/detach freely; the shell keeps running.

The **one design decision that matters most** is *how* "pick up where they left off"
works — and it's the part naive approaches get wrong.

---

## 1. The core model (proven, low-risk)

A daemon holds the PTY master fd; the shell runs on the slave side with the slave as
its controlling terminal. Because the shell's lifecycle is tied to the *daemon's* fd
(not any client's terminal), disconnecting a client doesn't kill anything. This is
exactly what `dtach`, `abduco`, `tmux`, and `screen` do, and it's the same mechanism
as `docker attach`.

- **dtach:** *"the program under the control of dtach would not be affected by the
  terminal being disconnected"* — detach leaves the program *"running in the
  background."*
- **abduco:** programs *"run independently from their controlling terminal … detached
  … and then later reattached."*

**Verdict: high confidence, well-trodden. The persistence half is the easy half.**

## 2. Go building blocks — `creack/pty`

The de-facto Go primitive (used by Docker and gotty). The daemon needs:

- `pty.Start(cmd)` — assigns a PTY tty to the shell's stdin/stdout/stderr, **starts it
  in a new session, sets the controlling terminal**, and returns the master `*os.File`.
  Read/write that file to drive the shell.
- `pty.Open()` — returns the master/slave pair directly for finer control.
- **Resize:** `pty.Setsize(f, ws)`, `pty.StartWithSize(cmd, ws)`, `Winsize{Rows, Cols,
  X, Y}`, and the `TIOCSWINSZ` ioctl constant. Setting the size via `TIOCSWINSZ` also
  raises `SIGWINCH` in the child so it redraws.

> **Key constraint:** the PTY master holds exactly ONE window size. You cannot literally
> satisfy multiple clients of different sizes — you push a single *reconciled* dimension
> (see §5).

## 3. Streaming to clients — transport is the trivial part

The web tools (GoTTY, ttyd) just relay PTY output over WebSocket and forward input back,
rendering via xterm.js. **But note the trap:** GoTTY *"starts a new process … when a new
client connects … users cannot share a single terminal by default"* — it delegates
sharing to tmux. In other words, **the transport is trivial; the session-persistence +
shared-attach logic must live in `sxbd` itself.** Don't expect a streaming library to
give you the hard part.

> ⚠️ A claim that ttyd supports bounded multi-client shared attach via `--max-clients`
> was **refuted** (0-3 in verification). Web-relay tools generally lack native shared
> attach. Don't design around it.

For this architecture, a **framed protocol over a Unix domain socket** (dtach/rtach/
abduco style) is the natural fit — one daemon + one socket per session. gRPC
bidi-streaming or WebSocket are viable but heavier; UDS keeps it local and simple.

## 4. "Resume where they left off" — the decision that matters

This is where approaches diverge sharply:

| Approach | On reattach | Example |
|---|---|---|
| **Bare `dtach`** | **Blank screen** — no buffer at all | `dtach` |
| **Raw byte ring buffer** | Replays recent PTY bytes; paginated scrollback | `rtach` (configurable ring buffer, e.g. 4MB) |
| **VT screen-state snapshot** ✅ | Feeds PTY output into a headless VT emulator that holds screen state + scrollback; sends a **reconstructed snapshot** | `pilotty` (vt100 crate), `zmx` (libghostty-vt) |

**Recommendation: the VT-snapshot approach.** It yields a *correct current-screen redraw
regardless of where in the byte stream the client reconnects* — no guessing how many
bytes to replay, and no replaying half of an escape sequence. `zmx`'s model is the
clearest blueprint: *"daemon sends pty output to client AND ghostty-vt; ghostty-vt holds
terminal state and scrollback; on reattach ghostty-vt sends terminal snapshot to client
stdout."*

**The Go substitute for the VT layer: [`hinshun/vt10x`](https://github.com/hinshun/vt10x)**
— a pure-Go headless VT100/xterm emulation backend *explicitly built "for terminal
muxing."* Its `Terminal` is an `io.Writer` (just write PTY bytes into it), and its `View`
interface exposes `Cell(x,y)`, `Size()`, `Cursor()`, `CursorVisible()`, `Title()`,
`Mode()` — everything needed to reconstruct a redraw. Direct analog of pilotty's Rust
vt100 crate / zmx's libghostty-vt.

> ⚠️ **Caveat:** `vt10x` is small/low-activity (~47 stars). Real maintenance risk —
> evaluate vendoring it, or budget for maintaining a fork. This is the single biggest
> technical risk in the Go path.

Optionally keep a **byte ring buffer** *in addition* to the VT snapshot for true long
scrollback history (rtach-style paginated retrieval), while the VT snapshot handles the
current-screen redraw.

## 5. The multi-viewer resize problem — pick a policy explicitly

With multiple clients of different sizes on one PTY, there is **no single correct answer**
— choose and document one:

- **`dtach`:** broken by design (*"you will likely encounter problems if your terminals
  have different window sizes"*).
- **`abduco` / `rtach`:** **most-recently-active client controls the size** (abduco
  applies `TIOCSWINSZ` only for the non-read-only list-head client).
- **`tmux`:** configurable — `smallest` (default, safest for correctness) / `largest` /
  `latest` / `manual`.

`rtach`'s protocol is a good template: a `winch` packet (`rows/cols/xpixel/ypixel`), a
16-byte **client ID**, and a `claim_active` packet marking which client currently drives
size + input. **Recommendation:** default to `smallest` for correctness, or
`latest`/active-client for single-driver ergonomics, and support **read-only viewers**
for observers.

## 6. Concrete architecture for `sxbd` + Bubble Tea `sxb`

```
                    sxbd (per host)
   ┌─────────────────────────────────────────────┐
   │  session:                                    │
   │    creack/pty master ──► shell (slave/ctty)  │
   │         │                                    │
   │         ├──► vt10x emulator (screen+scrollback state)
   │         │        (snapshot source on attach) │
   │         └──► [optional byte ring buffer]     │
   │         │                                    │
   │    UDS listener ──► fan-out to N clients      │
   │    resize arbiter (smallest | active-client)  │
   └─────────────────────────────────────────────┘
        ▲ attach/detach        ▲ attach (read-only)
     sxb (Bubble Tea)      other terminal / observer
```

**Attach flow:** client connects → daemon sends VT snapshot (full redraw) → client
streams live PTY output + sends input + `winch`. **Detach:** client disconnects; shell +
PTY + VT state persist.

**Bubble Tea integration note:** decide how sxb's own render loop coexists with receiving
a raw VT snapshot. Simplest is to treat the daemon connection as an inner "terminal
viewport" — render the snapshot as a full redraw on attach, then apply incremental PTY
output. Bubble Tea's `WindowSizeMsg` maps to the `winch` packet sent upstream.

## Prior art worth studying (by transferability)

- **`zmx`** (neurosnap) — closest match: daemon + UDS + VT-state rehydration +
  multiplayer. Study its snapshot model. *(Zig/libghostty — architecture transfers, code
  doesn't.)*
- **`rtach`** (eriklangille) — modern dtach replacement; **its framed protocol**
  (winch/client-id/claim_active/scrollback-page) is the best wire-protocol template.
  *(Zig.)*
- **`pilotty`** — daemon lifecycle patterns: auto-start on first command, auto-shutdown
  after idle. *(Rust.)*
- **`abduco` / `dtach`** — the battle-tested, minimal reference implementations of the
  core model.
- **`creack/pty` + `hinshun/vt10x`** — the actual Go dependencies.

---

## Gaps & open questions

The verification pass surfaced **one under-evidenced area that is central to
switchboard**: how PTY persistence interacts with shells running **inside remote
sandboxes/containers or over SSH** (Docker/containerd attach, tmux control mode, Teleport
`tsh` internals weren't verified). The critical unanswered design question:

> **Where does the PTY-persistence boundary sit relative to the sandbox?**
>
> - **(A)** `sxbd` keeps the PTY alive *on the host* and streams into the sandbox via
>   `ssh`/`docker exec`. Simpler daemon placement, but if the SSH/exec transport drops,
>   the shell inside the sandbox may die even though the daemon persists.
> - **(B)** A daemon runs *inside* the sandbox/container itself, owning the PTY next to
>   the shell. Survives host-side transport drops, but requires deploying the daemon into
>   every sandbox and a nested attach path.

This is the highest-value thing to prototype next, because it determines whether "jump
back in" survives a dropped SSH connection or a flaky network — arguably the whole point
of the feature. Suggested spike: try both (A) and (B) with a deliberately killed
transport to see which actually preserves the session.

Other open questions:

- Exact wire protocol (framed-UDS vs gRPC vs WebSocket).
- Whether true scrollback history is needed in addition to the VT snapshot.
- Per-session memory bounds on a shared host daemon.
- How Bubble Tea's input/render loop reconciles with a full VT snapshot on attach
  (full redraw vs. incremental).

---

## Sources

Primary sources (project repos / official docs), all passing 3-0 adversarial
verification unless noted:

| Source | Angle |
|---|---|
| [sjl/dtach](https://github.com/sjl/dtach) | fundamentals / systems internals |
| [martanne/abduco](https://github.com/martanne/abduco) | fundamentals / systems internals |
| [eriklangille/rtach](https://github.com/eriklangille/rtach) | fundamentals / protocol design |
| [creack/pty](https://github.com/creack/pty) · [godoc](https://pkg.go.dev/github.com/creack/pty) | Go implementation |
| [yudai/gotty](https://github.com/yudai/gotty) | streaming / multi-client |
| [tsl0922/ttyd](https://github.com/tsl0922/ttyd) | streaming / multi-client (max-clients claim **refuted**) |
| [msmps/pilotty](https://github.com/msmps/pilotty) | buffering / replay / VT emulation |
| [neurosnap/zmx](https://github.com/neurosnap/zmx) | prior art (closest match) |
| [hinshun/vt10x](https://github.com/hinshun/vt10x) · [godoc](https://pkg.go.dev/github.com/hinshun/vt10x) | Go VT emulation backend |
| [tmux/tmux wiki](https://github.com/tmux/tmux/wiki) | multi-client resize policy |
| [smithersai/zmux](https://github.com/smithersai/zmux) | prior art (daemon + JSON-RPC UDS) |

**Confidence caveats:** rtach (Zig), pilotty (Rust), and zmx (Zig/libghostty) are
cross-language — architectures transfer, code does not; the Go VT layer is `hinshun/vt10x`,
which carries maintenance risk (~47 stars). rtach/pilotty/zmx are recent (2025–2026) and
unproven at scale vs. the battle-tested tmux/screen/abduco/dtach. The multi-viewer resize
problem has no single correct answer. The sandbox/SSH interaction (item 5) produced no
verified claims and remains under-evidenced.
