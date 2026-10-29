package agent

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Orphans (SWI-17, research R14). The agent survives a daemon restart
// (agentCommand), and the restarted daemon has no handle on it: it can neither
// type into it nor read its screen. Such an agent is an orphan. Whether a
// sandbox has one is decided by evidence: a probe of the sandbox (one
// read-only `sbx exec`) for the process the launch command recorded in
// ~/.sxb/agent.pid, alive with the recorded start time. The daemon only
// detects and surfaces orphans; it never stops one on its own. Every spawn
// asks the OrphanGate first (Registry.Session), so none starts beside one.

// OrphanGate decides whether the sandbox's agent may be spawned now. Admit
// never blocks: the terminal registry calls Session under its own lock. It
// refuses with an ErrOrphan error beside an orphan (or while one is being
// checked for) and with ErrBusy while a lifecycle operation holds the
// sandbox's runtime lock. On success the caller holds that lock until release
// is called, which Session does once the factory's cmd.Start has returned and
// the session is registered, so no stop can slip in between.
type OrphanGate interface {
	Admit(sandboxID string) (release func(), err error)
}

// ErrOrphan is what every gate refusal beside a detached agent matches
// (errors.Is); its text is the refusal the developer sees.
var ErrOrphan = errors.New("detached agent")

type orphanErr string

func (e orphanErr) Error() string        { return string(e) }
func (e orphanErr) Is(target error) bool { return target == ErrOrphan }

// The gate's refusals (tasks.md Deferred items: plain notes). The TUI matches
// both texts.
var (
	ErrDetached = error(orphanErr("agent detached by a daemon restart"))
	ErrChecking = error(orphanErr("checking for a detached agent"))
)

// ErrBusy refuses a developer request while a lifecycle operation (stop,
// restart, refresh, kit attach, destroy) holds the sandbox's runtime lock
// (generic text, tasks.md Deferred items).
var ErrBusy = errors.New("sandbox is busy; try again")

// agentFlag is the fixed flag every agent the daemon launches starts with:
// the probe's fallback recognises an agent launched before agent.pid existed
// by it (the spec's extra args may have changed since).
const agentFlag = "--dangerously-skip-permissions"

// pidRecord is the launch prefix that records the agent's generation, PID,
// start time and PTY before `exec` (research R14): `exec` keeps all three,
// bash's comm has no spaces (so field 22 of its stat is the start time), and
// the write is best effort (`;`), so an agent still starts if it fails.
func pidRecord(gen int64) string {
	return fmt.Sprintf(`mkdir -p ~/.sxb 2>/dev/null; echo "%d $$ $(cut -d' ' -f22 /proc/$$/stat) $(tty)" > ~/.sxb/agent.pid 2>/dev/null; `, gen)
}

