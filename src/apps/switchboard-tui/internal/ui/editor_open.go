package ui

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/editor"
)

// Open in editor (SWI-24, FR-510..FR-523; contracts/editor-launch.md). `v`
// opens the sandbox's controlled workspace folder in the editor chosen on the
// settings screen. Every check that needs no path runs on the update loop
// before the daemon call (FR-526); the executable lookup, the daemon call, the
// path checks and the launch run inside the command. The outcome arrives as
// one message that clears only this row's spinner and never reloads; a
// non-zero exit within 3 s arrives as a second (FR-523).

// editorOutcomeMsg is one `v`'s outcome: the assembled, sanitised text, whether
// it failed (error style, held like any `v` failure), and for a launch the
// reaper's channel with what the exit report needs (data-model §7).
type editorOutcomeMsg struct {
	sandboxID string
	text      string
	failed    bool
	wait      <-chan editor.ExitReport
	exe       string // Plan.Executable cut to 60 columns
	where     string // the override variable, or the settings-screen phrase
}

// editorExitMsg is FR-523's report: status and style only.
type editorExitMsg struct {
	sandboxID string
	text      string
}

const settingsWhere = "the Custom command on the settings screen (,)"

// lookPath finds the executable (exec.LookPath); tests stub it, since the
// editors aren't installed where the tests run.
var lookPath = exec.LookPath

// withNotices appends FR-529's notices as ` (a; b)`.
func withNotices(text string, notices []string) string {
	if len(notices) == 0 {
		return text
	}
	return text + " (" + strings.Join(notices, "; ") + ")"
}

