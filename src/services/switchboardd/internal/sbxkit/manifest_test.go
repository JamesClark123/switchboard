package sbxkit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func writeSbx(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sbx")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestBuildParsesHelp(t *testing.T) {
	body := `
case "$1" in
  "--version") echo "sbx 1.2.3" ;;
  "--help")
    cat <<'HELP'
Usage: sbx [options]

Options:
  --network <MODE>   network mode for the sandbox
  --cpus <N>         cpu count
  --privileged       run privileged
HELP
    ;;
  *) exit 1 ;;
esac
`
	b := &Builder{Bin: writeSbx(t, body)}
	m, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.GetSbxVersion() != "sbx 1.2.3" {
		t.Errorf("version = %q", m.GetSbxVersion())
	}
	if len(m.GetOptions()) != 3 {
		t.Fatalf("expected 3 options, got %d: %v", len(m.GetOptions()), keysOf(m))
	}
	// Sorted: cpus, network, privileged.
	if got := keysOf(m); got[0] != "cpus" || got[1] != "network" || got[2] != "privileged" {
		t.Errorf("options not sorted: %v", got)
	}
	if m.GetOptions()[2].GetType() != "bool" {
		t.Errorf("a flag without an argument should be bool, got %q", m.GetOptions()[2].GetType())
	}
}

func TestBuildFallsBackToHelp(t *testing.T) {
	// An `options` subcommand is never consulted; help parsing is the only source.
	body := `
case "$1" in
  "--version") echo "sbx 0.9" ;;
  "options") exit 2 ;;
  "--help")
    cat <<'HELP'
Usage: sbx [options]

Options:
  --network <MODE>   network mode for the sandbox
  --privileged       run privileged
  --name <NAME>      sandbox name
HELP
    ;;
  *) exit 1 ;;
esac
`
	b := &Builder{Bin: writeSbx(t, body)}
	m, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := keysOf(m)
	if len(got) != 3 {
		t.Fatalf("expected 3 parsed flags, got %v", got)
	}
	// --privileged takes no arg => bool; --network takes an arg => string.
	byKey := map[string]*pb.OptionManifest_Option{}
	for _, o := range m.GetOptions() {
		byKey[o.GetKey()] = o
	}
	if byKey["privileged"].GetType() != "bool" {
		t.Errorf("privileged type = %q, want bool", byKey["privileged"].GetType())
	}
	if byKey["network"].GetType() != "string" {
		t.Errorf("network type = %q, want string", byKey["network"].GetType())
	}
}

func TestBuildErrorsWhenSbxMissing(t *testing.T) {
	b := &Builder{Bin: filepath.Join(t.TempDir(), "no-sbx")}
	if _, err := b.Build(context.Background()); err == nil {
		t.Error("expected Build error when sbx is absent")
	}
}

func TestValidate(t *testing.T) {
	m := &pb.OptionManifest{
		SbxVersion: "1.0",
		Options: []*pb.OptionManifest_Option{
			{Key: "network"}, {Key: "cpus"},
		},
	}
	if err := Validate(m, map[string]string{"network": `"host"`, "cpus": "2"}); err != nil {
		t.Errorf("valid options rejected: %v", err)
	}
	err := Validate(m, map[string]string{"network": `"host"`, "bogus": "1", "alsobad": "2"})
	if err == nil {
		t.Fatal("expected validation error for unknown options")
	}
	// Offending keys are named.
	if !contains(err.Error(), "bogus") || !contains(err.Error(), "alsobad") {
		t.Errorf("error should name offending keys: %v", err)
	}
	// Empty manifest is a no-op.
	if err := Validate(&pb.OptionManifest{}, map[string]string{"anything": "1"}); err != nil {
		t.Errorf("empty manifest should skip validation: %v", err)
	}
	if err := Validate(nil, map[string]string{"x": "1"}); err != nil {
		t.Errorf("nil manifest should skip validation: %v", err)
	}
}

func keysOf(m *pb.OptionManifest) []string {
	out := make([]string, 0, len(m.GetOptions()))
	for _, o := range m.GetOptions() {
		out = append(out, o.GetKey())
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
