package mcp

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The runtime's gateway puts every loaded server's tools in one flat namespace and
// refuses a load whose tool names clash with a server already loaded, e.g.
//
//	tool "list_issues" from server "linear" collides with an existing tool already registered for server "github"
//
// Neither `sbx mcp add/load` nor `--static-mcp` can prefix or rename tools, so the
// mitigation is filtering at the server (README › MCP tool collisions). These
// helpers recognise the diagnostic so launch logs and the attach overlay can say
// so; the raw runtime text is always kept alongside the hint.

// ToolCollision is one clashing tool: Tool from Server collides with the same
// tool already registered for Other.
type ToolCollision struct {
	Tool, Server, Other string
}

var toolCollisionRe = regexp.MustCompile(`tool "([^"]+)" from server "([^"]+)" collides with an existing tool already registered for server "([^"]+)"`)

// LooksLikeToolCollision reports whether a runtime diagnostic is a tool-name
// collision between two MCP servers.
func LooksLikeToolCollision(out string) bool {
	return strings.Contains(strings.ToLower(out), "collides with an existing tool")
}

// ParseToolCollisions extracts every (tool, server, other) triple the diagnostic
// names, in order of appearance and without duplicates.
func ParseToolCollisions(out string) []ToolCollision {
	var res []ToolCollision
	seen := map[ToolCollision]bool{}
	for _, m := range toolCollisionRe.FindAllStringSubmatch(out, -1) {
		c := ToolCollision{Tool: m[1], Server: m[2], Other: m[3]}
		if !seen[c] {
			seen[c] = true
			res = append(res, c)
		}
	}
	return res
}

// CollisionHint turns a collision diagnostic into an actionable one-liner naming
// the servers and tools involved and the remedy; "" when out is not a collision.
// When the text matches the collision wording but the triples cannot be parsed
// (the runtime reworded it), a generic hint is returned.
func CollisionHint(out string) string {
	if !LooksLikeToolCollision(out) {
		return ""
	}
	const remedy = "filter tools on one server via its headers. See README › MCP tool collisions"
	cols := ParseToolCollisions(out)
	if len(cols) == 0 {
		return "tool name collision between MCP servers; " + remedy
	}
	byOther := map[string][]string{}
	var others []string
	for _, c := range cols {
		if _, ok := byOther[c.Other]; !ok {
			others = append(others, c.Other)
		}
		byOther[c.Other] = append(byOther[c.Other], c.Tool)
	}
	sort.Strings(others)
	parts := make([]string, 0, len(others))
	for _, o := range others {
		parts = append(parts, fmt.Sprintf("%s (%s)", o, strings.Join(byOther[o], ", ")))
	}
	return "tool name collision with " + strings.Join(parts, "; ") + "; " + remedy
}
