package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

func settingsModel(t *testing.T) (Model, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{}
	m := twoHostModel(t, d).WithSettings(st.Settings())
	return m, st
}

func TestSettingsScreenOpensFromBothScreensAndReturns(t *testing.T) {
	m, _ := settingsModel(t)
	m, _ = update(m, press(","))
	if m.screen != screenSettings || !strings.Contains(m.View(), "Automatic host sign-in") {
		t.Fatalf("`,` should open the settings screen; screen %v", m.screen)
	}
	m, _ = update(m, press("esc"))
	if m.screen != screenList {
		t.Errorf("esc should return to the list, got %v", m.screen)
	}
	m, _ = update(m, shiftRight())
	m, _ = update(m, press(","))
	m, _ = update(m, press("esc"))
	if m.screen != screenDaemons {
		t.Errorf("esc should return to the daemon screen it was opened from, got %v", m.screen)
	}
}

func TestSettingsToggleSavesImmediately(t *testing.T) {
	m, st := settingsModel(t)
	if !m.prefs.AutoConnectHosts {
		t.Fatal("auto-connect should default to on")
	}
	m, _ = update(m, press(","))
	if !strings.Contains(m.View(), "[on]") {
		t.Errorf("toggle should read on; got:\n%s", m.View())
	}
	m, _ = update(m, press(" "))
	if m.prefs.AutoConnectHosts || m.status != "saved settings" || !strings.Contains(m.View(), "[off]") {
		t.Errorf("space should turn it off and save; prefs %+v status %q", m.prefs, m.status)
	}
	got, err := st.Settings().Load()
	if err != nil || got.AutoConnectHosts {
		t.Errorf("persisted = %+v, %v", got, err)
	}
}

func TestSettingsUnreadableFileShowsNoticeAndDefaults(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir(), "settings.toml"), []byte("auto_connect_hosts = [broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := twoHostModel(t, &fakeDaemon{}).WithSettings(st.Settings())
	if !m.prefs.AutoConnectHosts {
		t.Error("defaults must apply when the file is unreadable")
	}
	m, _ = update(m, press(","))
	if !strings.Contains(m.View(), "could not be read") {
		t.Errorf("the screen should say defaults apply; got:\n%s", m.View())
	}
}

func TestSettingsWithoutStoreAppliesForSessionOnly(t *testing.T) {
	m := twoHostModel(t, &fakeDaemon{}) // no WithSettings: store nil, defaults on
	m, _ = update(m, press(","))
	m, _ = update(m, press(" "))
	if m.prefs.AutoConnectHosts || !strings.Contains(m.status, "session only") {
		t.Errorf("prefs %+v status %q", m.prefs, m.status)
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("k"))
	m, _ = update(m, press(","))
	if m.screen != screenList {
		t.Errorf("`,` again should close the screen, got %v", m.screen)
	}
}
