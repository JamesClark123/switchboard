package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// recNotifier records the last desktop notification raised.
type recNotifier struct{ title, msg string }

func (r *recNotifier) Notify(title, message string) { r.title, r.msg = title, message }

func TestUpdateHint(t *testing.T) {
	cases := []struct {
		latest, cur string
		want        bool // whether a hint is produced
	}{
		{"v0.4.1", "v0.4.0", true},
		{"v0.4.0", "v0.4.0", false},
		{"", "v0.4.0", false},
		{"v0.4.0", "", false},
	}
	for _, c := range cases {
		got := updateHint(c.latest, c.cur)
		if (got != "") != c.want {
			t.Errorf("updateHint(%q,%q) = %q, want hint=%v", c.latest, c.cur, got, c.want)
		}
	}
}

func TestUpdateAvailableSetsBannerAndNotifies(t *testing.T) {
	rec := &recNotifier{}
	m := sized(New(&fakeDaemon{}, "/work").WithVersion("v0.4.0").WithNotifier(rec))

	// A newer release raises a banner + desktop notification and exposes `u`.
	m, _ = update(m, updateAvailableMsg{latest: "v0.5.0"})
	if m.updateBanner == "" || m.latestVersion != "v0.5.0" {
		t.Fatalf("expected banner + latestVersion set, got %q/%q", m.updateBanner, m.latestVersion)
	}
	if rec.title == "" {
		t.Error("expected a desktop notification")
	}
	if !strings.Contains(m.View(), "update available") {
		t.Error("list view should show the update banner")
	}
	// The list help now surfaces the update key.
	var hasU bool
	for _, b := range m.listHelp() {
		if b.Help().Key == "u" {
			hasU = true
		}
	}
	if !hasU {
		t.Error("listHelp should include the update key when an update is available")
	}

	// An equal version raises nothing.
	m2 := sized(New(&fakeDaemon{}, "/work").WithVersion("v0.5.0").WithNotifier(&recNotifier{}))
	m2, _ = update(m2, updateAvailableMsg{latest: "v0.5.0"})
	if m2.updateBanner != "" {
		t.Error("no banner when already current")
	}
}

func TestEnterUpdateUpToDate(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work").WithVersion("v0.5.0"))
	m.latestVersion = "v0.5.0" // not newer
	mm, cmd := update(m, press("u"))
	if mm.screen == screenUpdate {
		t.Error("should not enter update screen when current")
	}
	if !strings.Contains(mm.status, "up to date") {
		t.Errorf("status = %q", mm.status)
	}
	_ = cmd
}

func TestUpdateFlowTriggerAndRestart(t *testing.T) {
	d := &fakeDaemon{daemonVer: "v0.4.0"}
	m := withSandboxes(New(d, "/work").WithVersion("v0.4.0"), []*pb.Sandbox{{Id: "a", DisplayName: "x"}})
	m, _ = update(m, updateAvailableMsg{latest: "v0.5.0"})

	// Stub the network/disk side effects.
	origApply, origExec := applyLocalUpdate, selfExecPath
	applyLocalUpdate = func(_ context.Context, target, _ string) (string, bool, error) { return target, false, nil }
	selfExecPath = func() (string, error) { return "/home/u/.local/bin/sxb", nil }
	defer func() { applyLocalUpdate, selfExecPath = origApply, origExec }()

	// `u` enters the update screen and kicks off the orchestration command.
	m, cmd := update(m, press("u"))
	if m.screen != screenUpdate || !m.update.running {
		t.Fatalf("expected running update screen, got screen=%v running=%v", m.screen, m.update.running)
	}
	if !strings.Contains(m.View(), "Updating to v0.5.0") {
		t.Error("running update view should name the target")
	}

	// Run the orchestration command → updateResultMsg, then apply it.
	msg := runCmd(cmd)
	// cmd is a Batch (runUpdateCmd + spinner tick); find the updateResultMsg by
	// running the update command directly instead.
	if _, ok := msg.(updateResultMsg); !ok {
		msg = runCmd(m.runUpdateCmd("v0.5.0"))
	}
	res, ok := msg.(updateResultMsg)
	if !ok {
		t.Fatalf("expected updateResultMsg, got %T", msg)
	}
	if d.updateTarget != "v0.5.0" {
		t.Errorf("daemon update target = %q, want v0.5.0", d.updateTarget)
	}
	m, _ = update(m, res)
	if !m.update.finished || m.update.localVer != "v0.5.0" {
		t.Fatalf("update should finish with localVer set: %+v", m.update)
	}
	if m.updateBanner != "" {
		t.Error("banner should clear after a successful client update")
	}
	if !strings.Contains(m.View(), "client updated to v0.5.0") {
		t.Error("summary should confirm the client update")
	}

	// Enter restarts into the new binary (reexec + quit).
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.ShouldReexec() || !m.quitting {
		t.Error("enter on a successful update should request re-exec and quit")
	}
	_ = cmd
}

