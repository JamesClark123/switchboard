package mcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestParseInspectShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		out      string
		headers  []string
		reported bool
	}{
		"strings":       {`{"url":"u","headers":["A: 1","B: 2"]}`, []string{"A: 1", "B: 2"}, true},
		"objects":       {`[{"name":"gh","url":"u","headers":[{"name":"A","value":"1"}]}]`, []string{"A: 1"}, true},
		"map":           {`{"headers":{"B":"2","A":"1"}}`, []string{"A: 1", "B: 2"}, true},
		"empty list":    {`{"headers":[]}`, nil, false},
		"bad entry":     {`{"headers":[1]}`, nil, false},
		"nameless":      {`{"headers":[{"value":"1"}]}`, nil, false},
		"bad map value": {`{"headers":{"A":1}}`, nil, false},
		"scalar":        {`{"headers":"A: 1"}`, nil, false},
		"absent":        {`{"url":"u"}`, nil, false},
		"not json":      {`nope`, nil, false},
		"other entry":   {`[{"name":"a"},{"name":"b"}]`, nil, false},
		"only entry":    {`[{"name":"other","headers":["A: 1"]}]`, []string{"A: 1"}, true},
	} {
		info := parseInspect(tc.out, "gh")
		if strings.Join(info.headers, "|") != strings.Join(tc.headers, "|") || info.headersReported != tc.reported {
			t.Errorf("%s: headers %v reported %v", name, info.headers, info.headersReported)
		}
	}
	info := parseInspect(`{"URL":"u","clientId":"c","authorization_server":"https://a","resource":"r","image":"i","kind":"remote","skip_ssrf_check":true}`, "gh")
	if *info.url != "u" || *info.clientID != "c" || *info.authServer != "https://a" || info.resource != "r" || info.image != "i" || info.kind != "remote" || !info.skipSSRF {
		t.Errorf("fields = %+v", info)
	}
	inspectReportsHeaders = true
	t.Cleanup(func() { inspectReportsHeaders = false })
	if info := parseInspect(`{"headers":[]}`, "gh"); !info.headersReported {
		t.Error("an empty list reports none when the flag says inspect reports headers")
	}
}

func TestParseAuthStatusScopes(t *testing.T) {
	for out, want := range map[string]string{
		`[{"server_name":"gh","default_scopes":["a","b"]}]`: "a b true",
		`{"recorded_scopes":["c", 1]}`:                      "c true",
		`{"scopes":{"default":["d"]}}`:                      "d true",
		`{"scopes":{"default":[]}}`:                         " false",
		`{"default_scopes":[]}`:                             " false",
		`{"scopes":"x"}`:                                    " false",
		`[{"server_name":"gh","status":"authorized"}]`:      " false",
		`nope`: " false",
	} {
		s, ok := parseAuthStatusScopes(out, "gh")
		if got := strings.Join(s, " ") + " " + map[bool]string{true: "true", false: "false"}[ok]; got != want {
			t.Errorf("%s = %q, want %q", out, got, want)
		}
	}
	authStatusReportsScopes = true
	t.Cleanup(func() { authStatusReportsScopes = false })
	if _, ok := parseAuthStatusScopes(`{"default_scopes":[]}`, "gh"); !ok {
		t.Error("an empty list reports none when the flag says so")
	}
}

