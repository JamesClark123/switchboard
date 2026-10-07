package store

import (
	"fmt"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// Kit schema migration (feature 008, FR-092, research R8, contracts/kit-schema-v2.md).
//
// Switchboard authored schema 1 kits before feature 008. A legacy file is detected
// and translated to the schema 2 model when it is read; every section with a
// schema 2 equivalent is carried losslessly, anything without one is dropped AND
// reported, the load never fails, and the file is rewritten as schema 2 only when
// the developer saves it.

// Migration reports what a legacy-to-2 translation did.
type Migration struct {
	FromVersion string
	Carried     []string // section names mapped losslessly
	Dropped     []Drop   // fields with no schema 2 equivalent
	Notes       []string // follow-ups the developer should know about
}

// Drop is one discarded field and why.
type Drop struct {
	Path, Reason string
}

// Summary renders the report as one line per item for a status banner.
func (m *Migration) Summary() []string {
	if m == nil {
		return nil
	}
	out := []string{fmt.Sprintf("migrated from schema %s: carried %s", m.FromVersion, strings.Join(m.Carried, ", "))}
	for _, d := range m.Dropped {
		out = append(out, "dropped "+d.Path+" — "+d.Reason)
	}
	out = append(out, m.Notes...)
	return out
}

// decodeSpec parses spec.yaml bytes into the schema 2 model, migrating a legacy
// file and flagging an unsupported schema.
func decodeSpec(b []byte) (*Kit, *Migration, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, nil, err
	}
	version, kind := DetectSchema(raw)
	if version == "3" || kind == "workload" || kind == "set" {
		kit := &Kit{SchemaVersion: version, Kind: kind, Name: str(raw["name"]), DisplayName: str(raw["displayName"]), Description: str(raw["description"])}
		kit.Unsupported = "schema 3 kits (workloads and sets) do not compose with the runtime's built-in agents, which are schema 2 environments"
		return kit, nil, nil
	}
	if isLegacy(raw) {
		kit, mig := migrateV1(raw)
		return kit, mig, nil
	}
	var kit Kit
	if err := yaml.Unmarshal(b, &kit); err != nil {
		return nil, nil, err
	}
	return &kit, nil, nil
}

// DetectSchema reads the declared schema version and kind from a raw spec.
func DetectSchema(raw map[string]any) (version, kind string) {
	return str(raw["schemaVersion"]), str(raw["kind"])
}

// legacyKeys are the top-level keys that only schema 1 used.
var legacyKeys = []string{"network", "commands", "agentContext", "memory"}

// isLegacy reports whether raw is a schema 1 kit: declared as "1", or using any
// schema-1-only key (a hand-edited file may omit the version).
func isLegacy(raw map[string]any) bool {
	if str(raw["schemaVersion"]) == "1" {
		return true
	}
	for _, k := range legacyKeys {
		if _, ok := raw[k]; ok {
			return true
		}
	}
	if c, ok := raw["credentials"].(map[string]any); ok {
		if _, ok := c["sources"]; ok {
			return true
		}
	}
	if e, ok := raw["environment"].(map[string]any); ok {
		if _, ok := e["proxyManaged"]; ok {
			return true
		}
	}
	return false
}

