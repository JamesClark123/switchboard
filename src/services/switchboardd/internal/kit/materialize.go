// Package kit materializes client-authored agent kits onto this host so the local
// `sbx` binary can consume them as local-path kit sources (feature 004).
//
// Kits are owned CLIENT-side, like configs (see the contract header in
// switchboard.proto): the client authors and stores them and ships the rendered
// spec.yaml over the wire. The daemon's only job is to lay that YAML down in a
// directory `sbx` can read. Nothing here parses or validates the schema — the host
// `sbx kit validate` is the authority, so Docker's (explicitly experimental) kit
// schema can evolve without a daemon change.
package kit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Materializer writes kit directories under Root.
type Materializer struct {
	Root string // e.g. $HOME/switchboard/kits
}

// safeID reports whether id is usable as a single directory name. Kit ids arrive
// from a network peer and are joined onto Root, so anything that could traverse
// out of it ("..", separators, absolute paths) or produce a surprising directory
// is rejected rather than sanitized — a silently-rewritten id would materialize a
// kit the client can't correlate with what it sent.
func safeID(id string) error {
	switch {
	case id == "":
		return fmt.Errorf("kit id is required")
	case len(id) > 64:
		return fmt.Errorf("kit id %q is too long (max 64)", id)
	case id != filepath.Base(id), id == ".", id == "..":
		return fmt.Errorf("kit id %q must be a single path segment", id)
	case strings.ContainsAny(id, `/\`), filepath.IsAbs(id):
		return fmt.Errorf("kit id %q must not contain a path separator", id)
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return fmt.Errorf("kit id %q must be lowercase alphanumeric, '-' or '_'", id)
		}
	}
	return nil
}

// Dir returns the directory a kit id materializes into.
func (m *Materializer) Dir(id string) (string, error) {
	if err := safeID(id); err != nil {
		return "", err
	}
	if m.Root == "" {
		return "", fmt.Errorf("kit root is not configured")
	}
	return filepath.Join(m.Root, id), nil
}

// Write lays spec.yaml down at <Root>/<id>/spec.yaml and returns the directory,
// which is what gets passed to sbx as the kit source.
//
// The directory is rebuilt from scratch on every call so that an edited kit never
// leaves a stale file behind — the client is the source of truth, and this is a
// cache of it, not a store.
func (m *Materializer) Write(spec *pb.KitSpec) (string, error) {
	dir, err := m.Dir(spec.GetId())
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(spec.GetSpecYaml()) == "" {
		return "", fmt.Errorf("kit %q has an empty spec.yaml", spec.GetId())
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear kit dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create kit dir: %w", err)
	}
	path := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(path, []byte(spec.GetSpecYaml()), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return dir, nil
}

// Resolve turns a KitRef into the source string to hand sbx: an inline spec is
// materialized to a local directory; an external source (local path, .zip,
// git+URL, OCI ref) is passed through untouched for sbx to resolve itself.
func (m *Materializer) Resolve(ref *pb.KitRef) (string, error) {
	switch r := ref.GetRef().(type) {
	case *pb.KitRef_Spec:
		return m.Write(r.Spec)
	case *pb.KitRef_Source:
		src := strings.TrimSpace(r.Source)
		if src == "" {
			return "", fmt.Errorf("kit source is empty")
		}
		return src, nil
	default:
		return "", fmt.Errorf("kit reference has neither a spec nor a source")
	}
}

// ResolveAll resolves refs in order; sbx composes stacked kits and the author's
// order is meaningful, so it is preserved.
func (m *Materializer) ResolveAll(refs []*pb.KitRef) ([]string, error) {
	out := make([]string, 0, len(refs))
	for i, ref := range refs {
		src, err := m.Resolve(ref)
		if err != nil {
			return nil, fmt.Errorf("kit %d: %w", i+1, err)
		}
		out = append(out, src)
	}
	return out, nil
}