func TestDefinitionViewFlags(t *testing.T) {
	e := newSaveEnv(t)
	d := remote("https://x/mcp", "X-Org: acme")
	d.Scopes = []string{"read"}
	d.ClientId = "c1"
	e.add("gh", d, true, nil)
	e.write("gh.inspect", `{"url":"https://moved/mcp","client_id":"c1","headers":["x-org:acme"],"resource":"r1","skip_ssrf_check":true}`)
	e.write("gh.auth", `[{"server_name":"gh","status":"unauthorized","default_scopes":["read","offline_access"]}]`)
	v := e.open("gh")
	if !hasFlag(v, "url", pb.McpFieldFlag_CHANGED_ON_HOST) || v.GetDefinition().GetUrl() != "https://moved/mcp" {
		t.Errorf("url = %v", v)
	}
	if hasFlag(v, "headers/X-Org", pb.McpFieldFlag_CHANGED_ON_HOST) || hasFlag(v, "scopes", pb.McpFieldFlag_CHANGED_ON_HOST) {
		t.Errorf("presentation-only differences flagged: %v", v.GetFlags())
	}
	if strings.Join(v.GetDefinition().GetScopes(), ",") != "read" {
		t.Errorf("a runtime-added offline_access was pre-filled: %v", v.GetDefinition().GetScopes())
	}
	if len(v.GetUnsupportedSettings()) != 2 {
		t.Errorf("unsupported = %v", v.GetUnsupportedSettings())
	}
	if !v.GetAttachByDefault() || !v.GetHasRecord() || v.GetFingerprint() == "" || !v.GetSecretsAvailable() {
		t.Errorf("view = %v", v)
	}
	// An unrecorded remote server names what cannot be recovered.
	e.write("ext.srv", "remote http")
	if v := e.open("ext"); !hasFlag(v, "url", pb.McpFieldFlag_NOT_RECOVERABLE) || v.GetHasRecord() {
		t.Errorf("ext = %v", v)
	}
	// forAdd needs no registration and reads only the secret status.
	e.sec.vals["mcp:new:k"] = "v"
	e.sec.vals["github"] = "v"
	v, err := e.m.Definition(context.Background(), "new", true)
	if err != nil || !has(v.GetStoredSecrets(), "mcp:new:k") || has(v.GetStoredSecrets(), "github") || v.GetDefinition() != nil {
		t.Errorf("forAdd = %v, %v", v, err)
	}
	// Secrets unreadable: the view says so instead of failing.
	e.sec.listErr = errors.New("secret ls broke")
	if v := e.open("gh"); v.GetSecretsAvailable() || v.GetSecretsUnavailableReason() == "" {
		t.Errorf("secrets unavailable = %v", v)
	}
	e.sec.listErr = nil
	if _, err := e.m.Definition(context.Background(), "nope", false); !errors.Is(err, ErrGone) {
		t.Errorf("gone = %v", err)
	}
	if _, err := e.m.Definition(context.Background(), "-bad", false); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("bad name = %v", err)
	}
	e.touch("ls.fail")
	if _, err := e.m.Definition(context.Background(), "gh", false); err == nil {
		t.Error("a failed list must fail the read")
	}
}