// innermost is the OS reason in a launch or lookup error (FR-518): the error
// reached through errors.Unwrap until none is left.
func innermost(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// hostLookupText is FR-520's refusal, the error cut in the middle to 78
// columns because these errors put the path first and the reason last.
func hostLookupText(n, errText string) string {
	return "can't open " + n + ": host entry unavailable (" + cutMiddle(errText, hostErrCols) + ") — check the hosts file"
}

// startFailedText is FR-518's message for a command that can't be started:
// the executable (cut in the middle), the OS reason (cut to 40) and where to
// fix it. The wording is the contract's shortened form (§4): the sandbox's id
// is left out and the reminder is `(or use Custom)`, so the tightest case (a
// 60-column executable, a 40-column reason, SWITCHBOARD_CODE_INSIDERS_BIN and
// a 60-column unknown-editor notice) fits in 4 lines at 80 columns (T028).
func startFailedText(e editor.Editor, exe string, err error) string {
	lead := "can't start " + cutMiddle(exe, valueCols) + ": " + cutEnd(innermost(err).Error(), osReasonCols)
	if e.ID == editor.Custom {
		return lead + " — fix " + settingsWhere
	}
	return lead + " — set " + e.EnvVar + " to one executable (or use Custom)"
}

// refusalText maps a typed refusal to its status text (contract §4).
func refusalText(n string, e editor.Editor, ref *editor.Refusal) string {
	const fix = "; fix it on the settings screen (,)"
	switch ref.Rule {
	case editor.RuleHostLookup:
		return hostLookupText(n, ref.Detail)
	case editor.RuleNoRemote:
		return e.Name + " can't open folders on remote hosts — switch editors on the settings screen (,)"
	case editor.RuleEmpty:
		return "no Custom command is set — set one on the settings screen (,)"
	case editor.RuleEmptyExecutable:
		return "the Custom command has an empty first argument — fix it on the settings screen (,)"
	case editor.RuleMarkerInExecutable:
		return "the Custom command's first argument contains a marker — the program can't be {path} or {host}" + fix
	case editor.RuleAssignment:
		return "the Custom command starts with NAME=value, which nothing sets — use a wrapper script" + fix
	case editor.RuleShellSyntax:
		return "the Custom command doesn't run in a shell, so $VAR, backticks and unquoted | ; & < > aren't allowed — use sh -c or a wrapper script as the note shows" + fix
	case editor.RuleUnbalancedQuotes:
		return "the Custom command has an unbalanced quote — fix it on the settings screen (,)"
	case editor.RuleTrailingBackslash:
		return "the Custom command ends in a backslash — fix it on the settings screen (,)"
	case editor.RuleControlCharacter:
		return "the Custom command contains a control character — re-enter it on the settings screen (,)"
	case editor.RuleNoHome:
		return "the Custom command uses ~ but the home directory can't be determined — set HOME or use a full path"
	case editor.RuleTargetDash:
		return "the host's SSH target starts with '-' and can't be passed to an editor — fix the host entry"
	case editor.RuleZedTarget:
		return "the host's SSH target can't be used in an ssh:// address — fix the host entry"
	case editor.RulePathUnsafe:
		return "the workspace path from the daemon isn't absolute or contains control characters — fix it on the daemon"
	case editor.RulePathLimited:
		if e.ID == editor.Custom {
			return "the workspace path contains " + ref.Detail + ", which isn't allowed when Custom opens a remote sandbox — move the workspace on the daemon or switch editors on the settings screen (,)"
		}
		return "the workspace path contains " + ref.Detail + ", which isn't allowed on a remote sandbox while " + ref.Where + " is set — move the workspace on the daemon or unset " + ref.Where
	}
	return "can't open " + n + ": " + ref.Rule + " — fix it on the settings screen (,)"
}

// exitReportText is FR-523's report.
func exitReportText(n, exe, where string, r editor.ExitReport, notices []string) string {
	text := exe + " exited with status " + strconv.Itoa(r.Status)
	if r.Signal != "" {
		text += " (" + r.Signal + ")"
	}
	return withNotices(text+" after opening "+n+" — check "+where, notices)
}

// openEditorCmd is `v`: the refusals that need no path return at once; the
// command does the rest and reports with editorOutcomeMsg.
func (m Model) openEditorCmd(hostID, sandboxID string) tea.Cmd {
	e, _ := m.sessionEditor()
	notices := m.vNotices()
	n := short(sandboxID)
	if m.launcher == nil {
		text := withNotices(e.Name+" opener not configured", notices)
		return func() tea.Msg { return editorOutcomeMsg{sandboxID: sandboxID, text: text, failed: true} }
	}
	target, hostErr := m.sshTargetForHost(hostID)
	home, _ := os.UserHomeDir()
	req := editor.Request{
		Editor: e, Bins: m.editorBins, Custom: m.prefs.EditorCustomCommand,
		SSHTarget: target, HostErr: hostErr, Home: home,
	}
	exe, ref := editor.Precheck(req)
	if ref != nil {
		text := withNotices(refusalText(n, e, ref), notices)
		return func() tea.Msg { return editorOutcomeMsg{sandboxID: sandboxID, text: text, failed: true} }
	}
	d := m.daemonForHost(hostID)
	launcher := m.launcher
	failure := func(text string) tea.Msg {
		return editorOutcomeMsg{sandboxID: sandboxID, text: withNotices(text, notices), failed: true}
	}
	return func() tea.Msg {
		// The executable search stats PATH entries, which a stale mount can
		// block, so it runs here and not on the update loop (FR-526).
		if _, err := lookPath(exe); err != nil {
			return failure(startFailedText(e, exe, err))
		}
		ctx, cancel := ctxTimeout()
		defer cancel()
		tgt, err := d.VSCodeTarget(ctx, sandboxID)
		if err != nil {
			return failure("can't open " + n + ": " + cutEnd(err.Error(), outsideTextCols))
		}
		req.WorkspacePath = tgt.GetWorkspacePath()
		plan, ref := editor.Resolve(req)
		if ref != nil {
			return failure(refusalText(n, e, ref))
		}
		started, err := launcher.Launch(plan.Argv)
		if err != nil {
			return failure(startFailedText(e, plan.Executable, err))
		}
		all := notices
		if plan.RemoteNoHost {
			all = append(append([]string{}, notices...), "the path is on the remote host")
		}
		where := settingsWhere
		if plan.EnvVar != "" {
			where = plan.EnvVar
		}
		return editorOutcomeMsg{
			sandboxID: sandboxID, text: withNotices("opened "+n+" in "+e.Name, all),
			wait: started.Report, exe: cutMiddle(plan.Executable, valueCols), where: where,
		}
	}
}

// waitExitCmd blocks on the reaper's channel and returns the FR-523 report,
// or nil when the channel closes (a zero exit, or still running after 3 s).
// The notices are captured when the outcome is handled, so a later settings
// change doesn't alter a report already in flight.
func waitExitCmd(wait <-chan editor.ExitReport, sandboxID, exe, where string, notices []string) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-wait
		if !ok {
			return nil
		}
		return editorExitMsg{sandboxID: sandboxID, text: exitReportText(short(sandboxID), exe, where, r, notices)}
	}
}

// handleEditorOutcome clears only this row's spinner and rebuilds the rows (a
// row's text is built into its list item, so the spinner would stay otherwise)
// with no reload (FR-523); a completion clears m.err as statusMsg does today,
// so an earlier error never turns it red (FR-529, SC-002), and it ends any
// held failure: the newest `v` outcome replaces an older one, held or showing.
func (m Model) handleEditorOutcome(msg editorOutcomeMsg) (tea.Model, tea.Cmd) {
	delete(m.busy, msg.sandboxID)
	m.refreshListItems()
	if msg.failed {
		m.setVFail(msg.text)
		return m, m.vHoldLimitCmd()
	}
	m.vFail = vFailState{} // the error style ends with the message it styled
	m.vHold.text, m.vHold.shown = "", time.Time{}
	m.status = msg.text
	m.err = nil
	if msg.wait != nil {
		return m, waitExitCmd(msg.wait, msg.sandboxID, msg.exe, msg.where, m.vNotices())
	}
	return m, nil
}

// handleEditorExit is the FR-523 report: status and style only, no busy
// change, no reload.
func (m Model) handleEditorExit(msg editorExitMsg) (tea.Model, tea.Cmd) {
	m.setVFail(msg.text)
	return m, m.vHoldLimitCmd()
}
