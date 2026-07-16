package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	yaml "gopkg.in/yaml.v3"
)

// Kit is a client-authored Docker Sandboxes agent kit (feature 004).
//
// Unlike Configuration (TOML), a kit is stored as the real artifact Docker
// defines — kits/<id>/spec.yaml — so it can be hand-edited, shared, committed, or
// passed straight to `sbx kit validate`/`--kit` without a translation step. The
// struct below is the editor's model AND the YAML schema: field tags render the
// spec, so what is stored is exactly what the daemon materializes.
//
// Only `kind: mixin` is authored here. A mixin extends an existing agent, which
// is what switchboard's sandboxes are; a `kind: sandbox` kit would replace the
// agent image and entrypoint wholesale and is out of scope for the editor —
// attach one as an external source instead.
//
// The schema is Docker's and is explicitly experimental, so unknown/incoming
// fields are NOT round-tripped: the editor owns the fields it renders. See
// https://docs.docker.com/ai/sandboxes/customize/kit-reference/
type Kit struct {
	SchemaVersion string `yaml:"schemaVersion"`
	Kind          string `yaml:"kind"`
	Name          string `yaml:"name"`
	DisplayName   string `yaml:"displayName,omitempty"`
	Description   string `yaml:"description,omitempty"`

	Credentials *KitCredentials `yaml:"credentials,omitempty"`
	Network     *KitNetwork     `yaml:"network,omitempty"`
	Environment *KitEnvironment `yaml:"environment,omitempty"`
	Commands    *KitCommands    `yaml:"commands,omitempty"`
	// AgentContext is markdown appended to the agent's memory. Named `agentContext`
	// since sbx v0.32.0 (previously `memory`).
	AgentContext string `yaml:"agentContext,omitempty"`
}

// KitCommands is the section the editor centres on: what the kit runs inside the
// sandbox.
type KitCommands struct {
	// Install runs ONCE at sandbox creation, as `sh -c <command>`, before the agent
	// attaches. This is where package installs belong.
	Install []KitInstallCommand `yaml:"install,omitempty"`
	// InitFiles are written at every sandbox start, with ${WORKDIR} expanded to the
	// workspace path. Use these for anything that must be on disk before the agent
	// runs — startup commands can't be relied on for that.
	InitFiles []KitInitFile `yaml:"initFiles,omitempty"`
	// Startup runs at EVERY sandbox start, as an argv array (no shell). These must
	// be idempotent: they replay on every restart, including the ones `sbx kit add`
	// and a refresh trigger.
	Startup []KitStartupCommand `yaml:"startup,omitempty"`
}

