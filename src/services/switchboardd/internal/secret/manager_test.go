package secret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The documented `sbx secret ls` table (configuration/credentials, 2026) plus a
// sandbox-scoped row and a registry row.
const docsTable = "SCOPE      TYPE      NAME        SECRET\n" +
	"(global)   service   github      gho_GCaw4o****...****43qy\n" +
	"my-sb      service   anthropic   sk-ant-****\n" +
	"(global)   registry  ghcr.io     ****\n"

type env struct {
	m      *Manager
	log    string
	stdin  string
	events int
}

func newEnv(t *testing.T, cases string) *env {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sbx")
	logPath := filepath.Join(dir, "sbx.log")
	stdinPath := filepath.Join(dir, "stdin")
	script := "#!/usr/bin/env bash\necho \"$*\" >> \"" + logPath + "\"\ncase \"$2\" in\n" + cases + "\n  *) exit 0 ;;\nesac\n"
	script = strings.ReplaceAll(script, "STDIN_FILE", stdinPath)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &env{log: logPath, stdin: stdinPath}
	e.m = NewManager(&CLI{Bin: bin}, "host-1", func(ev *pb.Event) {
		if ev.GetSecretsChanged().GetHostId() != "host-1" {
			t.Errorf("event host = %q", ev.GetSecretsChanged().GetHostId())
		}
		e.events++
	})
	return e
}