// probeDetect is the shell (POSIX sh) both scripts share. It prints what it
// read and leaves the live matched processes in T as "pid:start" words:
//   - agent <exe>: the agent binary (readlink -f "$(command -v claude)"). A
//     process is the agent when its executable is in that binary's directory
//     (an auto-update repoints claude to a sibling version while a running
//     agent keeps the old one, possibly deleted), argv0 is claude and the
//     fixed flag follows;
//   - ref <ppid> <exe>: the probe shell's parent, the reference for the exec
//     path a first bind compares against (research R13);
//   - file <gen> <pid> <start>, then proc … for that process only if
//     /proc/<pid>/stat still has the recorded start time (a reused PID never
//     matches) and it is the agent binary with the fixed flags;
//   - otherwise (no parseable agent.pid, or one naming a dead process, or one
//     naming a live process that is not the agent: the file is writable
//     inside the sandbox, so a forged record never keeps a probe alive or
//     steers a signal), nofile, then proc … for every process whose
//     executable is the agent binary and whose command line starts with the
//     fixed flags, except those whose parent is such a process (nested ones).
//
// It fails closed (exits non-zero: the probe failed, never "gone") when it
// cannot resolve the agent binary, or when the scan finds nothing while
// agent.pid names a live process with its recorded start time that it cannot
// identify as the agent. The reap (sxb_reap set) is the developer's explicit
// stop, so it does neither: with no agent binary it signals nothing and
// clears the sandbox, and it also signals a live agent.pid process it cannot
// identify by binary (an npm install runs claude under node) when that
// process's command line carries the fixed flag. The start time is read after the last ")" of the
// stat line, so a comm with spaces never shifts the fields; tabs and line
// breaks in a command line are blanked, so it cannot inject output lines.
const probeDetect = `u=$(printf '\037')
a=$(command -v claude 2>/dev/null) && a=$(readlink -f "$a" 2>/dev/null) || a=
[ -n "$a" ] || [ -n "$sxb_reap" ] || { echo 'probe: no claude binary in the sandbox' >&2; exit 1; }
st() { sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | cut -d' ' -f20; }
pp() { sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | cut -d' ' -f2; }
cl() { tr '\000\t\n\r' '\037   ' < "/proc/$1/cmdline" 2>/dev/null; }
isagent() {
  [ -n "$a" ] || return 1
  e=$(readlink "/proc/$1/exe" 2>/dev/null); e=${e% (deleted)}
  [ -n "$e" ] && [ "${e%/*}" = "${a%/*}" ] || return 1
  c=$(cl "$1"); a0=${c%%"$u"*}; r=${c#*"$u"}; a1=${r%%"$u"*}
  [ "${a0##*/}" = claude ] && [ "$a1" = ` + agentFlag + ` ]
}
show() { sp=$(pp "$1"); printf 'proc\t%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$(st "$1")" "$sp" "$(readlink "/proc/$1/exe" 2>/dev/null)" "$(readlink "/proc/$sp/exe" 2>/dev/null)" "$(cl "$1")"; }
printf 'agent\t%s\n' "$a"
printf 'ref\t%s\t%s\n' "$PPID" "$(readlink "/proc/$PPID/exe" 2>/dev/null)"
T=
g= p= s=
read -r g p s _ < "$HOME/.sxb/agent.pid" 2>/dev/null
f=
case "$g:$p:$s" in
  *[!0-9:]*|:*|*::*|*:) ;;
  *) if [ "$(st "$p")" != "$s" ]; then f=gone; elif isagent "$p"; then f=live; else f=other; fi ;;
esac
if [ "$f" = live ]; then
  printf 'file\t%s\t%s\t%s\n' "$g" "$p" "$s"
  show "$p"; T="$p:$s"
else
  printf 'nofile\n'
  m=
  for d in /proc/[0-9]*; do q=${d#/proc/}; isagent "$q" && m="$m $q"; done
  for q in $m; do
    case " $m " in *" $(pp "$q") "*) continue ;; esac
    t=$(st "$q"); [ -n "$t" ] || continue
    show "$q"; T="$T $q:$t"
  done
  if [ "$f" = other ] && [ -n "$sxb_reap" ]; then
    case "$u$(cl "$p")" in *"$u"` + agentFlag + `"$u"*) show "$p"; T="$T $p:$s" ;; esac
  elif [ "$f" = other ] && [ -z "$T" ]; then echo "probe: agent.pid names live process $p, not the agent" >&2; exit 1; fi
fi
`

// ProbeScript is the read-only probe: one `sbx exec <ref> bash -lc` (T047),
// the launch's login shell, so `claude` resolves on the same PATH.
func ProbeScript() string { return "sxb_reap=\n" + probeDetect }

// ReapScript is the developer's stop of an orphan (ReapOrphan): the same
// detection in the same script, so the start-time and binary checks guard
// every signal; TERM to each live matched process, up to wait for them to go,
// then KILL to any still alive with its recorded start time. It reports the
// processes it signalled (`reaped <n>`, 0 when every one was already gone).
func ReapScript(wait time.Duration) string {
	secs := int(wait / time.Second)
	if secs < 1 {
		secs = 1
	}
	return "sxb_reap=1\n" + probeDetect + fmt.Sprintf(`n=0
live() { [ "$(st "${1%%%%:*}")" = "${1#*:}" ]; }
for t in $T; do if live "$t"; then kill -TERM "${t%%%%:*}" 2>/dev/null; n=$((n+1)); fi; done
i=0
while [ $i -lt %d ]; do
  left=0; for t in $T; do live "$t" && left=1; done
  [ $left = 0 ] && break
  sleep 1; i=$((i+1))
done
for t in $T; do live "$t" && kill -KILL "${t%%%%:*}" 2>/dev/null; done
printf 'reaped\t%%d\n' "$n"
`, secs)
}

