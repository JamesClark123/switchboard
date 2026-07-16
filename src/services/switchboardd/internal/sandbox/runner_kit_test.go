package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// argRecordingSbx writes a stub `sbx` that records its argv, one arg per line, so
// tests can assert the exact command line built for kit operations. The sbx CLI is
// not installed in this environment, so these assertions are the guard against the
// documented surface drifting away from what the runner builds.
func argRecordingSbx(t *testing.T, exit int) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "sbx")
	log = filepath.Join(dir, "args.log")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > " + log + "\n"
	if exit != 0 {
		script += "echo 'spec.yaml: unknown field' >&2\n"
	}
	script += "exit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func readArgs(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("stub sbx was never invoked: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// Kits must render as a repeated `--kit <src>` on create, in the order given.
func TestLaunchRendersKitFlagsInOrder(t *testing.T) {
	bin, log := argRecordingSbx(t, 0)
	r := &SbxRunner{Bin: bin}
	if _, err := r.Launch(context.Background(), LaunchSpec{
		SandboxID:     "id-1",
		Name:          "sb1",
		WorkspacePath: "/tmp/ws/sb1",
		SeedingMode:   pb.SeedingMode_SEEDING_MODE_DUPLICATE,
		KitSources:    []string{"/kits/a", "ghcr.io/org/b:1.0"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(readArgs(t, log), " ")
	if !strings.Contains(got, "--kit /kits/a --kit ghcr.io/org/b:1.0") {
		t.Errorf("args = %q, want one --kit per source, in order", got)
	}
	// The agent and workspace still precede the kit flags.
	if !strings.HasPrefix(got, "create --name sb1 claude /tmp/ws/sb1") {
		t.Errorf("args = %q, want the create form preserved", got)
	}
}

func TestLaunchWithNoKitsAddsNoKitFlag(t *testing.T) {
	bin, log := argRecordingSbx(t, 0)
	r := &SbxRunner{Bin: bin}
	if _, err := r.Launch(context.Background(), LaunchSpec{Name: "sb1", WorkspacePath: "/w"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(readArgs(t, log), " "); strings.Contains(got, "--kit") {
		t.Errorf("args = %q, want no --kit when none are selected", got)
	}
}

// Blank sources would produce a bare `--kit` with no value, which sbx would reject.
func TestKitFlagsSkipsBlankSources(t *testing.T) {
	if got := kitFlags([]string{"", "  ", "/kits/a"}); len(got) != 2 || got[0] != "--kit" || got[1] != "/kits/a" {
		t.Errorf("kitFlags = %v, want only the real source", got)
	}
	if got := kitFlags(nil); len(got) != 0 {
		t.Errorf("kitFlags(nil) = %v, want empty", got)
	}
}

// `sbx kit add` takes the kit source POSITIONALLY — `--kit` is creation-only and is
// rejected against an existing sandbox.
func TestAddKitUsesPositionalSource(t *testing.T) {
	bin, log := argRecordingSbx(t, 0)
	r := &SbxRunner{Bin: bin}
	if err := r.AddKit(context.Background(), "sb1", "/kits/ruff", nil); err != nil {
		t.Fatal(err)
	}
	got := readArgs(t, log)
	want := []string{"kit", "add", "sb1", "/kits/ruff"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

func TestAddKitSurfacesFailure(t *testing.T) {
	bin, _ := argRecordingSbx(t, 1)
	r := &SbxRunner{Bin: bin}
	if err := r.AddKit(context.Background(), "sb1", "/kits/ruff", nil); err == nil {
		t.Error("expected a non-zero sbx exit to surface as an error")
	}
}

func TestValidateKitInvokesSbxKitValidate(t *testing.T) {
	bin, log := argRecordingSbx(t, 0)
	r := &SbxRunner{Bin: bin}
	if _, err := r.ValidateKit(context.Background(), "/kits/ruff"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(readArgs(t, log), " ")
	if got != "kit validate /kits/ruff" {
		t.Errorf("args = %q, want `kit validate /kits/ruff`", got)
	}
}

// A rejected kit must return sbx's diagnostics, not just an exit code — the editor
// shows them verbatim.
func TestValidateKitReturnsDiagnosticsOnFailure(t *testing.T) {
	bin, _ := argRecordingSbx(t, 1)
	r := &SbxRunner{Bin: bin}
	out, err := r.ValidateKit(context.Background(), "/kits/bad")
	if err == nil {
		t.Fatal("expected a non-zero exit to report an error")
	}
	if !strings.Contains(out, "unknown field") {
		t.Errorf("output = %q, want sbx's diagnostics returned", out)
	}
}
