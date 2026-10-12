package mcp

import (
	"encoding/json"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// listRow is one line of `sbx mcp ls`.
//
// Reconciled on a real host (2026-10-10): the table is
//
//	NAME     TRANSPORT     STATUS       GATEWAY                 DETAIL
//	notion   remote http   needs auth   LOCAL, managed by you   sbx mcp auth notion
//
// — there is no URL/command column. The documented `NAME TYPE URL/COMMAND` shape
// is still accepted so a future runtime that prints a target keeps working.
type listRow struct {
	name, transport, status, gateway, detail, target string
}

// parseList reads the header line to learn the column names and start offsets,
// then slices each aligned row by those offsets (values contain single spaces:
// "remote http", "needs auth", "LOCAL, managed by you"), falling back to a split
// on runs of two or more spaces for a row that is narrower or re-flowed. An
// empty body or a "no servers" message yields an empty list — never an error,
// since the host answered.
func parseList(out string) ([]listRow, error) {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	headerIdx := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(l)), "NAME") {
			headerIdx = i
			break
		}
	}
	rows := []listRow{}
	if headerIdx < 0 {
		// No header: NAME then whatever follows, split on 2+ spaces, else fields.
		for _, l := range lines {
			if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "-") || isNoServersLine(l) {
				continue
			}
			f := splitColumns(l)
			if len(f) < 2 {
				f = strings.Fields(l)
			}
			if len(f) < 2 {
				continue
			}
			rows = append(rows, assign([]column{{name: "NAME"}, {name: "TYPE"}, {name: "TARGET"}}, append(f[:2], strings.Join(f[2:], " "))))
		}
		return rows, nil
	}
	cols := headerColumns(lines[headerIdx])
	for _, l := range lines[headerIdx+1:] {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "-") || isNoServersLine(l) {
			continue
		}
		vals := sliceByOffsets(l, cols)
		if vals == nil {
			vals = splitColumns(l)
		}
		if len(vals) == 0 || strings.TrimSpace(vals[0]) == "" {
			continue
		}
		rows = append(rows, assign(cols, vals))
	}
	return rows, nil
}

type column struct {
	name  string
	start int
}

// headerColumns splits the header on runs of two or more spaces, keeping each
// column's start offset.
func headerColumns(header string) []column {
	var cols []column
	i := 0
	for i < len(header) {
		for i < len(header) && header[i] == ' ' {
			i++
		}
		if i >= len(header) {
			break
		}
		start := i
		// A column name ends at a run of two spaces (or a trailing space);
		// single spaces inside a name ("URL/COMMAND" is one, "MCP AUTH" would be too) are kept.
		for i < len(header) && !columnBreak(header, i) {
			i++
		}
		cols = append(cols, column{name: strings.ToUpper(strings.TrimSpace(header[start:i])), start: start})
	}
	return cols
}

// columnBreak reports whether position i of header starts a gap between column
// names: a space followed by another space, or a space at the very end.
func columnBreak(header string, i int) bool {
	if header[i] != ' ' {
		return false
	}
	return i+1 == len(header) || header[i+1] == ' '
}

// sliceByOffsets cuts a row at the header's column starts when the row is
// padded to them (a space right before each start); nil when it is not.
func sliceByOffsets(l string, cols []column) []string {
	for _, c := range cols[1:] {
		if c.start > len(l) || (c.start > 0 && l[c.start-1] != ' ') {
			return nil
		}
	}
	vals := make([]string, len(cols))
	for i, c := range cols {
		end := len(l)
		if i+1 < len(cols) {
			end = cols[i+1].start
		}
		vals[i] = strings.TrimSpace(slice(l, c.start, end))
	}
	return vals
}

// splitColumns splits a row on runs of two or more spaces.
func splitColumns(l string) []string {
	var out []string
	for _, part := range strings.Split(strings.TrimSpace(l), "  ") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// assign maps column values onto a listRow by header name; unknown columns are
// ignored and missing ones stay empty.
func assign(cols []column, vals []string) listRow {
	var r listRow
	for i, c := range cols {
		if i >= len(vals) {
			break
		}
		v := strings.TrimSpace(vals[i])
		switch c.name {
		case "NAME":
			r.name = v
		case "TRANSPORT", "TYPE", "KIND":
			r.transport = v
		case "STATUS", "STATE", "AUTH":
			r.status = v
		case "GATEWAY":
			r.gateway = v
		case "DETAIL", "DETAILS":
			r.detail = v
		case "URL/COMMAND", "URL", "COMMAND", "TARGET", "ENDPOINT":
			r.target = v
		}
	}
	return r
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

// kindOf maps the TRANSPORT (documented: TYPE) column: anything starting with
// "remote" ("remote http") is an endpoint the gateway connects to; every other
// transport (stdio, command, oci, local, …) runs on the host.
func kindOf(transport string) pb.McpServerKind {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(transport)), "remote") {
		return pb.McpServerKind_MCP_SERVER_KIND_REMOTE
	}
	return pb.McpServerKind_MCP_SERVER_KIND_LOCAL
}

// authFromStatus maps the STATUS column ("needs auth") when the dedicated
// status query is unavailable.
func authFromStatus(status string) pb.McpAuthState {
	return authWord(status)
}

// parseAuthStatus maps `sbx mcp auth status <name> --json`. Reconciled on a
// real host (2026-10-10): the output is a JSON ARRAY of
// {"server_name": "...", "status": "unauthorized"}; the entry for name (or the
// only entry) decides. The object shapes assumed earlier (an `authorized` bool,
// a `status`/`state` string) remain accepted. Anything else is UNKNOWN, which
// never fails a list.
func parseAuthStatus(out, name string) pb.McpAuthState {
	trimmed := strings.TrimSpace(out)
	var list []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
		var chosen map[string]any
		for _, e := range list {
			if s, _ := e["server_name"].(string); s == name {
				chosen = e
				break
			}
		}
		if chosen == nil && len(list) == 1 {
			chosen = list[0]
		}
		if chosen == nil {
			return pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
		}
		return authFromObject(chosen)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
	}
	return authFromObject(raw)
}

func authFromObject(raw map[string]any) pb.McpAuthState {
	if v, ok := raw["authorized"]; ok {
		if b, ok := v.(bool); ok {
			if b {
				return pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED
			}
			return pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED
		}
	}
	for _, key := range []string{"status", "state"} {
		if s, ok := raw[key].(string); ok {
			if st := authWord(s); st != pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN {
				return st
			}
		}
	}
	return pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
}

func authWord(s string) pb.McpAuthState {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "authorized", "valid", "ok", "active", "ready", "authenticated":
		return pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED
	case "unauthorized", "needs auth", "needs authorization", "expired", "missing", "none", "revoked", "invalid", "not authorized", "unauthenticated":
		return pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED
	}
	return pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
}

// findURL returns the first http(s) URL token in line, stripped of surrounding
// quotes and trailing punctuation. Reconciled on a real host: `sbx mcp auth`
// prints "Open this URL to authorize MCP server "notion":" and then the bare URL
// on the NEXT line, so every line is scanned.
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