// ProbedProc is one live agent process a probe matched.
type ProbedProc struct {
	PID     int
	Start   string
	PPID    int
	Exe     string
	PPIDExe string
	Cmdline []string
}

// ProbeResult is what a probe (or a reap) read in the sandbox.
type ProbeResult struct {
	AgentExe   string
	RefPPID    int
	RefPPIDExe string
	// File is set when agent.pid parsed; FileGen/FilePID/FileStart are what it
	// records.
	File      bool
	FileGen   int64
	FilePID   int
	FileStart string
	// Procs are the live matched agent processes (none: the agent is gone).
	Procs []ProbedProc
	// Reaped is the number of processes a reap signalled.
	Reaped int
}

// Alive reports whether the probe found a live agent process.
func (r ProbeResult) Alive() bool { return len(r.Procs) > 0 }

// Main is the agent.pid process as BindFromProbe takes it, when agent.pid
// parsed and names a live process.
func (r ProbeResult) Main() (ProbedAgent, bool) {
	if !r.File || len(r.Procs) == 0 {
		return ProbedAgent{}, false
	}
	p := r.Procs[0]
	return ProbedAgent{
		Gen: r.FileGen, PID: p.PID, Start: p.Start, Exe: p.Exe, AgentExe: r.AgentExe, Cmdline: p.Cmdline,
		PPID: p.PPID, PPIDExe: p.PPIDExe, RefPPID: r.RefPPID, RefPPIDExe: r.RefPPIDExe,
	}, true
}

// ParseProbe reads a probe's (or reap's) output. Output with neither a file
// nor a nofile line, or a probe's with no agent binary, is an error: the script
// did not run to its decision, and a failed probe is never "gone". A reap's
// output may have no agent binary (it runs to its end without one).
func ParseProbe(out string) (ProbeResult, error) {
	var r ProbeResult
	decided, reaped := false, false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		switch f[0] {
		case "agent":
			if len(f) > 1 {
				r.AgentExe = f[1]
			}
		case "ref":
			if len(f) > 2 {
				r.RefPPID, _ = strconv.Atoi(f[1])
				r.RefPPIDExe = f[2]
			}
		case "file":
			if len(f) < 4 {
				return r, fmt.Errorf("probe: malformed line %q", line)
			}
			r.File, decided = true, true
			r.FileGen, _ = strconv.ParseInt(f[1], 10, 64)
			r.FilePID, _ = strconv.Atoi(f[2])
			r.FileStart = f[3]
		case "nofile":
			decided = true
		case "proc":
			if len(f) < 7 {
				return r, fmt.Errorf("probe: malformed line %q", line)
			}
			p := ProbedProc{Start: f[2], Exe: f[4], PPIDExe: f[5]}
			p.PID, _ = strconv.Atoi(f[1])
			p.PPID, _ = strconv.Atoi(f[3])
			if f[6] != "" {
				p.Cmdline = strings.Split(strings.TrimRight(f[6], "\x1f"), "\x1f")
			}
			if p.PID > 0 && p.Start != "" {
				r.Procs = append(r.Procs, p)
			}
		case "reaped":
			reaped = true
			if len(f) > 1 {
				r.Reaped, _ = strconv.Atoi(f[1])
			}
		}
	}
	if !decided {
		return r, errors.New("probe: no answer from the sandbox")
	}
	if r.AgentExe == "" && !reaped {
		return r, errors.New("probe: no agent binary in the sandbox")
	}
	return r, nil
}
