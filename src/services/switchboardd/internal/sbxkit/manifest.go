// Package sbxkit introspects the host `sbx` CLI to build the full option manifest
// the client config editor renders (FR-014), and validates a config's kit options
// against it at launch (fails loudly on unknown keys).
//
// Introspection strategy (research R6): prefer a machine-readable schema if the
// CLI exposes one (`sbx options --json`); otherwise parse `sbx --help` output.
// The exact `sbx` surface is unverified in the dev environment, so both paths are
// implemented defensively and the manifest is version-stamped.
package sbxkit

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Builder builds an OptionManifest from a host `sbx` binary.
type Builder struct {
	Bin string
}

// jsonOption is the shape expected from `sbx options --json` (R6 preferred path).
type jsonOption struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	EnumValues  []string `json:"enum_values"`
	Default     string   `json:"default"`
	Required    bool     `json:"required"`
}

// run executes the sbx binary with args and returns trimmed stdout.
func (b *Builder) run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, b.Bin, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Build assembles the manifest, preferring the JSON schema path and falling back
// to help-parsing. The result is version-stamped via `sbx --version`.
func (b *Builder) Build(ctx context.Context) (*pb.OptionManifest, error) {
	version, _ := b.run(ctx, "--version") // best-effort; non-fatal

	if opts, err := b.fromJSON(ctx); err == nil && len(opts) > 0 {
		return &pb.OptionManifest{SbxVersion: version, Options: opts}, nil
	}

	opts, err := b.fromHelp(ctx)
	if err != nil {
		return nil, fmt.Errorf("introspect sbx options: %w", err)
	}
	return &pb.OptionManifest{SbxVersion: version, Options: opts}, nil
}

// fromJSON parses `sbx options --json` into typed options.
func (b *Builder) fromJSON(ctx context.Context) ([]*pb.OptionManifest_Option, error) {
	out, err := b.run(ctx, "options", "--json")
	if err != nil {
		return nil, err
	}
	var raw []jsonOption
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	opts := make([]*pb.OptionManifest_Option, 0, len(raw))
	for _, o := range raw {
		typ := o.Type
		if typ == "" {
			typ = "string"
		}
		opts = append(opts, &pb.OptionManifest_Option{
			Key:          o.Key,
			Type:         typ,
			Description:  o.Description,
			EnumValues:   o.EnumValues,
			DefaultValue: o.Default,
			Required:     o.Required,
		})
	}
	sortOptions(opts)
	return opts, nil
}

// helpLine matches a `--flag[ <ARG>]   description` line in `sbx --help` output.
// A flag taking no argument is treated as a bool option.
var helpLine = regexp.MustCompile(`^\s*--([a-zA-Z][\w-]*)(\s+[<\[]?[A-Za-z][\w-]*[>\]]?)?\s{2,}(.*)$`)

// fromHelp parses option flags out of `sbx --help`.
func (b *Builder) fromHelp(ctx context.Context) ([]*pb.OptionManifest_Option, error) {
	out, err := b.run(ctx, "--help")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var opts []*pb.OptionManifest_Option
	for _, line := range strings.Split(out, "\n") {
		m := helpLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		if seen[key] {
			continue
		}
		seen[key] = true
		typ := "bool"
		if strings.TrimSpace(m[2]) != "" {
			typ = "string"
		}
		opts = append(opts, &pb.OptionManifest_Option{
			Key:         key,
			Type:        typ,
			Description: strings.TrimSpace(m[3]),
		})
	}
	sortOptions(opts)
	return opts, nil
}

func sortOptions(opts []*pb.OptionManifest_Option) {
	sort.Slice(opts, func(i, j int) bool { return opts[i].GetKey() < opts[j].GetKey() })
}

// Validate fails loudly if kitOptions contains any key absent from the manifest
// (spec edge case: a config referencing an unsupported option fails at launch
// naming the offending key). An empty/nil manifest cannot validate, so it is a
// no-op (introspection unavailable; do not reject every option).
func Validate(m *pb.OptionManifest, kitOptions map[string]string) error {
	if m == nil || len(m.GetOptions()) == 0 {
		return nil
	}
	known := make(map[string]bool, len(m.GetOptions()))
	for _, o := range m.GetOptions() {
		known[o.GetKey()] = true
	}
	var unknown []string
	for k := range kitOptions {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unsupported sbx option(s) %s (not in the host's sbx %s manifest)",
			strings.Join(unknown, ", "), m.GetSbxVersion())
	}
	return nil
}
