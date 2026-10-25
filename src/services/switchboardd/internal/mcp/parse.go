package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

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

// isLoopbackHost reports whether host is a loopback name or address.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

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
