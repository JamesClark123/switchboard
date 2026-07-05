package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// fakeSbx writes a stub `sbx` script that emulates the subcommands SbxRunner
// invokes, so the exec path is exercised without a real sandbox CLI.
func fakeSbx(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sbx")
	// create prints a multi-line human banner (like the real sbx) to prove the
	// runner uses the assigned --name as the handle, not scraped stdout.
	script := `#!/usr/bin/env bash
case "$1" in
  create) echo "layer: Already exists"; echo "Created sandbox '$3'"; echo "  run: sbx run --name $3" ;;
  stop|start) exit 0 ;;
  rm) exit 0 ;;
  status) echo "running" ;;
  clone) mkdir -p "$3" ;;             # $3 is dest
  *) echo "unknown $1" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestSbxRunnerLifecycle(t *testing.T) {
	r := &SbxRunner{Bin: fakeSbx(t)}
	ctx := context.Background()

	var logs []string
	ref, err := r.Launch(ctx, LaunchSpec{
		SandboxID:     "id-1",
		Name:          "sb1",
		WorkspacePath: "/tmp/ws/sb1",
		KitOptions:    map[string]string{"network": `"host"`, "cpus": "2"},
		SeedingMode:   pb.SeedingMode_SEEDING_MODE_DUPLICATE,
	}, func(l string) { logs = append(logs, l) })
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// The handle is the assigned --name (the human name), not the uuid or anything
	// parsed from the banner.
	if ref != "sb1" {
		t.Errorf("ref = %q, want sb1", ref)
	}
	if len(logs) == 0 {
		t.Error("expected log output captured")
	}

	if err := r.Stop(ctx, ref); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := r.Start(ctx, ref); err != nil {
		t.Errorf("Start: %v", err)
	}
	running, err := r.IsRunning(ctx, ref)
	if err != nil || !running {
		t.Errorf("IsRunning = %v, %v; want true", running, err)
	}
	if err := r.Destroy(ctx, ref); err != nil {
		t.Errorf("Destroy: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "clone-dest")
	if err := r.CloneRepo(ctx, "/some/repo", dest, nil); err != nil {
		t.Errorf("CloneRepo: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("clone dest not created: %v", err)
	}
}

func TestSbxRunnerErrorPropagation(t *testing.T) {
	// A non-existent binary makes every call fail.
	r := &SbxRunner{Bin: filepath.Join(t.TempDir(), "does-not-exist")}
	if _, err := r.Launch(context.Background(), LaunchSpec{SandboxID: "x"}, nil); err == nil {
		t.Error("expected Launch error with missing binary")
	}
	if err := r.Stop(context.Background(), "ref"); err == nil {
		t.Error("expected Stop error with missing binary")
	}
	// IsRunning treats an errored handle as not running.
	running, err := r.IsRunning(context.Background(), "ref")
	if err != nil || running {
		t.Errorf("IsRunning on bad binary = %v, %v; want false, nil", running, err)
	}
}

func TestFlagsDeterministic(t *testing.T) {
	got := flags(map[string]string{"b": "2", "a": `"x"`, "c": "true"})
	want := []string{"--a", "x", "--b", "2", "--c", "true"}
	if len(got) != len(want) {
		t.Fatalf("flags len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("flags[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
