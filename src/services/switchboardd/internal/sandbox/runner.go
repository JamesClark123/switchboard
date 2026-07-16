package sandbox

import (
	"context"
	"encoding/json"
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
	// KitSources are agent-kit sources rendered as one `--kit <src>` each
	// (feature 004, FR-032): a materialized local path, a .zip, a git+URL, or an
	// OCI ref. Only honoured at creation — sbx rejects `--kit` against an existing
	// sandbox; use AddKit for that.
	KitSources []string
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
	// AddKit attaches a kit to an existing sandbox (`sbx kit add`). sbx restarts
	// the sandbox to apply it, preserving VM state.
	AddKit(ctx context.Context, containerRef, kitSource string, log func(string)) error
	// ValidateKit checks a materialized kit directory (`sbx kit validate`),
	// returning sbx's diagnostics verbatim when it rejects the kit.
	ValidateKit(ctx context.Context, kitDir string) (string, error)
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

// kitFlags renders each kit source as a repeated `--kit <src>` pair, in the order
// given: sbx composes stacked kits, and the author's order is meaningful.
func kitFlags(sources []string) []string {
	out := make([]string, 0, len(sources)*2)
	for _, s := range sources {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, "--kit", s)
		}
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
	args = append(args, kitFlags(spec.KitSources)...)
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

// IsRunning reports whether ref (a sandbox name or id) is a live container.
//
// It queries `sbx ls --json` and matches the ref against each sandbox's name or
// id. sbx has no `status` subcommand (verified against sbx v0.33.0: `sbx status
// <ref>` exits non-zero with usage text), so re-adoption must read the list.
func (r *SbxRunner) IsRunning(ctx context.Context, ref string) (bool, error) {
	out, err := r.run(ctx, nil, "ls", "--json")
	if err != nil {
		return false, nil // treat an errored/unknown runtime as not running
	}
	var listing struct {
		Sandboxes []struct {
			Name   string `json:"name"`
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"sandboxes"`
	}
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		return false, nil // unparseable listing => treat as not running
	}
	for _, s := range listing.Sandboxes {
		if s.Name == ref || s.ID == ref {
			return strings.EqualFold(s.Status, "running"), nil
		}
	}
	return false, nil
}

func (r *SbxRunner) CloneRepo(ctx context.Context, repo, dest string, log func(string)) error {
	_, err := r.run(ctx, log, "clone", repo, dest)
	return err
}

// AddKit maps to `sbx kit add <sandbox> <kit-source>` (feature 004, FR-033).
//
// The kit source is POSITIONAL here, unlike the `--kit <src>` flag `sbx create`
// takes: sbx rejects `--kit` against an existing sandbox with "--kit can only be
// used when creating a new sandbox". Verified against the sbx docs (kits.md);
// `sbx` is not installed in the dev environment, so this and ValidateKit are the
// two call-sites to reconcile first if the surface has moved.
//
// sbx restarts the sandbox to apply the kit; VM state (installed packages, images,
// volumes, agent history) is preserved across that restart. Kits cannot be removed
// from a running sandbox — the sandbox must be destroyed and recreated.
func (r *SbxRunner) AddKit(ctx context.Context, ref, kitSource string, log func(string)) error {
	_, err := r.run(ctx, log, "kit", "add", ref, kitSource)
	return err
}

// ValidateKit maps to `sbx kit validate <path>`. sbx reports schema errors and
// deprecation warnings on a non-zero exit; its combined output is returned so the
// client can surface the diagnostics verbatim rather than a generic failure.
func (r *SbxRunner) ValidateKit(ctx context.Context, kitDir string) (string, error) {
	out, err := exec.CommandContext(ctx, r.Bin, "kit", "validate", kitDir).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