func TestReconcileDropsUnlockedAndKeepsLocked(t *testing.T) {
	e := newSaveEnv(t)
	e.add("a", remote("https://a"), true, nil)
	e.add("b", remote("https://b"), true, nil)
	e.add("c", remote("https://c"), true, nil)
	// a vanishes while unlocked: mark and definition dropped. b vanishes while
	// locked: kept. c holds a pending replace and is registered by another path.
	if _, err := e.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		s.PendingReplaces = map[string]*pb.McpPendingReplace{"c": {Name: "c", Revision: 1, RemovalFailedSecrets: []string{"mcp:c:x"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.rm("a.srv", "b.srv")
	_, release, _ := e.m.lockName(context.Background(), "b", lockOpts{})
	at, err := e.m.ResolveAttach(context.Background())
	release()
	if err != nil {
		t.Fatal(err)
	}
	s := e.settings()
	if s.GetMarks()["a"] || s.GetDefinitions()["a"] != nil || !has(at.Skipped, "a") {
		t.Errorf("a = %v / %v", s, at)
	}
	if !s.GetMarks()["b"] || s.GetDefinitions()["b"] == nil {
		t.Errorf("a locked name was reconciled: %v", s)
	}
	if s.GetPendingReplaces()["c"] != nil || s.GetMarks()["c"] || s.GetDefinitions()["c"] != nil {
		t.Errorf("c = %v", s)
	}
	if !has(e.endedHooks(), "c:removed") {
		t.Errorf("hooks = %v", e.endedHooks())
	}
	var ended *pb.McpSaveOutcome
	for _, o := range e.outcomes() {
		if o.GetName() == "c" {
			ended = o
		}
	}
	if ended == nil || !has(ended.GetRollbackFailed(), "mcp:c:x") {
		t.Errorf("another-path outcome = %v", ended)
	}
	// A pending replace with its mark is skipped at launch as being replaced.
	if _, err := e.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		s.PendingReplaces = map[string]*pb.McpPendingReplace{"p": {Name: "p", AttachByDefault: true, Revision: 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	at, _ = e.m.ResolveAttach(context.Background())
	if !has(at.BeingReplaced, "p") || has(at.Servers, "p") {
		t.Errorf("pending at launch = %+v", at)
	}
	// A save that re-registers is being replaced at launch.
	e.add("d", remote("https://d"), true, nil)
	e.m.setReplacing("d")
	at, _ = e.m.ResolveAttach(context.Background())
	if !has(at.BeingReplaced, "d") || has(at.Servers, "d") || e.m.ReplaceState("d") != BeingReplaced {
		t.Errorf("replacing at launch = %+v", at)
	}
}

// TestSavePanicCrashes re-runs the test binary (tasks.md T011): a hook panics
// while stateMu (persist) or m.mu (recoverName) is held under the per-name
// lock, and the release's re-panic guard must let the child crash within 5 s
// instead of deadlocking. The negative control switches the guard off and
// expects the child still running at the deadline.
func TestSavePanicCrashes(t *testing.T) {
	switch os.Getenv("SXB_PANIC_CHILD") {
	case "":
	case "persist", "persist-unguarded":
		skipPanicGuard = os.Getenv("SXB_PANIC_CHILD") == "persist-unguarded"
		e := newSaveEnv(t)
		testHookPersist = func() {
			e.m.stateMu.Lock()
			panic("swi21-test-hook-persist")
		}
		_, _ = e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "x", Definition: remote("https://x"), IsNew: true})
		select {}
	case "recover":
		fastRecovery(t)
		e := newSaveEnv(t)
		e.leftover(&pb.McpSaveInProgress{Name: "x", Definition: remote("https://x")})
		testHookRecover = func() {
			e.m.mu.Lock()
			panic("swi21-test-hook-recover")
		}
		e.m = e.newManager()
		select {}
	}
	for _, tc := range []struct{ mode, want string }{
		{"persist", "panic: swi21-test-hook-persist"},
		{"recover", "panic: swi21-test-hook-recover"},
		{"persist-unguarded", ""},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSavePanicCrashes$", "-test.timeout=1m")
		cmd.Env = append(os.Environ(), "SXB_PANIC_CHILD="+tc.mode)
		out, err := cmd.CombinedOutput()
		timedOut := ctx.Err() != nil
		cancel()
		if tc.want == "" {
			if !timedOut {
				t.Errorf("%s: the unguarded child exited (%v) instead of deadlocking:\n%s", tc.mode, err, out)
			}
			continue
		}
		if timedOut || err == nil || !strings.Contains(string(out), tc.want) || strings.Contains(string(out), "test timed out") {
			t.Errorf("%s: timedOut=%v err=%v\n%s", tc.mode, timedOut, err, out)
		}
	}
}

func TestSetModeReturnsClientSettingsOnly(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x"), true, nil)
	s, err := e.m.SetMode(context.Background(), pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE)
	if err != nil || s.GetDefinitions() != nil || s.GetPendingReplaces() != nil || s.GetSavesInProgress() != nil || !s.GetMarks()["gh"] {
		t.Errorf("settings = %v, %v", s, err)
	}
}
