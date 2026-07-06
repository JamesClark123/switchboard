package daemonctl

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteReadClearPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "switchboard.pid")
	if err := WritePID(path, 4321); err != nil {
		t.Fatalf("WritePID: %v", err)
	}
	pid, err := ReadPID(path)
	if err != nil {
		t.Fatalf("ReadPID: %v", err)
	}
	if pid != 4321 {
		t.Errorf("ReadPID = %d, want 4321", pid)
	}
	if err := Clear(path); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	// Clearing an already-absent file is a no-op, not an error.
	if err := Clear(path); err != nil {
		t.Errorf("Clear on missing file: %v", err)
	}
	if _, err := ReadPID(path); err == nil {
		t.Error("ReadPID on missing file should error")
	}
}

func TestReadPIDMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "switchboard.pid")
	if err := os.WriteFile(path, []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPID(path); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("expected malformed pid error, got %v", err)
	}
}

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("the test process should be alive")
	}
	if Alive(0) || Alive(-1) {
		t.Error("non-positive pids are never alive")
	}
	// A very high pid is almost certainly unused.
	if Alive(1 << 30) {
		t.Error("an unused pid should not be reported alive")
	}
}

func TestRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "switchboard.pid")

	// No file → not running.
	if _, ok := Running(path); ok {
		t.Error("missing pid file should not be running")
	}

	// Live pid (this process) → running.
	if err := WritePID(path, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if pid, ok := Running(path); !ok || pid != os.Getpid() {
		t.Errorf("Running = (%d,%v), want (%d,true)", pid, ok, os.Getpid())
	}

	// Stale pid → not running, and the stale file is cleared.
	if err := WritePID(path, 1<<30); err != nil {
		t.Fatal(err)
	}
	if _, ok := Running(path); ok {
		t.Error("stale pid should not be running")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Running should clear a stale pid file")
	}
}

func TestSocketReachable(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")

	if SocketReachable(sock, 200*time.Millisecond) {
		t.Error("no listener yet → unreachable")
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if !SocketReachable(sock, time.Second) {
		t.Error("listener present → reachable")
	}
}

func TestRenderUnit(t *testing.T) {
	// Default args → plain `serve`.
	u := RenderUnit(UnitOptions{
		ExecStart:   "/usr/local/bin/sxbd",
		Environment: []string{"SWITCHBOARDD_SOCKET=/run/x.sock", "SWITCHBOARDD_DATA_DIR=/data"},
	})
	for _, want := range []string{
		"ExecStart=/usr/local/bin/sxbd serve\n",
		"Restart=always",
		"WantedBy=default.target",
		"Environment=SWITCHBOARDD_SOCKET=/run/x.sock",
		"Environment=SWITCHBOARDD_DATA_DIR=/data",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("rendered unit missing %q:\n%s", want, u)
		}
	}

	// Explicit args are joined into ExecStart (e.g. carrying --debug on boot).
	u2 := RenderUnit(UnitOptions{ExecStart: "/bin/sxbd", Args: []string{"serve", "--debug"}})
	if !strings.Contains(u2, "ExecStart=/bin/sxbd serve --debug\n") {
		t.Errorf("expected joined args in ExecStart:\n%s", u2)
	}
}

func TestUserUnitPathAndInstalled(t *testing.T) {
	dir := t.TempDir()

	// XDG_CONFIG_HOME wins when set.
	env := map[string]string{"XDG_CONFIG_HOME": dir}
	getenv := func(k string) string { return env[k] }
	path, err := UserUnitPath(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "systemd", "user", UnitName); path != want {
		t.Errorf("UserUnitPath = %q, want %q", path, want)
	}
	if UnitInstalled(getenv) {
		t.Error("unit should not be installed yet")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !UnitInstalled(getenv) {
		t.Error("unit should be installed after writing the file")
	}

	// Falls back to $HOME/.config when XDG_CONFIG_HOME is unset.
	env2 := map[string]string{"HOME": "/home/tester"}
	p2, err := UserUnitPath(func(k string) string { return env2[k] })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/tester", ".config", "systemd", "user", UnitName); p2 != want {
		t.Errorf("fallback UserUnitPath = %q, want %q", p2, want)
	}

	// Neither set → error.
	if _, err := UserUnitPath(func(string) string { return "" }); err == nil {
		t.Error("expected an error when neither XDG_CONFIG_HOME nor HOME is set")
	}
}