// KitInstallCommand is a shell string run once at creation. User defaults to "0"
// (root) when omitted.
type KitInstallCommand struct {
	Command     string `yaml:"command"`
	User        string `yaml:"user,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// KitStartupCommand is an argv array run at every start. User defaults to "1000"
// (the agent) when omitted.
type KitStartupCommand struct {
	Command     []string `yaml:"command"`
	User        string   `yaml:"user,omitempty"`
	Background  bool     `yaml:"background,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

// KitInitFile is a file written into the container at start.
type KitInitFile struct {
	Path          string `yaml:"path"`
	Content       string `yaml:"content"`
	Mode          string `yaml:"mode,omitempty"`
	OnlyIfMissing bool   `yaml:"onlyIfMissing,omitempty"`
	Description   string `yaml:"description,omitempty"`
}

// KitNetwork declares the kit's egress policy. Deny rules win over allow rules,
// including across composed kits.
type KitNetwork struct {
	AllowedDomains []string `yaml:"allowedDomains,omitempty"`
	DeniedDomains  []string `yaml:"deniedDomains,omitempty"`
}

// KitEnvironment declares container env vars. ProxyManaged names are populated by
// the proxy at request time and pair with Credentials.
type KitEnvironment struct {
	Variables    map[string]string `yaml:"variables,omitempty"`
	ProxyManaged []string          `yaml:"proxyManaged,omitempty"`
}

// KitCredentials maps a service id to where its secret comes from. Secrets stay on
// the host and are injected by the proxy — no credential value is ever stored here.
type KitCredentials struct {
	Sources map[string]KitCredentialSource `yaml:"sources,omitempty"`
}

// KitCredentialSource names the env vars a service's credential may come from.
type KitCredentialSource struct {
	Env []string `yaml:"env,omitempty"`
}

// KitStore persists kits under <configDir>/kits/<id>/spec.yaml.
type KitStore struct {
	s *Store
}

// Kits returns a KitStore backed by this Store.
func (s *Store) Kits() *KitStore { return &KitStore{s: s} }

// ErrKitNotFound is returned when a kit id is absent.
var ErrKitNotFound = errors.New("kit not found")

func (k *KitStore) dir(id string) string  { return filepath.Join(k.s.Dir(), "kits", id) }
func (k *KitStore) file(id string) string { return filepath.Join(k.dir(id), "spec.yaml") }

// ID is the kit's stable identifier: its directory name, derived from Name. It is
// also the directory name the daemon materializes into, so it is constrained to
// what the daemon accepts (lowercase alphanumeric, '-', '_').
func (kit *Kit) ID() string { return slug(kit.Name) }

// Normalize fills in the fields the schema requires and drops empty sections so a
// kit with, say, no network rules doesn't render `network: {}`.
//
// The name is slugged only when it has content: slug() falls back to "config" for
// an empty string, which would quietly name a kit after the wrong entity instead of
// failing. SpecYAML rejects the empty case before calling here.
func (kit *Kit) Normalize() {
	kit.SchemaVersion = "1"
	kit.Kind = "mixin"
	if strings.TrimSpace(kit.Name) != "" {
		kit.Name = slug(kit.Name)
	} else {
		kit.Name = ""
	}
	if kit.Commands != nil && len(kit.Commands.Install) == 0 && len(kit.Commands.Startup) == 0 && len(kit.Commands.InitFiles) == 0 {
		kit.Commands = nil
	}
	if kit.Network != nil && len(kit.Network.AllowedDomains) == 0 && len(kit.Network.DeniedDomains) == 0 {
		kit.Network = nil
	}
	if kit.Environment != nil && len(kit.Environment.Variables) == 0 && len(kit.Environment.ProxyManaged) == 0 {
		kit.Environment = nil
	}
	if kit.Credentials != nil && len(kit.Credentials.Sources) == 0 {
		kit.Credentials = nil
	}
}

// SpecYAML renders the kit as spec.yaml. yaml.v3 owns quoting and block scalars
// here rather than any hand-rolled emitter: install commands and initFile contents
// are arbitrary shell and multi-line text, where escaping mistakes would produce a
// spec that parses but means something else.
func (kit *Kit) SpecYAML() (string, error) {
	if strings.TrimSpace(kit.Name) == "" {
		return "", errors.New("kit name is required")
	}
	kit.Normalize()
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(kit); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ToSpec renders the kit into the wire payload the daemon materializes.
func (kit *Kit) ToSpec() (*pb.KitSpec, error) {
	y, err := kit.SpecYAML()
	if err != nil {
		return nil, err
	}
	return &pb.KitSpec{Id: kit.ID(), SpecYaml: y}, nil
}

// ToRef wraps the kit as a KitRef for launch/attach.
func (kit *Kit) ToRef() (*pb.KitRef, error) {
	spec, err := kit.ToSpec()
	if err != nil {
		return nil, err
	}
	return &pb.KitRef{Ref: &pb.KitRef_Spec{Spec: spec}}, nil
}

// KitSourceRef wraps an external kit source (local path, .zip, git+URL, OCI ref)
// as a KitRef. sbx resolves it on the daemon host.
func KitSourceRef(source string) *pb.KitRef {
	return &pb.KitRef{Ref: &pb.KitRef_Source{Source: strings.TrimSpace(source)}}
}

// Save writes kits/<id>/spec.yaml. The rendered YAML is the stored artifact, so
// what the editor saves is byte-for-byte what the daemon materializes.
func (k *KitStore) Save(kit *Kit) (*Kit, error) {
	y, err := kit.SpecYAML()
	if err != nil {
		return nil, err
	}
	id := kit.ID()
	if err := os.MkdirAll(k.dir(id), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(k.file(id), []byte(y), 0o644); err != nil {
		return nil, err
	}
	return kit, nil
}

// Get loads a kit by id (ErrKitNotFound when absent).
func (k *KitStore) Get(id string) (*Kit, error) {
	b, err := os.ReadFile(k.file(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrKitNotFound
		}
		return nil, err
	}
	var kit Kit
	if err := yaml.Unmarshal(b, &kit); err != nil {
		return nil, err
	}
	return &kit, nil
}

// List returns all saved kits, ordered by name. A directory without a readable
// spec.yaml is skipped rather than failing the whole listing — a hand-edited kit
// must not make the picker unopenable.
func (k *KitStore) List() ([]*Kit, error) {
	entries, err := os.ReadDir(filepath.Join(k.s.Dir(), "kits"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Kit
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		kit, err := k.Get(e.Name())
		if err != nil {
			continue
		}
		out = append(out, kit)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete removes a saved kit. Deleting a missing id is not an error.
func (k *KitStore) Delete(id string) error {
	if id == "" {
		return errors.New("kit id is required")
	}
	if err := os.RemoveAll(k.dir(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Dir returns the on-disk directory of a kit, so the UI can point the user at the
// artifact for hand-editing or sharing.
func (k *KitStore) Dir(id string) string { return k.dir(id) }