// migrateV1 applies the runtime's published schema 1 → 2 mapping.
func migrateV1(raw map[string]any) (*Kit, *Migration) {
	mig := &Migration{FromVersion: "1"}
	kit := &Kit{SchemaVersion: "2", Kind: "mixin"}
	kit.Name = str(raw["name"])
	kit.DisplayName = str(raw["displayName"])
	kit.Description = str(raw["description"])
	kit.Version = str(raw["version"])
	carried := func(name string) { mig.Carried = append(mig.Carried, name) }
	known := map[string]bool{"schemaVersion": true, "kind": true, "name": true, "displayName": true, "description": true, "version": true}

	// network.allowedDomains / deniedDomains → permissions.network.allow / deny
	if n, ok := raw["network"].(map[string]any); ok {
		known["network"] = true
		perms := &KitNetworkPerms{Allow: strs(n["allowedDomains"]), Deny: strs(n["deniedDomains"])}
		if len(perms.Allow)+len(perms.Deny) > 0 {
			kit.Permissions = &KitPermissions{Network: perms}
			carried("network permissions")
		}
		dropUnknown(mig, "network", n, "allowedDomains", "deniedDomains")
	}

	// environment.variables stays; proxyManaged moves under credentials.
	var proxied []string
	if e, ok := raw["environment"].(map[string]any); ok {
		known["environment"] = true
		if vars := strmap(e["variables"]); len(vars) > 0 {
			kit.Environment = &KitEnvironment{Variables: vars}
			carried("environment")
		}
		proxied = strs(e["proxyManaged"])
		dropUnknown(mig, "environment", e, "variables", "proxyManaged")
	}
	isProxied := map[string]bool{}
	for _, p := range proxied {
		isProxied[p] = true
	}

	// credentials.sources.<id>.env[] → credentials[] {service, apiKey.name}
	if c, ok := raw["credentials"].(map[string]any); ok {
		known["credentials"] = true
		if sources, ok := c["sources"].(map[string]any); ok {
			ids := make([]string, 0, len(sources))
			for id := range sources {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				src, _ := sources[id].(map[string]any)
				env := strs(src["env"])
				cred := KitCredential{Service: id}
				if len(env) > 0 {
					cred.APIKey = &KitAPIKey{Name: env[0], ProxyManaged: isProxied[env[0]]}
					delete(isProxied, env[0])
					mig.Notes = append(mig.Notes, fmt.Sprintf("credential %s: declare the domains and header the proxy injects %s into (schema 2 apiKey.inject)", id, env[0]))
					for _, extra := range env[1:] {
						mig.Dropped = append(mig.Dropped, Drop{Path: fmt.Sprintf("credentials.sources.%s.env[%s]", id, extra), Reason: "schema 2 declares one env var per credential and no host source; values are bound on the host"})
					}
				} else {
					mig.Notes = append(mig.Notes, fmt.Sprintf("credential %s: add an API-key env var name (schema 2 requires apiKey or oauth)", id))
				}
				kit.Credentials = append(kit.Credentials, cred)
			}
			if len(ids) > 0 {
				carried("credentials")
			}
		}
		dropUnknown(mig, "credentials", c, "sources")
	}
	for _, p := range sortedKeys(isProxied) {
		mig.Dropped = append(mig.Dropped, Drop{Path: "environment.proxyManaged[" + p + "]", Reason: "no credential source declared this variable; schema 2 attaches proxyManaged to a credential"})
	}

	// commands.{install,initFiles,startup} → setup.{install,files,startup}
	if c, ok := raw["commands"].(map[string]any); ok {
		known["commands"] = true
		setup := &KitSetup{}
		decodeInto(c["install"], &setup.Install)
		decodeInto(c["initFiles"], &setup.Files)
		decodeInto(c["startup"], &setup.Startup)
		if len(setup.Install)+len(setup.Files)+len(setup.Startup) > 0 {
			kit.Setup = setup
			carried("setup (install/files/startup)")
		}
		dropUnknown(mig, "commands", c, "install", "initFiles", "startup")
	}

	// agentContext / memory → agentInstructions.content
	for _, key := range []string{"agentContext", "memory"} {
		if v := str(raw[key]); v != "" {
			known[key] = true
			if kit.AgentInstructions == nil {
				kit.AgentInstructions = &KitAgentInstructions{Content: v}
				carried("agent instructions")
			} else {
				mig.Dropped = append(mig.Dropped, Drop{Path: key, Reason: "both agentContext and memory were set; the first became agentInstructions"})
			}
		} else if _, ok := raw[key]; ok {
			known[key] = true
		}
	}

	// Anything else has no schema 2 equivalent the editor renders.
	for _, k := range sortedKeys(rawKeys(raw)) {
		if !known[k] {
			mig.Dropped = append(mig.Dropped, Drop{Path: k, Reason: "no schema 2 equivalent"})
		}
	}
	if len(mig.Carried) == 0 {
		mig.Carried = []string{"identity"}
	}
	return kit, mig
}

// --- raw-YAML helpers ---

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int, int64, float64, bool:
		return fmt.Sprint(t)
	}
	return ""
}

func strs(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s := str(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func strmap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, e := range m {
		out[k] = str(e)
	}
	return out
}

// decodeInto re-encodes a raw sub-tree through yaml so the typed item structs
// (shared by schema 1 and 2) parse it.
func decodeInto(v any, dst any) {
	if v == nil {
		return
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return
	}
	_ = yaml.Unmarshal(b, dst)
}

func dropUnknown(mig *Migration, section string, m map[string]any, known ...string) {
	ok := map[string]bool{}
	for _, k := range known {
		ok[k] = true
	}
	for _, k := range sortedKeys(rawKeys(m)) {
		if !ok[k] {
			mig.Dropped = append(mig.Dropped, Drop{Path: section + "." + k, Reason: "no schema 2 equivalent"})
		}
	}
}

func rawKeys(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