func TestLaunchAgentPath(t *testing.T) {
	getenv := func(k string) string {
		if k == "HOME" {
			return "/Users/dev"
		}
		return ""
	}
	got, err := LaunchAgentPath(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/Users/dev/Library/LaunchAgents/" + LaunchdLabel + ".plist"; got != want {
		t.Errorf("LaunchAgentPath = %q, want %q", got, want)
	}
	// HOME unset → error (rather than a root-relative path).
	if _, err := LaunchAgentPath(func(string) string { return "" }); err == nil {
		t.Error("expected an error when HOME is unset")
	}
}

func TestRenderLaunchAgent(t *testing.T) {
	plist := RenderLaunchAgent(UnitOptions{
		ExecStart:   "/usr/local/bin/sxbd",
		Args:        []string{"serve", "--debug"},
		Environment: []string{"SWITCHBOARDD_SOCKET=/Users/dev/x.sock", "SWITCHBOARDD_HOST_ID=mac & co"},
		LogPath:     "/Users/dev/switchboard.log",
	})
	for _, want := range []string{
		`<key>Label</key>`,
		`<string>` + LaunchdLabel + `</string>`,
		`<string>/usr/local/bin/sxbd</string>`,
		`<string>serve</string>`,
		`<string>--debug</string>`,
		`<key>RunAtLoad</key>`,
		`<key>KeepAlive</key>`,
		`<key>SWITCHBOARDD_SOCKET</key>`,
		`<string>/Users/dev/x.sock</string>`,
		`<key>StandardOutPath</key>`,
		`<string>/Users/dev/switchboard.log</string>`,
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("rendered plist missing %q\n%s", want, plist)
		}
	}
	// XML metacharacters in an env value are escaped.
	if !strings.Contains(plist, "mac &amp; co") {
		t.Errorf("plist did not escape '&' in an env value:\n%s", plist)
	}
	// Args default to ["serve"] when none are given.
	if p := RenderLaunchAgent(UnitOptions{ExecStart: "/bin/sxbd"}); !strings.Contains(p, "<string>serve</string>") {
		t.Errorf("default args should be [serve]:\n%s", p)
	}
}

func TestBootPathAndInstalledPerOS(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		switch k {
		case "HOME":
			return dir
		default:
			return ""
		}
	}

	// darwin → LaunchAgent plist; linux → systemd unit.
	dp, err := BootUnitPath("darwin", getenv)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "Library", "LaunchAgents", LaunchdLabel+".plist"); dp != want {
		t.Errorf("darwin BootUnitPath = %q, want %q", dp, want)
	}
	lp, err := BootUnitPath("linux", getenv)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ".config", "systemd", "user", UnitName); lp != want {
		t.Errorf("linux BootUnitPath = %q, want %q", lp, want)
	}
	// Unsupported OS → error, and BootInstalled is false.
	if _, err := BootUnitPath("plan9", getenv); err == nil {
		t.Error("expected error for an unsupported OS")
	}
	if BootInstalled("plan9", getenv) {
		t.Error("BootInstalled should be false on an unsupported OS")
	}

	// Not installed until the plist exists; then detected.
	if BootInstalled("darwin", getenv) {
		t.Error("launchd agent should not be installed yet")
	}
	if err := os.MkdirAll(filepath.Dir(dp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dp, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !BootInstalled("darwin", getenv) {
		t.Error("launchd agent should be detected after writing the plist")
	}
}

func TestBootBackendName(t *testing.T) {
	if got := BootBackendName("darwin"); got != "launchd agent" {
		t.Errorf("darwin backend = %q", got)
	}
	if got := BootBackendName("linux"); got != "systemd user service" {
		t.Errorf("linux backend = %q", got)
	}
	if got := BootBackendName("windows"); got != "" {
		t.Errorf("unsupported backend = %q, want empty", got)
	}
}

func TestCurrentEnv(t *testing.T) {
	got := CurrentEnv([]string{
		"PATH=/bin",
		"SWITCHBOARDD_SOCKET=/run/x.sock",
		"HOME=/home/x",
		"SWITCHBOARDD_DATA_DIR=/data",
	})
	if len(got) != 2 || got[0] != "SWITCHBOARDD_SOCKET=/run/x.sock" || got[1] != "SWITCHBOARDD_DATA_DIR=/data" {
		t.Errorf("CurrentEnv = %v", got)
	}
}
