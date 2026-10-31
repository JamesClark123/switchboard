package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxtable"
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
			f := sbxtable.SplitColumns(l)
			if len(f) < 2 {
				f = strings.Fields(l)
			}
			if len(f) < 2 {
				continue
			}
			rows = append(rows, assign([]sbxtable.Column{{Name: "NAME"}, {Name: "TYPE"}, {Name: "TARGET"}}, append(f[:2], strings.Join(f[2:], " "))))
		}
		return rows, nil
	}
	cols := sbxtable.HeaderColumns(lines[headerIdx])
	for _, l := range lines[headerIdx+1:] {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "-") || isNoServersLine(l) {
			continue
		}
		vals := sbxtable.SliceByOffsets(l, cols)
		if vals == nil {
			vals = sbxtable.SplitColumns(l)
		}
		if len(vals) == 0 || strings.TrimSpace(vals[0]) == "" {
			continue
		}
		rows = append(rows, assign(cols, vals))
	}
	return rows, nil
}

// assign maps column values onto a listRow by header name; unknown columns are
// ignored and missing ones stay empty.
func assign(cols []sbxtable.Column, vals []string) listRow {
	var r listRow
	for i, c := range cols {
		if i >= len(vals) {
			break
		}
		v := strings.TrimSpace(vals[i])
		switch c.Name {
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

// isLoopbackHost is the shared loopback-host rule (libs/switchboard-dial): the
// TUI's paste check uses the very same function.
func isLoopbackHost(host string) bool { return dial.IsLoopbackHost(host) }

// minCallbackPort rejects privileged ports: the relay and paste-back must never
// reach well-known services (ssh, smtp, …) on the daemon host's loopback.
const minCallbackPort = 1024

// callbackPort extracts the loopback port of the OAuth redirect_uri carried by an
// authorization URL, or 0 when there is none (no redirect_uri, a non-loopback
// host, or no explicit port). The runtime's callback listener waits there.
func callbackPort(authURL string) uint32 {
	u, err := url.Parse(authURL)
	if err != nil {
		return 0
	}
	// Exactly one redirect_uri: a second one (e.g. carried by the provider's own
	// authorization endpoint) makes it ambiguous which the listener serves, and the
	// first must not be able to steer the relay.
	vals := u.Query()["redirect_uri"]
	if len(vals) != 1 {
		return 0
	}
	ru, err := url.Parse(vals[0])
	if err != nil || !isLoopbackHost(ru.Hostname()) {
		return 0
	}
	p, err := strconv.ParseUint(ru.Port(), 10, 16)
	if err != nil || p < minCallbackPort {
		return 0
	}
	return uint32(p)
}

// callbackTarget validates a pasted redirect URL — loopback host, the announced
// port — and returns the URL to replay on the daemon host's own loopback.
func callbackTarget(raw string, port uint32) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("not an http redirect URL")
	}
	if !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("redirect URL host must be localhost")
	}
	if u.Port() != strconv.Itoa(int(port)) {
		return "", fmt.Errorf("redirect URL port must be %d", port)
	}
	u.Scheme = "http"
	u.Host = net.JoinHostPort("localhost", strconv.Itoa(int(port)))
	return u.String(), nil
}

// --- SWI-21: inspect + auth-status scopes (research R11) ---

// inspectInfo is what `sbx mcp inspect <name> --json` positively reported. A
// nil pointer / unset flag is *not reported* (FR-402). The schema is
// undocumented, so every key is optional.
type inspectInfo struct {
	headers         []string // "Name: value" as reported (raw, never sent to clients)
	headersReported bool
	url, clientID   *string
	authServer      *string
	resource        string
	image           string
	kind            string
	skipSSRF        bool
}

// parseInspect reads the inspect JSON tolerantly: an object, or an array whose
// entry for name (or only entry) is one. Unknown or invalid JSON reports nothing.
func parseInspect(out, name string) inspectInfo {
	var info inspectInfo
	raw := pickObject(out, name)
	if raw == nil {
		return info
	}
	str := func(keys ...string) *string {
		for _, k := range keys {
			if s, ok := raw[k].(string); ok {
				return &s
			}
		}
		return nil
	}
	info.url = str("url", "URL")
	info.clientID = str("client_id", "clientId")
	info.authServer = str("oauth_authorization_server", "oauthAuthorizationServer", "authorization_server")
	if s := str("resource"); s != nil {
		info.resource = *s
	}
	if s := str("image"); s != nil {
		info.image = *s
	}
	if s := str("type", "kind"); s != nil {
		info.kind = *s
	}
	if b, ok := raw["skip_ssrf_check"].(bool); ok {
		info.skipSSRF = b
	}
	if h, ok := raw["headers"]; ok {
		info.headers, info.headersReported = parseInspectHeaders(h)
		if len(info.headers) == 0 && !inspectReportsHeaders {
			info.headersReported = false
		}
	}
	return info
}

// pickObject decodes out as an object, or picks name's entry (or the only one)
// from an array of objects.
func pickObject(out, name string) map[string]any {
	trimmed := strings.TrimSpace(out)
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) == nil {
		return obj
	}
	var list []map[string]any
	if json.Unmarshal([]byte(trimmed), &list) != nil {
		return nil
	}
	for _, e := range list {
		for _, k := range []string{"server_name", "name"} {
			if s, _ := e[k].(string); s == name {
				return e
			}
		}
	}
	if len(list) == 1 {
		return list[0]
	}
	return nil
}

// parseInspectHeaders accepts a list of "Name: value" strings, a list of
// {name,value} objects, or a name → value object.
func parseInspectHeaders(v any) ([]string, bool) {
	var out []string
	switch h := v.(type) {
	case []any:
		for _, e := range h {
			switch x := e.(type) {
			case string:
				out = append(out, x)
			case map[string]any:
				n, _ := x["name"].(string)
				val, _ := x["value"].(string)
				if n == "" {
					return nil, false
				}
				out = append(out, n+": "+val)
			default:
				return nil, false
			}
		}
	case map[string]any:
		for n, e := range h {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, n+": "+s)
		}
		sort.Strings(out)
	default:
		return nil, false
	}
	return out, true
}

// parseAuthStatusScopes reads the recorded default scopes from `sbx mcp auth
// status <name> --json` when present (`default_scopes`, `recorded_scopes` or
// `scopes.default`). The 2026-10-10 real host printed only server_name +
// status, which reports nothing.
func parseAuthStatusScopes(out, name string) ([]string, bool) {
	raw := pickObject(out, name)
	if raw == nil {
		return nil, false
	}
	list := func(v any) ([]string, bool) {
		a, ok := v.([]any)
		if !ok {
			return nil, false
		}
		var s []string
		for _, e := range a {
			if str, ok := e.(string); ok {
				s = append(s, str)
			}
		}
		return s, true
	}
	for _, k := range []string{"default_scopes", "recorded_scopes"} {
		if s, ok := list(raw[k]); ok {
			return s, len(s) > 0 || authStatusReportsScopes
		}
	}
	if sc, ok := raw["scopes"].(map[string]any); ok {
		if s, ok := list(sc["default"]); ok {
			return s, len(s) > 0 || authStatusReportsScopes
		}
	}
	return nil, false
}