func TestUpdateResultBrewAndErrorPaths(t *testing.T) {
	base := func() Model {
		m := sized(New(&fakeDaemon{}, "/work").WithVersion("v0.4.0"))
		m.screen = screenUpdate
		m.update = updateState{target: "v0.5.0"}
		return m
	}

	// Brew-managed: no re-exec, view nudges brew upgrade, esc returns.
	m := base()
	m, _ = update(m, updateResultMsg{results: []hostUpdate{{name: "local"}}, localBrew: true})
	if !strings.Contains(m.View(), "brew upgrade") {
		t.Error("brew-managed client should nudge `brew upgrade`")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.ShouldReexec() {
		t.Error("brew-managed update must not re-exec")
	}
	if m.screen != screenList {
		t.Error("enter on a non-restartable result returns to the list")
	}

	// Host + client errors are surfaced.
	m = base()
	m, _ = update(m, updateResultMsg{
		results:  []hostUpdate{{name: "web", err: errors.New("boom")}},
		localErr: errors.New("disk full"),
	})
	v := m.View()
	if !strings.Contains(v, "boom") || !strings.Contains(v, "disk full") {
		t.Errorf("error view missing details:\n%s", v)
	}
	// esc returns to the list without re-exec.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList || m.ShouldReexec() {
		t.Error("esc after a failed update returns to the list, no re-exec")
	}
}

func TestUpdateTargetsSingleDaemon(t *testing.T) {
	d := &fakeDaemon{}
	m := sized(New(d, "/work"))
	ts := m.updateTargets()
	if len(ts) != 1 || ts[0].name != "local" {
		t.Fatalf("expected a single local target, got %+v", ts)
	}
}

func TestUpdateTargetsManagerFallsBackWhenNoneConnected(t *testing.T) {
	// A manager with only a disconnected host contributes no targets, so the
	// local daemon is still updated.
	m := sized(New(&fakeDaemon{}, "/work"))
	mgr := client.NewManager()
	mgr.Upsert(client.HostEntry{ID: "web", DisplayName: "web", Kind: "ssh"})
	m.manager = mgr
	ts := m.updateTargets()
	if len(ts) != 1 || ts[0].name != "local" {
		t.Fatalf("expected fallback to local when no host is connected, got %+v", ts)
	}
}

func TestUpdateKeysIgnoredWhileRunning(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.screen = screenUpdate
	m.update = updateState{running: true, target: "v0.5.0"}
	mm, _ := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if mm.screen != screenUpdate {
		t.Error("keys must be ignored while an update is in flight")
	}
	if len(m.updateHelpBindings()) != 0 {
		t.Error("no help bindings while running")
	}
}

func TestCheckUpdateOptOut(t *testing.T) {
	t.Setenv("SXB_NO_UPDATE_CHECK", "1")
	if msg := runCmd(checkUpdateCmd()); msg != nil {
		t.Errorf("opt-out should suppress the update check, got %T", msg)
	}
}
