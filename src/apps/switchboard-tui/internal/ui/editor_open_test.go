package ui

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/editor"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// fakeLauncher records every argv and hands out a controllable report channel.
type fakeLauncher struct {
	mu     sync.Mutex
	argv   [][]string
	report chan editor.ExitReport
	err    error
}

func (l *fakeLauncher) Launch(argv []string) (*editor.Started, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.argv = append(l.argv, argv)
	if l.err != nil {
		return nil, l.err
	}
	return &editor.Started{Report: l.report}, nil
}

func (l *fakeLauncher) last() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.argv) == 0 {
		return nil
	}
	return l.argv[len(l.argv)-1]
}

func (l *fakeLauncher) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.argv)
}

// stubLookPath makes every executable findable: the editors aren't installed
// where the tests run. realLookPath restores exec.LookPath for a test.
func stubLookPath(t *testing.T) {
	t.Helper()
	lookPath = func(name string) (string, error) { return name, nil }
	t.Cleanup(func() { lookPath = exec.LookPath })
}

func realLookPath(t *testing.T) {
	t.Helper()
	lookPath = exec.LookPath
	t.Cleanup(func() { lookPath = exec.LookPath })
}

// editorModel is a sized model with sandbox sb1 selected, a recording launcher
// and a settings store in a temp dir.
func editorModel(t *testing.T, d *fakeDaemon) (Model, *fakeLauncher, *store.Store) {
	t.Helper()
	stubLookPath(t)
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := &fakeLauncher{report: make(chan editor.ExitReport, 1)}
	sbs := []*pb.Sandbox{
		{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
		{Id: "sb2", DisplayName: "other", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
	}
	m := New(d, "/work").WithGroups(st.Groups()).WithEditor(l, map[string]string{}).WithSettings(st.Settings())
	m = withSandboxes(m, sbs)
	return m, l, st
}

// remoteEditorModel is editorModel on a saved SSH host user@box.
func remoteEditorModel(t *testing.T, d *fakeDaemon) (Model, *fakeLauncher, *store.Store) {
	t.Helper()
	m, l, st := editorModel(t, d)
	hs := st.Hosts()
	if _, err := hs.Save(store.KnownHost{ID: "remote", DisplayName: "box", Kind: "ssh", SSHTarget: "user@box"}); err != nil {
		t.Fatal(err)
	}
	return m.WithHosts(nil, hs, "remote"), l, st
}

// pressV presses v, runs its command and feeds the outcome back. It returns
// the outcome and the command the handler returned (the exit wait, or nil).
func pressV(t *testing.T, m Model) (Model, editorOutcomeMsg, tea.Cmd) {
	t.Helper()
	m, cmd := update(m, press("v"))
	if cmd == nil {
		t.Fatal("v returned no command")
	}
	msg, ok := runCmd(cmd).(editorOutcomeMsg)
	if !ok {
		t.Fatalf("v's command returned %T", runCmd(cmd))
	}
	m, next := update(m, msg)
	return m, msg, next
}

// TestOpenEditorDefaultIsVSCode (SC-002): with no settings and no variables,
// v starts `code <path>` and reports `opened sb1 in VS Code` exactly as before
// this feature; only the pressed row's spinner is cleared and nothing reloads.
func TestOpenEditorDefaultIsVSCode(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := editorModel(t, d)
	m.busy["sb2"] = "stopping"
	m, cmd := update(m, press("v"))
	if m.busy["sb1"] != "opening" {
		t.Fatalf("busy = %v", m.busy)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "opening") {
		t.Errorf("the row should show opening:\n%s", v)
	}
	msg := runCmd(cmd).(editorOutcomeMsg)
	if got := l.last(); strings.Join(got, " ") != "code /workspace" {
		t.Errorf("argv = %q, want [code /workspace]", got)
	}
	close(l.report)
	m, next := update(m, msg)
	if m.status != "opened sb1 in VS Code" || msg.failed {
		t.Errorf("status = %q (failed %v)", m.status, msg.failed)
	}
	if _, busy := m.busy["sb1"]; busy || m.busy["sb2"] != "stopping" {
		t.Errorf("only sb1's spinner clears: %v", m.busy)
	}
	if m.listLoading {
		t.Error("a completion must not reload")
	}
	if v := ansi.Strip(m.View()); strings.Contains(v, "opening") {
		t.Errorf("the row should stop showing opening:\n%s", v)
	}
	// The exit wait ends with nothing when the channel closes.
	if got := runCmd(next); got != nil {
		t.Errorf("closed report channel gave %v", got)
	}
	if m.statusIsErr() {
		t.Error("a completion is not red")
	}
	if !strings.Contains(m.renderHelp(m.listHelp()), "v vscode") {
		t.Errorf("footer should read v vscode: %q", m.renderHelp(m.listHelp()))
	}
}

func TestOpenEditorSelectedEditorAndFooter(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := editorModel(t, d)
	m.prefs.Editor = "webstorm"
	close(l.report)
	m, _, _ = pressV(t, m)
	if got := l.last(); strings.Join(got, " ") != "webstorm /workspace" {
		t.Errorf("argv = %q", got)
	}
	if m.status != "opened sb1 in WebStorm" {
		t.Errorf("status = %q", m.status)
	}
	if h := m.renderHelp(m.listHelp()); !strings.Contains(h, "v webstorm") || strings.Contains(h, "vscode") {
		t.Errorf("footer should name webstorm: %q", h)
	}
	m.prefs.Editor = "custom"
	if h := m.renderHelp(m.listHelp()); !strings.Contains(h, "v custom") {
		t.Errorf("footer should read v custom: %q", h)
	}
}

// Both early returns clear the pressed row: no launcher, and a refusal before
// the daemon call.
func TestOpenEditorEarlyReturnsClearRow(t *testing.T) {
	d := &fakeDaemon{}
	m, _, _ := editorModel(t, d)
	m.launcher = nil
	m, msg, _ := pressV(t, m)
	if !msg.failed || m.status != "VS Code opener not configured" || m.busy["sb1"] != "" || !m.statusIsErr() {
		t.Errorf("no launcher: %+v busy %v", msg, m.busy)
	}
	m.prefs.Editor = "goland"
	m, msg, _ = pressV(t, m)
	if m.status != "GoLand opener not configured" {
		t.Errorf("the name is the editor's: %q", m.status)
	}
	// A refusal that needs no path returns before VSCodeTarget is called.
	m, l, _ := remoteEditorModel(t, d)
	m.prefs.Editor = "webstorm"
	before := d.vscodeCalls
	m, msg, _ = pressV(t, m)
	if !msg.failed || m.status != "WebStorm can't open folders on remote hosts — switch editors on the settings screen (,)" {
		t.Errorf("no-remote: %+v", msg)
	}
	if d.vscodeCalls != before || l.calls() != 0 || m.busy["sb1"] != "" {
		t.Errorf("a refusal must not call the daemon or launch: calls %d launches %d busy %v", d.vscodeCalls, l.calls(), m.busy)
	}
	if !m.statusIsErr() {
		t.Error("a refusal is red")
	}
}

// An errMsg leaves m.err set; a later completion clears it so it isn't red.
func TestOpenEditorCompletionClearsErr(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := editorModel(t, d)
	m, _ = update(m, errMsg{errors.New("boom")})
	if !m.statusIsErr() {
		t.Fatal("errMsg should be red")
	}
	close(l.report)
	m, _, _ = pressV(t, m)
	if m.err != nil || m.statusIsErr() || m.status != "opened sb1 in VS Code" {
		t.Errorf("err %v status %q red %v", m.err, m.status, m.statusIsErr())
	}
}

func TestOpenEditorDaemonErrorCutThenNotice(t *testing.T) {
	d := &fakeDaemon{vscodeErr: errors.New(strings.Repeat("x", 150))}
	m, _, _ := editorModel(t, d)
	m.prefs.Editor = "future-ide"
	m, msg, _ := pressV(t, m)
	want := "can't open sb1: " + strings.Repeat("x", 119) + "… (unknown editor 'future-ide' in settings)"
	if !msg.failed || m.status != want {
		t.Errorf("status = %q\nwant       %q", m.status, want)
	}
	if !m.statusIsErr() {
		t.Error("a failure is red")
	}
}

// The FR-523 report replaces the status, is red, leaves another row's spinner
// and is never overwritten by its own completion (which precedes it).
func TestOpenEditorExitReport(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := editorModel(t, d)
	m.prefs.Editor = "goland"
	m.busy["sb2"] = "stopping"
	m, _, next := pressV(t, m)
	if m.status != "opened sb1 in GoLand" {
		t.Fatalf("status = %q", m.status)
	}
	l.report <- editor.ExitReport{Status: 3}
	exit, ok := runCmd(next).(editorExitMsg)
	if !ok {
		t.Fatalf("exit wait returned %T", runCmd(next))
	}
	m, cmd := update(m, exit)
	if cmd != nil {
		t.Error("the report returns no command")
	}
	if m.status != "goland exited with status 3 after opening sb1 — check SWITCHBOARD_GOLAND_BIN" {
		t.Errorf("status = %q", m.status)
	}
	if !m.statusIsErr() || m.busy["sb2"] != "stopping" || m.listLoading {
		t.Errorf("red %v busy %v loading %v", m.statusIsErr(), m.busy, m.listLoading)
	}
	// A signal names itself.
	if got := exitReportText("sb1", "zed", "SWITCHBOARD_ZED_BIN", editor.ExitReport{Status: -1, Signal: "terminated"}, nil); got != "zed exited with status -1 (terminated) after opening sb1 — check SWITCHBOARD_ZED_BIN" {
		t.Errorf("signal report = %q", got)
	}
}

// The unknown-editor and unreadable notices appear on every outcome kind
// (FR-529): completion, refusal, failure and exit report.
func TestOpenEditorNoticesOnEveryOutcome(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := editorModel(t, d)
	m.prefs.Editor = "future-ide"
	m.settingsView.fileUnreadable = errors.New("parse settings.toml: bad")
	const notices = " (unknown editor 'future-ide' in settings; settings couldn't be read at startup, changes aren't saved)"
	m, _, next := pressV(t, m)
	if m.status != "opened sb1 in VS Code"+notices {
		t.Errorf("completion = %q", m.status)
	}
	l.report <- editor.ExitReport{Status: 2}
	m, _ = update(m, runCmd(next))
	if !strings.HasSuffix(m.status, notices) || !strings.Contains(m.status, "exited with status 2") {
		t.Errorf("report = %q", m.status)
	}
	d.vscodeErr = errors.New("daemon down")
	m, _, _ = pressV(t, m)
	if m.status != "can't open sb1: daemon down"+notices {
		t.Errorf("failure = %q", m.status)
	}
	d.vscodeErr = nil
	rm, _, _ := remoteEditorModel(t, d)
	rm.prefs.Editor = "future-ide" // VS Code, so a remote target is fine; make it refuse with a dash
	rm.settingsView.fileUnreadable = m.settingsView.fileUnreadable
	if _, err := rm.hostStore.Save(store.KnownHost{ID: "remote", DisplayName: "box", Kind: "ssh", SSHTarget: "-oX"}); err != nil {
		t.Fatal(err)
	}
	rm, _, _ = pressV(t, rm)
	if !strings.HasPrefix(rm.status, "the host's SSH target starts with '-'") || !strings.HasSuffix(rm.status, notices) {
		t.Errorf("refusal = %q", rm.status)
	}
}

// TestOpenEditorOverrideVariable (T027, FR-502): the raw value is one
// executable, blank gives the default, and SWITCHBOARD_CODE_BIN reaches VS
// Code unchanged; the config → WithEditor round trip is in config's tests.
func TestOpenEditorOverrideVariable(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := editorModel(t, d)
	m.prefs.Editor = "webstorm"
	m.editorBins = map[string]string{"webstorm": "/opt/My Tools/ws"}
	// The executable is looked up on PATH: a real file stands in.
	dir := t.TempDir()
	exe := filepath.Join(dir, "My Tools", "ws")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.editorBins["webstorm"] = exe
	close(l.report)
	m, _, _ = pressV(t, m)
	if got := l.last(); len(got) != 2 || got[0] != exe || got[1] != "/workspace" {
		t.Errorf("argv = %q", got)
	}
	m.editorBins["webstorm"] = "   "
	m, _, _ = pressV(t, m)
	if got := l.last(); strings.Join(got, " ") != "webstorm /workspace" {
		t.Errorf("blank variable: argv = %q", got)
	}
	m.prefs.Editor = ""
	m.editorBins["vscode"] = exe
	m, _, _ = pressV(t, m)
	if got := l.last(); got[0] != exe {
		t.Errorf("SWITCHBOARD_CODE_BIN: argv = %q", got)
	}
}

// TestOpenEditorNotFound (T028, FR-518): a missing executable and a launch
// refused by the OS name the executable, the reason, and the variable or the
// settings screen; the tightest case fits in 4 lines at 80 columns.
func TestOpenEditorNotFound(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := editorModel(t, d)
	realLookPath(t)
	m.prefs.Editor = "webstorm"
	missing := filepath.Join(t.TempDir(), "missing", "ws")
	m.editorBins = map[string]string{"webstorm": missing}
	m, msg, _ := pressV(t, m)
	want := "can't start " + cutMiddle(missing, 60) + ": no such file or directory — set SWITCHBOARD_WEBSTORM_BIN to one executable (or use Custom)"
	if !msg.failed || m.status != want {
		t.Errorf("not found = %q\nwant        %q", m.status, want)
	}
	if l.calls() != 0 {
		t.Error("a missing executable is refused before the daemon call and the launch")
	}
	// A launch failure: the OS reason is the innermost error.
	m.editorBins["webstorm"] = "webstorm"
	l.err = &fs.PathError{Op: "fork/exec", Path: "webstorm", Err: syscall.EACCES}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "webstorm"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	m, _, _ = pressV(t, m)
	if m.status != "can't start webstorm: permission denied — set SWITCHBOARD_WEBSTORM_BIN to one executable (or use Custom)" {
		t.Errorf("launch failed = %q", m.status)
	}
	// Custom points to the settings screen.
	m.prefs.Editor = "custom"
	m.prefs.EditorCustomCommand = "webstorm {path}"
	m, _, _ = pressV(t, m)
	if m.status != "can't start webstorm: permission denied — fix the Custom command on the settings screen (,)" {
		t.Errorf("custom launch failed = %q", m.status)
	}
	// The tightest case: a 60-column executable, a 40-column reason,
	// SWITCHBOARD_CODE_INSIDERS_BIN and the 60-column unknown-editor notice.
	ci, _ := editor.Lookup("code-insiders")
	exe := strings.Repeat("e", 60)
	reason := errors.New(strings.Repeat("r", 40))
	for _, e := range []editor.Editor{ci, {ID: editor.Custom, Name: "Custom"}} {
		text := withNotices(startFailedText(e, exe, reason), []string{"unknown editor '" + strings.Repeat("u", 60) + "' in settings"})
		if n := len(statusLines(text, 80, 6)); n > 4 {
			t.Errorf("the tightest not-found case (%s) wraps to %d lines at 80 columns:\n%s", e.ID, n, strings.Join(statusLines(text, 80, 6), "\n"))
		}
	}
}

// TestOpenEditorRemote (T031, US4): the Remote-SSH option, Zed's address, the
// refusal for an editor without remote support (before any daemon call), a
// failed host lookup, the target checks and the path limit.
func TestOpenEditorRemote(t *testing.T) {
	d := &fakeDaemon{}
	m, l, st := remoteEditorModel(t, d)
	close(l.report)
	m.prefs.Editor = "cursor"
	m, _, _ = pressV(t, m)
	if got := l.last(); strings.Join(got, " ") != "cursor --remote ssh-remote+user@box /workspace" {
		t.Errorf("cursor argv = %q", got)
	}
	m.prefs.Editor = "zed"
	d.vscodePath = "/srv/ws/my proj"
	m, _, _ = pressV(t, m)
	if got := l.last(); strings.Join(got, " ") != "zed ssh://user@box/srv/ws/my%20proj" {
		t.Errorf("zed argv = %q", got)
	}
	d.vscodePath = ""
	// No remote support: refused before VSCodeTarget, nothing launched, red.
	m.prefs.Editor = "webstorm"
	calls, launches := d.vscodeCalls, l.calls()
	m, msg, _ := pressV(t, m)
	if !msg.failed || !strings.Contains(m.status, "WebStorm can't open folders on remote hosts") || !m.statusIsErr() {
		t.Errorf("no-remote: %q", m.status)
	}
	if d.vscodeCalls != calls || l.calls() != launches {
		t.Error("the refusal must come before the daemon call and the launch")
	}
	// A failed host lookup refuses for VS Code too (FR-520): a real error from
	// a wrong type in hosts.toml, over 78 columns so the cut shows.
	m.prefs.Editor = ""
	if err := os.WriteFile(filepath.Join(st.Dir(), "hosts.toml"), []byte("[[hosts]]\nid = 'remote'\ndisplay_name = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, lookupErr := st.Hosts().Get("remote")
	if lookupErr == nil || ansi.StringWidth(lookupErr.Error()) <= hostErrCols {
		t.Fatalf("the lookup error should be real and long: %v", lookupErr)
	}
	m, msg, _ = pressV(t, m)
	cut := cutMiddle(lookupErr.Error(), hostErrCols)
	if !msg.failed || m.status != "can't open sb1: host entry unavailable ("+cut+") — check the hosts file" {
		t.Errorf("host-lookup = %q", m.status)
	}
	if strings.Count(cut, "…") != 1 || ansi.StringWidth(cut) != hostErrCols || !strings.HasPrefix(cut, ansi.Truncate(lookupErr.Error(), 38, "")) || !strings.HasSuffix(cut, ansi.TruncateLeft(lookupErr.Error(), ansi.StringWidth(lookupErr.Error())-39, "")) {
		t.Errorf("the lookup error keeps its first 38 and last 39 columns: %q", cut)
	}
	if d.vscodeCalls != calls {
		t.Error("a failed lookup must not call the daemon")
	}
	// The active host missing from the hosts file is local (EnsureLocal
	// couldn't write it), unless the manager knows it as an SSH host.
	if err := os.Remove(filepath.Join(st.Dir(), "hosts.toml")); err != nil {
		t.Fatal(err)
	}
	if tgt, err := m.sshTargetForHost("remote"); tgt != "" || err != nil {
		t.Errorf("active host absent from the store: %q, %v", tgt, err)
	}
	if _, err := m.sshTargetForHost("other"); err == nil {
		t.Error("a non-active host that is gone refuses")
	}
	mgr := client.NewManager()
	mgr.Upsert(client.HostEntry{ID: "remote", DisplayName: "box", Kind: "ssh", SSHTarget: "user@box"})
	if _, err := m.WithHosts(mgr, st.Hosts(), "remote").sshTargetForHost("remote"); err == nil {
		t.Error("an SSH host the manager knows refuses when its entry is gone")
	}
	// Target checks.
	target := func(tgt string) {
		t.Helper()
		if _, err := st.Hosts().Save(store.KnownHost{ID: "remote", DisplayName: "box", Kind: "ssh", SSHTarget: tgt}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(st.Dir(), "hosts.toml")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	target("-oProxyCommand=x")
	m, _, _ = pressV(t, m)
	if !strings.HasPrefix(m.status, "the host's SSH target starts with '-'") {
		t.Errorf("target-dash = %q", m.status)
	}
	target("user@box/evil")
	m.prefs.Editor = "zed"
	m, _, _ = pressV(t, m)
	if !strings.HasPrefix(m.status, "the host's SSH target can't be used in an ssh:// address") {
		t.Errorf("zed-target = %q", m.status)
	}
	// A relative daemon path with Zed is the daemon's fault, not the host entry's.
	target("user@box")
	d.vscodePath = "work/space"
	m, _, _ = pressV(t, m)
	if !strings.HasPrefix(m.status, "the workspace path from the daemon isn't absolute") {
		t.Errorf("relative path with Zed = %q", m.status)
	}
	// The path limit (FR-517): on for the selected editor's own set variable.
	target("user@box")
	d.vscodePath = "/srv/ws/a(b"
	m.prefs.Editor = "cursor"
	m.editorBins = map[string]string{"cursor": "/x/cursor"}
	m, _, _ = pressV(t, m)
	if m.status != "the workspace path contains '(', which isn't allowed on a remote sandbox while SWITCHBOARD_CURSOR_BIN is set — move the workspace on the daemon or unset SWITCHBOARD_CURSOR_BIN" {
		t.Errorf("path-limited (built-in) = %q", m.status)
	}
	m.prefs.Editor = "custom"
	m.prefs.EditorCustomCommand = "logedit {path}"
	m, _, _ = pressV(t, m)
	if m.status != "the workspace path contains '(', which isn't allowed when Custom opens a remote sandbox — move the workspace on the daemon or switch editors on the settings screen (,)" {
		t.Errorf("path-limited (Custom) = %q", m.status)
	}
}

// TestOpenEditorRemoteNoHostNotice (T032, FR-515, FR-529): the three-notice
// completion, within 3 lines at 80 columns.
func TestOpenEditorRemoteNoHostNotice(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := remoteEditorModel(t, d)
	close(l.report)
	m.prefs.Editor = "custom"
	m.prefs.EditorCustomCommand = "logedit {path}"
	m.settingsView.fileUnreadable = errors.New("x")
	m, _, _ = pressV(t, m)
	want := "opened sb1 in Custom (settings couldn't be read at startup, changes aren't saved; the path is on the remote host)"
	if m.status != want {
		t.Errorf("status = %q", m.status)
	}
	if n := len(statusLines(want, 80, 6)); n > 3 {
		t.Errorf("wraps to %d lines", n)
	}
	// With {host} the warning is left out.
	m.prefs.EditorCustomCommand = "logedit --host {host} {path}"
	m, _, _ = pressV(t, m)
	if strings.Contains(m.status, "remote host") || strings.Join(l.last(), " ") != "logedit --host user@box /workspace" {
		t.Errorf("with {host}: %q argv %q", m.status, l.last())
	}
}

// TestOpenEditorCustom (T038, US3): the saved command is re-checked on every
// v, ~ expands at launch, the path is one argument, {host} is dropped locally,
// and the report points to the settings screen.
func TestOpenEditorCustom(t *testing.T) {
	d := &fakeDaemon{}
	m, l, _ := editorModel(t, d)
	m.prefs.Editor = "custom"
	m.prefs.EditorCustomCommand = "API_TOKEN=abc123 x {path}"
	m, msg, _ := pressV(t, m)
	if !msg.failed || m.status != "the Custom command starts with NAME=value, which nothing sets — use a wrapper script; fix it on the settings screen (,)" {
		t.Errorf("assignment = %q", m.status)
	}
	m, _ = update(m, press(","))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Custom (invalid command)") || strings.Contains(v, "abc123") {
		t.Errorf("the row must hide the command:\n%s", v)
	}
	if strings.Contains(m.status, "abc123") {
		t.Error("the status must not quote the command")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	// ~ expands with the home directory at launch.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(home, "bin", "edit")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	d.vscodePath = "/ws/a b"
	m.prefs.EditorCustomCommand = "~/bin/edit --open {path} extra"
	m, _, next := pressV(t, m)
	if got := l.last(); len(got) != 4 || got[0] != exe || got[2] != "/ws/a b" {
		t.Errorf("argv = %q", got)
	}
	if m.status != "opened sb1 in Custom" {
		t.Errorf("status = %q", m.status)
	}
	l.report <- editor.ExitReport{Status: 1}
	m, _ = update(m, runCmd(next))
	if m.status != cutMiddle(exe, 60)+" exited with status 1 after opening sb1 — check the Custom command on the settings screen (,)" {
		t.Errorf("custom report = %q", m.status)
	}
	// No home: refused.
	t.Setenv("HOME", "")
	m, _, _ = pressV(t, m)
	if !strings.HasPrefix(m.status, "the Custom command uses ~ but the home directory can't be determined") {
		t.Errorf("no-home = %q", m.status)
	}
	t.Setenv("HOME", home)
	// No marker: appended; a bare {host} is dropped locally.
	m.prefs.EditorCustomCommand = "~/bin/edit {host}"
	m, _, _ = pressV(t, m)
	if got := l.last(); strings.Join(got, "|") != exe+"|/ws/a b" {
		t.Errorf("argv = %q", got)
	}
	// An empty command, and every other rule, is refused with its wording.
	m.prefs.EditorCustomCommand = ""
	m, _, _ = pressV(t, m)
	if m.status != "no Custom command is set — set one on the settings screen (,)" {
		t.Errorf("empty = %q", m.status)
	}
	custom, _ := editor.Lookup("custom")
	for _, rule := range []string{editor.RuleEmptyExecutable, editor.RuleMarkerInExecutable, editor.RuleShellSyntax, editor.RuleUnbalancedQuotes, editor.RuleTrailingBackslash, editor.RuleControlCharacter, editor.RulePathUnsafe, "other"} {
		if got := refusalText("sb1", custom, &editor.Refusal{Rule: rule}); got == "" || strings.Contains(got, "abc123") {
			t.Errorf("%s: %q", rule, got)
		}
	}
}
