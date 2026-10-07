package mcp

import (
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestParseListDocumentedTable(t *testing.T) {
	out := "NAME                 TYPE     URL/COMMAND\n" +
		"notion               remote   https://mcp.notion.com/mcp\n" +
		"playwright           command  npx @playwright/mcp@latest\n" +
		"fetch                oci      ghcr.io/x/fetch:1  extra-col\n"
	rows, err := parseList(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0] != (listRow{"notion", "remote", "https://mcp.notion.com/mcp"}) {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].target != "npx @playwright/mcp@latest" {
		t.Errorf("a command line with spaces must survive: %q", rows[1].target)
	}
	if rows[2].typ != "oci" {
		t.Errorf("row 2 = %+v", rows[2])
	}
}

func TestParseListEmptyAndFallbacks(t *testing.T) {
	for _, in := range []string{"", "\n", "NAME  TYPE  URL/COMMAND\n", "No MCP servers registered.\n"} {
		rows, err := parseList(in)
		if err != nil || len(rows) != 0 {
			t.Errorf("parseList(%q) = %+v, %v; want empty", in, rows, err)
		}
	}
	// No header at all: whitespace fields.
	rows, _ := parseList("notion remote https://x/mcp\n----\n")
	if len(rows) != 1 || rows[0].name != "notion" || rows[0].target != "https://x/mcp" {
		t.Errorf("headerless fallback = %+v", rows)
	}
	// Re-flowed line narrower than the header: whitespace fields.
	rows, _ = parseList("NAME                 TYPE     URL/COMMAND\nn remote https://y\n")
	if len(rows) != 1 || rows[0] != (listRow{"n", "remote", "https://y"}) {
		t.Errorf("reflowed row = %+v", rows)
	}
}

func TestKindOf(t *testing.T) {
	if kindOf("remote") != pb.McpServerKind_MCP_SERVER_KIND_REMOTE || kindOf(" Remote ") != pb.McpServerKind_MCP_SERVER_KIND_REMOTE {
		t.Error("remote should map to REMOTE")
	}
	for _, typ := range []string{"command", "oci", "local", ""} {
		if kindOf(typ) != pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
			t.Errorf("%q should map to LOCAL", typ)
		}
	}
}

func TestParseAuthStatus(t *testing.T) {
	cases := map[string]pb.McpAuthState{
		`{"authorized": true}`:      pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED,
		`{"authorized": false}`:     pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED,
		`{"status": "authorized"}`:  pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED,
		`{"state": "Expired"}`:      pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED,
		`{"status": "weird"}`:       pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		`not json`:                  pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		``:                          pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		`{"scopes": ["read"]}`:      pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		`{"authorized": "yes"}`:     pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		` {"status":"valid"} `:      pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED,
		`{"state":"missing","x":1}`: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED,
	}
	for in, want := range cases {
		if got := parseAuthStatus(in); got != want {
			t.Errorf("parseAuthStatus(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFindURL(t *testing.T) {
	cases := map[string]string{
		`Open this URL to authorize MCP server "notion": https://api.notion.com/v1/oauth/authorize?x=1`: "https://api.notion.com/v1/oauth/authorize?x=1",
		`Visit "https://example.test/auth".`: "https://example.test/auth",
		`(http://localhost:1234/cb)`:         "http://localhost:1234/cb",
		`Resolving MCP server "notion"...`:   "",
		``:                                   "",
	}
	for in, want := range cases {
		if got := findURL(in); got != want {
			t.Errorf("findURL(%q) = %q, want %q", in, got, want)
		}
	}
}
