package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubSbx writes a fake `sbx` script answering --version and `mcp ls` per the
// given bodies (exit status via the ls body's trailing "exit N").
func stubSbx(t *testing.T, version, lsBody string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sbx")
	script := "#!/usr/bin/env bash\ncase \"$1 $2\" in\n  \"--version \") echo \"" + version + "\" ;;\n  \"mcp ls\") " + lsBody + " ;;\n  *) echo \"unknown $*\" >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestProbe(t *testing.T) {
	cases := []struct {
		name     string
		bin      string
		wantOK   bool
		wantWord string
	}{
		{"missing binary", filepath.Join(t.TempDir(), "nope"), false, "not installed"},
		{"unparseable version", stubSbx(t, "sbx-e2e 0.0", `echo ok`), false, "could not parse"},
		{"below baseline", stubSbx(t, "sbx version 0.35.0", `echo ok`), false, "below the minimum"},
		{"not signed in", stubSbx(t, "sbx version 0.46.0", `echo "You are not authenticated to Docker. Run sbx login" >&2; exit 1`), false, "not signed in"},
		{"mcp unusable", stubSbx(t, "sbx version 0.46.0", `echo "unknown command mcp" >&2; exit 1`), false, "not usable"},
		{"available", stubSbx(t, "sbx version 0.46.0", `printf 'NAME  TYPE  URL/COMMAND\n'`), true, ""},
	}
	for _, tc := range cases {
		a := Probe(context.Background(), tc.bin)
		if a.Available != tc.wantOK {
			t.Errorf("%s: available = %v (reason %q), want %v", tc.name, a.Available, a.Reason, tc.wantOK)
		}
		if tc.wantWord != "" && !strings.Contains(a.Reason, tc.wantWord) {
			t.Errorf("%s: reason %q should mention %q", tc.name, a.Reason, tc.wantWord)
		}
		if tc.name == "available" && (a.SbxVersion != "0.46.0" || !a.BaselineMet) {
			t.Errorf("available probe = %+v, want version 0.46.0 and baseline met", a)
		}
	}
}

func TestArgvTable(t *testing.T) {
	cases := map[string][]string{
		"mcp ls":                        argvList(),
		"mcp auth status notion --json": argvAuthStatus("notion"),
		"mcp add notion --url https://x/mcp --skip-auth":           argvAddRemote("notion", "https://x/mcp"),
		"mcp add pw --command npx --args a,b --dir /d --skip-auth": argvAddLocal("pw", "npx", []string{"a", "b"}, "/d"),
		"mcp add pw --command npx --skip-auth":                     argvAddLocal("pw", "npx", nil, ""),
		"mcp rm notion":                                            argvRemove("notion"),
		"mcp auth notion":                                          argvAuthorize("notion"),
		"mcp load notion --sandbox my-sb":                          argvLoad("notion", "my-sb"),
	}
	for want, got := range cases {
		if strings.Join(got, " ") != want {
			t.Errorf("argv = %q, want %q", strings.Join(got, " "), want)
		}
	}
}

func TestRunStreamingKillsOnCancel(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sbx")
	script := "#!/usr/bin/env bash\necho 'Open this URL to authorize \"x\": https://example.test/auth'\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cli := &CLI{Bin: bin}
	ctx, cancel := context.WithCancel(context.Background())
	var lines []string
	done := make(chan error, 1)
	go func() {
		done <- cli.runStreaming(ctx, func(l string) {
			lines = append(lines, l)
			cancel() // the first line arrived: abort like a timeout would
		}, "mcp", "auth", "x")
	}()
	err := <-done
	if err == nil || ctx.Err() == nil {
		t.Fatalf("expected a cancellation error, got %v", err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "https://example.test/auth") {
		t.Errorf("streamed lines = %q", lines)
	}
}

func TestRunCarriesOutputTail(t *testing.T) {
	bin := stubSbx(t, "sbx version 0.46.0", `echo "boom: no such server" >&2; exit 3`)
	_, err := (&CLI{Bin: bin}).run(context.Background(), "mcp", "ls")
	if err == nil || !strings.Contains(err.Error(), "boom: no such server") || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("err = %v", err)
	}
}