func (e *env) calls(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(e.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestListParsesDocumentedTable(t *testing.T) {
	e := newEnv(t, `  ls) printf '`+strings.ReplaceAll(docsTable, "\n", `\n`)+`' ;;`)
	secrets, err := e.m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 3 {
		t.Fatalf("secrets = %+v", secrets)
	}
	gh, an, reg := secrets[0], secrets[1], secrets[2]
	if gh.GetScope() != "global" || gh.GetType() != "service" || gh.GetName() != "github" || gh.GetMaskedValue() != "gho_GCaw4o****...****43qy" {
		t.Errorf("github row = %+v", gh)
	}
	if an.GetScope() != "my-sb" || an.GetName() != "anthropic" {
		t.Errorf("sandbox row = %+v", an)
	}
	if reg.GetType() != "registry" || reg.GetName() != "ghcr.io" {
		t.Errorf("registry row = %+v", reg)
	}
	if e.calls(t)[0] != "secret ls" {
		t.Errorf("argv = %v", e.calls(t))
	}
	if e.events != 0 {
		t.Error("a list must not emit")
	}
}

func TestParseListShapes(t *testing.T) {
	// Older docs: SCOPE SERVICE SECRET, no TYPE column ⇒ service.
	old := "SCOPE      SERVICE   SECRET\n(global)   github    gho_TK421****...****R2D2\n"
	got := parseList(old)
	if len(got) != 1 || got[0].GetName() != "github" || got[0].GetType() != "service" || got[0].GetScope() != "global" {
		t.Errorf("old shape = %+v", got)
	}
	for _, in := range []string{"", "No secrets stored.\n", "SCOPE  TYPE  NAME  SECRET\n"} {
		if got := parseList(in); len(got) != 0 {
			t.Errorf("parseList(%q) = %+v, want none", in, got)
		}
	}
	if normalizeScope("(global)") != "global" || normalizeScope("") != "global" || normalizeScope("(my-sb)") != "my-sb" || normalizeScope("my-sb") != "my-sb" {
		t.Error("normalizeScope")
	}
}

func TestSetPipesValueOnStdinAndEmits(t *testing.T) {
	e := newEnv(t, `  set) cat > STDIN_FILE; echo "Secret stored." ;;
  ls) printf '`+strings.ReplaceAll(docsTable, "\n", `\n`)+`' ;;`)
	ctx := context.Background()
	s, err := e.m.Set(ctx, &pb.SetSecretRequest{Name: " github ", Value: "gho_secret"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(e.stdin); string(got) != "gho_secret\n" {
		t.Errorf("stdin = %q, want the value plus newline", got)
	}
	calls := e.calls(t)
	if calls[0] != "secret set github --force" {
		t.Errorf("argv = %q", calls[0])
	}
	if strings.Contains(strings.Join(calls, " "), "gho_secret") {
		t.Error("the value must never appear in argv")
	}
	if s.GetName() != "github" || s.GetScope() != "global" || s.GetMaskedValue() != "gho_GCaw4o****...****43qy" {
		t.Errorf("returned row = %+v (should be the runtime's masked row)", s)
	}
	if e.events != 1 {
		t.Errorf("events = %d, want 1", e.events)
	}

	// Sandbox scope, and a row the list does not show falls back to a masked stub.
	s, err = e.m.Set(ctx, &pb.SetSecretRequest{Name: "openai", Value: "sk-1", Sandbox: "other-sb"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(e.calls(t), "secret set openai --sandbox other-sb --force") {
		t.Errorf("argv = %v", e.calls(t))
	}
	if s.GetScope() != "other-sb" || s.GetMaskedValue() != "****" || s.GetType() != "service" {
		t.Errorf("fallback row = %+v", s)
	}
}

func TestSetRejectsBadInputBeforeAnyCall(t *testing.T) {
	e := newEnv(t, `  set) cat > STDIN_FILE ;;`)
	ctx := context.Background()
	for _, req := range []*pb.SetSecretRequest{
		{Name: "", Value: "x"},
		{Name: "-bad", Value: "x"},
		{Name: "has space", Value: "x"},
		{Name: "github", Value: "   "},
		{Name: "github", Value: "line1\nline2"},
		{Name: "github", Value: "x", Sandbox: "bad name"},
	} {
		if _, err := e.m.Set(ctx, req); !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%+v: err = %v, want ErrInvalidArgs", req, err)
		}
	}
	if _, err := os.Stat(e.log); !os.IsNotExist(err) {
		t.Error("no CLI call may happen for a refused request")
	}
	if e.events != 0 {
		t.Error("no emit for refused requests")
	}
}

func TestSetFailureIsVerbatimWithoutTheValue(t *testing.T) {
	e := newEnv(t, `  set) cat > STDIN_FILE; echo "error: keychain locked" >&2; exit 1 ;;`)
	_, err := e.m.Set(context.Background(), &pb.SetSecretRequest{Name: "github", Value: "topsecret"})
	if err == nil || !strings.Contains(err.Error(), "keychain locked") || strings.Contains(err.Error(), "topsecret") {
		t.Errorf("err = %v", err)
	}
	if e.events != 0 {
		t.Error("a failed set must not emit")
	}
}

func TestRemoveArgvAndErrors(t *testing.T) {
	e := newEnv(t, `  rm) case "$*" in *ghost*) echo 'error: secret "ghost" not found' >&2; exit 1 ;; esac; echo removed ;;`)
	ctx := context.Background()
	if err := e.m.Remove(ctx, &pb.RemoveSecretRequest{Name: "github"}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Remove(ctx, &pb.RemoveSecretRequest{Name: "anthropic", Sandbox: "my-sb"}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Remove(ctx, &pb.RemoveSecretRequest{Name: "ghcr.io", Type: "registry"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"secret rm github -f", "secret rm anthropic --sandbox my-sb -f", "secret rm --registry ghcr.io -f"}
	if got := e.calls(t); strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("argv = %v, want %v", got, want)
	}
	if e.events != 3 {
		t.Errorf("events = %d, want 3", e.events)
	}
	err := e.m.Remove(ctx, &pb.RemoveSecretRequest{Name: "ghost"})
	if err == nil || !LooksNotFound(err.Error()) {
		t.Errorf("unknown secret err = %v", err)
	}
	if err := e.m.Remove(ctx, &pb.RemoveSecretRequest{Name: "x", Type: "custom"}); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("custom err = %v", err)
	}
	if err := e.m.Remove(ctx, &pb.RemoveSecretRequest{Name: "bad name"}); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("bad name err = %v", err)
	}
	if err := e.m.Remove(ctx, &pb.RemoveSecretRequest{Name: "github", Sandbox: "bad sb"}); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("bad sandbox err = %v", err)
	}
	if e.events != 3 {
		t.Error("failures must not emit")
	}
}

func TestArgvTable(t *testing.T) {
	cases := map[string][]string{
		"secret ls":                                 argvList(),
		"secret set github --force":                 argvSet("github", ""),
		"secret set mcp:gh:token --force":           argvSet("mcp:gh:token", ""),
		"secret set openai --sandbox my-sb --force": argvSet("openai", "my-sb"),
		"secret rm github -f":                       argvRemove("github", "", "service"),
		"secret rm github -f ":                      append(argvRemove("github", "", ""), ""),
		"secret rm --registry ghcr.io -f":           argvRemove("ghcr.io", "", "registry"),
		"secret rm anthropic --sandbox my-sb -f":    argvRemove("anthropic", "my-sb", "service"),
	}
	for want, got := range cases {
		if strings.TrimSpace(strings.Join(got, " ")) != strings.TrimSpace(want) {
			t.Errorf("argv = %q, want %q", strings.Join(got, " "), want)
		}
	}
	if NewManager(&CLI{}, "h", nil).emit != nil {
		t.Error("nil emit stays nil")
	}
	NewManager(&CLI{}, "h", nil).emitChanged() // must not panic
	if outputTail(nil) != "" || outputTail([]byte("a\nb\nc\nd\ne\n")) != ": b | c | d | e" {
		t.Errorf("outputTail = %q / %q", outputTail(nil), outputTail([]byte("a\nb\nc\nd\ne\n")))
	}
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}
