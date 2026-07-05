package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// LaunchSpec is the runner-level description of a sandbox to start.
type LaunchSpec struct {
	SandboxID     string            // uuid registry key (cross-host identity)
	Name          string            // per-host-unique human name => sbx --name
	WorkspacePath string            // controlled-folder copy to seed from
	KitOptions    map[string]string // JSON-encoded option values (FR-014)
	SeedingMode   pb.SeedingMode
	Sources       []*pb.SourceRef
}

// Runner abstracts the host sandbox CLI (`sbx`). The daemon shells out to it; the
// interface lets tests substitute a fake. NOTE (research R6): the exact `sbx`
// subcommand surface is unverified in this environment — the method bodies below
// encode the assumed mapping and MUST be reconciled against a real `sbx`.
type Runner interface {
	// Launch seeds and starts a sandbox from spec.WorkspacePath, returning the
	// underlying container handle (container_ref) used for re-adoption.
	Launch(ctx context.Context, spec LaunchSpec, log func(string)) (string, error)
	Stop(ctx context.Context, containerRef string) error
	Start(ctx context.Context, containerRef string) error // restart from retained copy
	Destroy(ctx context.Context, containerRef string) error
	// IsRunning reports whether containerRef is a live container (re-adoption).
	IsRunning(ctx context.Context, containerRef string) (bool, error)
	// CloneRepo uses the sandbox tooling's clone option to seed dest from a repo.
	CloneRepo(ctx context.Context, repo, dest string, log func(string)) error
}

// SbxRunner is the production Runner that shells out to the host `sbx` binary.
type SbxRunner struct {
	Bin string // e.g. "sbx"
}

// flags renders kit options as deterministic `--key value` args.
func flags(opts map[string]string) []string {
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		v := strings.Trim(opts[k], `"`)
		out = append(out, "--"+k, v)
	}
	return out
}

func (r *SbxRunner) run(ctx context.Context, log func(string), args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	out, err := cmd.CombinedOutput()
	if log != nil && len(out) > 0 {
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			log(line)
		}
	}
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", r.Bin, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Launch maps to `sbx create --name <id> claude <workspace> [kit flags]`.
//
// The container handle we return (and later pass to stop/start/rm) is the
// --name we assigned, NOT anything scraped from stdout: `sbx create` prints a
// human-readable banner (image-pull layers, "Created sandbox …", connect hint),
// so capturing its output as the handle would feed that whole blob back into
// `sbx stop <handle>` and fail. sbx addresses sandboxes by name.
func (r *SbxRunner) Launch(ctx context.Context, spec LaunchSpec, log func(string)) (string, error) {
	// The sandbox is named by the human name (unique per host); the uuid stays the
	// registry key. Fall back to the id only if no name was resolved.
	name := spec.Name
	if name == "" {
		name = spec.SandboxID
	}
	// for now we default to the claude runner. We need to support specifying different runners in the future
	args := []string{"create", "--name", name, "claude", spec.WorkspacePath}
	args = append(args, flags(spec.KitOptions)...)
	if log != nil {
		log(fmt.Sprintf("launching sandbox %s with args: %v", name, args))
	}
	if _, err := r.run(ctx, log, args...); err != nil {
		return "", err
	}
	return name, nil
}

func (r *SbxRunner) Stop(ctx context.Context, ref string) error {
	_, err := r.run(ctx, nil, "stop", ref)
	return err
}

func (r *SbxRunner) Start(ctx context.Context, ref string) error {
	_, err := r.run(ctx, nil, "start", ref)
	return err
}

func (r *SbxRunner) Destroy(ctx context.Context, ref string) error {
	_, err := r.run(ctx, nil, "rm", ref, "--force")
	return err
}

func (r *SbxRunner) IsRunning(ctx context.Context, ref string) (bool, error) {
	out, err := r.run(ctx, nil, "status", ref)
	if err != nil {
		return false, nil // treat an errored/unknown handle as not running
	}
	return strings.Contains(strings.ToLower(out), "running"), nil
}

func (r *SbxRunner) CloneRepo(ctx context.Context, repo, dest string, log func(string)) error {
	_, err := r.run(ctx, log, "clone", repo, dest)
	return err
}
