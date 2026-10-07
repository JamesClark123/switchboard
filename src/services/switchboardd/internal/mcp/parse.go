package mcp

import (
	"encoding/json"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// listRow is one line of `sbx mcp ls`.
type listRow struct {
	name, typ, target string
}

// parseList reads the documented `NAME  TYPE  URL/COMMAND` table. Column starts
// are taken from the header line, so values containing single spaces (a command
// line) survive and extra trailing columns are ignored. With no header the lines
// are split on whitespace runs as a fallback; an empty body or a "no servers"
// message yields an empty list — never an error, since the host answered.
func parseList(out string) ([]listRow, error) {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	headerIdx := -1
	for i, l := range lines {
		u := strings.ToUpper(l)
		if strings.Contains(u, "NAME") && strings.Contains(u, "TYPE") {
			headerIdx = i
			break
		}
	}
	rows := []listRow{}
	if headerIdx < 0 {
		for _, l := range lines {
			f := strings.Fields(l)
			if len(f) < 2 || strings.HasPrefix(f[0], "-") || isNoServersLine(l) {
				continue
			}
			rows = append(rows, listRow{name: f[0], typ: f[1], target: strings.Join(f[2:], " ")})
		}
		return rows, nil
	}
	header := lines[headerIdx]
	nameAt := strings.Index(strings.ToUpper(header), "NAME")
	typeAt := strings.Index(strings.ToUpper(header), "TYPE")
	targetAt := nextColumnStart(header, typeAt+len("TYPE"))
	for _, l := range lines[headerIdx+1:] {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "-") || isNoServersLine(l) {
			continue
		}
		var row listRow
		// Header offsets apply only when the row is padded to them (a space sits
		// right before each column start); a narrower or re-flowed row is split on
		// whitespace runs instead, so a value is never cut mid-word.
		aligned := typeAt > 0 && len(l) > targetAt && l[typeAt-1] == ' ' && l[targetAt-1] == ' '
		if aligned {
			row = listRow{
				name:   strings.TrimSpace(slice(l, nameAt, typeAt)),
				typ:    strings.TrimSpace(slice(l, typeAt, targetAt)),
				target: strings.TrimSpace(slice(l, targetAt, len(l))),
			}
		}
		if !aligned || row.name == "" || row.typ == "" || row.target == "" || strings.ContainsAny(row.name, " \t") {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			row = listRow{name: f[0], typ: f[1], target: strings.Join(f[2:], " ")}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// nextColumnStart finds where the column after position from begins: the first
// non-space after the next run of spaces.
func nextColumnStart(header string, from int) int {
	i := from
	for i < len(header) && header[i] != ' ' {
		i++
	}
	for i < len(header) && header[i] == ' ' {
		i++
	}
	return i
}

func slice(s string, from, to int) string {
	if from < 0 {
		from = 0
	}
	if from > len(s) {
		return ""
	}
	if to > len(s) || to < from {
		to = len(s)
	}
	return s[from:to]
}

func isNoServersLine(l string) bool {
	u := strings.ToLower(strings.TrimSpace(l))
	return strings.HasPrefix(u, "no ") && strings.Contains(u, "server")
}

// kindOf maps the TYPE column: the docs show "remote" for endpoint URLs; every
// other type (command, oci, local, …) runs on the host.
func kindOf(typ string) pb.McpServerKind {
	if strings.EqualFold(strings.TrimSpace(typ), "remote") {
		return pb.McpServerKind_MCP_SERVER_KIND_REMOTE
	}
	return pb.McpServerKind_MCP_SERVER_KIND_LOCAL
}

// parseAuthStatus maps `sbx mcp auth status <name> --json`. The field names are
// not pinned by the docs (reconciliation item, research R9), so the parser
// accepts the plausible shapes — an `authorized` bool, or a `status`/`state`
// string — and answers UNKNOWN for anything else. UNKNOWN never fails a list.
func parseAuthStatus(out string) pb.McpAuthState {
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
	}
	if v, ok := raw["authorized"]; ok {
		if b, ok := v.(bool); ok {
			if b {
				return pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED
			}
			return pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED
		}
	}
	for _, key := range []string{"status", "state"} {
		s, ok := raw[key].(string)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "authorized", "valid", "ok", "active":
			return pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED
		case "unauthorized", "expired", "missing", "none", "revoked", "invalid", "not authorized":
			return pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED
		}
	}
	return pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
}

// findURL returns the first http(s) URL token in line, stripped of surrounding
// quotes and trailing punctuation — the shape of the runtime's "Open this URL to
// authorize …: https://…" line.
func findURL(line string) string {
	for _, tok := range strings.Fields(line) {
		t := strings.TrimLeft(tok, `"'<([`)
		t = strings.TrimRight(t, `.,;:!"'>)]`)
		if strings.HasPrefix(t, "https://") || strings.HasPrefix(t, "http://") {
			return t
		}
	}
	return ""
}
