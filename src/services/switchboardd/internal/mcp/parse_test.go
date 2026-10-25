package mcp

import (
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The real `sbx mcp ls` table as a host printed it on 2026-10-10.
const realTable = "NAME     TRANSPORT     STATUS       GATEWAY                 DETAIL\n" +
	"notion   remote http   needs auth   LOCAL, managed by you   sbx mcp auth notion\n" +
	"pw       stdio         ready        LOCAL, managed by you   npx @playwright/mcp@latest\n"

func TestParseListRealTable(t *testing.T) {
	rows, err := parseList(realTable)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	want := listRow{name: "notion", transport: "remote http", status: "needs auth", gateway: "LOCAL, managed by you", detail: "sbx mcp auth notion"}
	if rows[0] != want {
		t.Errorf("row 0 = %+v, want %+v", rows[0], want)
	}
	if rows[1].name != "pw" || rows[1].transport != "stdio" || rows[1].status != "ready" || rows[1].detail != "npx @playwright/mcp@latest" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if kindOf(rows[0].transport) != pb.McpServerKind_MCP_SERVER_KIND_REMOTE || kindOf(rows[1].transport) != pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
		t.Error("kind should follow the TRANSPORT column")
	}
	if authFromStatus(rows[0].status) != pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED || authFromStatus(rows[1].status) != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED {
		t.Error("the STATUS column should map needs auth / ready")
	}
}

// The documented shape (with a target column) is still accepted.
func TestParseListDocumentedTable(t *testing.T) {
	out := "NAME                 TYPE     URL/COMMAND\n" +
		"notion               remote   https://mcp.notion.com/mcp\n" +
		"playwright           command  npx @playwright/mcp@latest\n" +
		"fetch                oci      ghcr.io/x/fetch:1\n"
	rows, err := parseList(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].name != "notion" || rows[0].transport != "remote" || rows[0].target != "https://mcp.notion.com/mcp" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].target != "npx @playwright/mcp@latest" {
		t.Errorf("a command line with spaces must survive: %q", rows[1].target)
	}
	if rows[2].transport != "oci" {
		t.Errorf("row 2 = %+v", rows[2])
	}
}

func TestParseListEmptyAndFallbacks(t *testing.T) {
	for _, in := range []string{"", "\n", "NAME  TYPE  URL/COMMAND\n", "NAME     TRANSPORT     STATUS       GATEWAY                 DETAIL\n", "No MCP servers registered.\n"} {
		rows, err := parseList(in)
		if err != nil || len(rows) != 0 {
			t.Errorf("parseList(%q) = %+v, %v; want empty", in, rows, err)
		}
	}
	// No header at all: name, type, then the rest.
	rows, _ := parseList("notion  remote  https://x/mcp\n----\n")
	if len(rows) != 1 || rows[0].name != "notion" || rows[0].target != "https://x/mcp" {
		t.Errorf("headerless fallback = %+v", rows)
	}
	// Re-flowed line narrower than the header: split on 2+ spaces.
	rows, _ = parseList("NAME     TRANSPORT     STATUS       GATEWAY                 DETAIL\nn  remote http  needs auth  LOCAL  sbx mcp auth n\n")
	if len(rows) != 1 || rows[0].name != "n" || rows[0].transport != "remote http" || rows[0].detail != "sbx mcp auth n" {
		t.Errorf("reflowed row = %+v", rows)
	}
}

func TestKindOf(t *testing.T) {
	for _, tr := range []string{"remote", " Remote ", "remote http", "remote sse"} {
		if kindOf(tr) != pb.McpServerKind_MCP_SERVER_KIND_REMOTE {
			t.Errorf("%q should map to REMOTE", tr)
		}
	}
	for _, tr := range []string{"stdio", "command", "oci", "local", ""} {
		if kindOf(tr) != pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
			t.Errorf("%q should map to LOCAL", tr)
		}
	}
}

func TestParseAuthStatus(t *testing.T) {
	cases := map[string]pb.McpAuthState{
		// The real shape (2026-10-10): an array keyed by server_name.
		`[{"server_name":"notion","status":"unauthorized"}]`:                                          pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED,
		`[{"server_name":"notion","status":"authorized"}]`:                                            pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED,
		`[{"server_name":"other","status":"authorized"},{"server_name":"notion","status":"expired"}]`: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED,
		`[{"server_name":"other","status":"authorized"},{"server_name":"x","status":"ok"}]`:           pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		`[]`: pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		// Earlier-assumed object shapes stay accepted.
		`{"authorized": true}`:      pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED,
		`{"authorized": false}`:     pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED,
		`{"status": "authorized"}`:  pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED,
		`{"state": "Expired"}`:      pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED,
		`{"status": "weird"}`:       pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		`not json`:                  pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		``:                          pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		`{"authorized": "yes"}`:     pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN,
		`{"state":"missing","x":1}`: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED,
	}
	for in, want := range cases {
		if got := parseAuthStatus(in, "notion"); got != want {
			t.Errorf("parseAuthStatus(%q) = %v, want %v", in, got, want)
		}
	}
}

// The real `sbx mcp auth` transcript: scope lines, the announcement, then the
// bare URL on its own line.
const realAuthTranscript = `OAuth scopes for MCP server "notion":
   requested: default
              (from the resource's required set; --no-scope requests none)
    required: default
  advertised: default
Open this URL to authorize MCP server "notion":
https://mcp.notion.com/authorize?client_id=GGHuqOMWx5gcpA8V&code_challenge=9dySPtqVFFoI-n4LKKJc9mCyRCwO5KWn2zuPDskgYbQ&code_challenge_method=S256&redirect_uri=http%3A%2F%2F127.0.0.1%3A64530%2Fcallback&resource=https%3A%2F%2Fmcp.notion.com%2Fmcp&response_type=code&scope=default&state=zhuZ_EUZ9i1cEJAsum378RTknRU2bt5ojqBJDv2aNo8`

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
	// Real transcript: exactly one line carries the URL, intact.
	var found []string
	for _, l := range strings.Split(realAuthTranscript, "\n") {
		if u := findURL(l); u != "" {
			found = append(found, u)
		}
	}
	if len(found) != 1 || !strings.HasPrefix(found[0], "https://mcp.notion.com/authorize?client_id=") || !strings.HasSuffix(found[0], "state=zhuZ_EUZ9i1cEJAsum378RTknRU2bt5ojqBJDv2aNo8") {
		t.Errorf("real transcript URLs = %q", found)
	}
}

func TestCallbackPort(t *testing.T) {
	cases := map[string]uint32{
		"https://auth.example.com/authorize?client_id=x&redirect_uri=http%3A%2F%2Flocalhost%3A34567%2Fcallback":                             34567,
		"https://auth.example.com/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A8080%2Fcb":                                                8080,
		"https://auth.example.com/authorize?redirect_uri=http%3A%2F%2F%5B%3A%3A1%5D%3A9000%2Fcb":                                            9000,
		"https://auth.example.com/authorize?client_id=x":                                                                                    0, // no redirect_uri
		"https://auth.example.com/authorize?redirect_uri=https%3A%2F%2Fexample.com%3A443%2Fcb":                                              0, // not loopback
		"https://auth.example.com/authorize?redirect_uri=http%3A%2F%2Flocalhost%2Fcb":                                                       0, // no port
		"https://auth.example.com/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A22%2Fcb":                                                  0, // privileged port
		"https://auth.example.com/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A22%2Fcb&redirect_uri=http%3A%2F%2Flocalhost%3A34567%2Fcb": 0, // ambiguous: two redirect_uri
		"::not a url": 0,
	}
	for in, want := range cases {
		if got := callbackPort(in); got != want {
			t.Errorf("callbackPort(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCallbackTarget(t *testing.T) {
	got, err := callbackTarget(" http://localhost:34567/callback?code=abc&state=s ", 34567)
	if err != nil || got != "http://localhost:34567/callback?code=abc&state=s" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{
		"http://evil.example.com:34567/cb", // not loopback
		"http://localhost:1/cb",            // not the announced port
		"ftp://localhost:34567/cb",         // not http
		"nonsense",
	} {
		if _, err := callbackTarget(bad, 34567); err == nil {
			t.Errorf("callbackTarget(%q) should be rejected", bad)
		}
	}
}

// Review (PR #1, run 41): URL hosts are case-insensitive.
func TestLocalhostSubdomainsAndTrailingDotAreLoopback(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST", "localhost.", "app.localhost", "App.LocalHost.", "127.0.0.1", "::1"} {
		if !isLoopbackHost(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"", "example.com", "localhost.example.com", "notlocalhost", "10.0.0.1", "evil.com.localhost.evil.com"} {
		if isLoopbackHost(h) {
			t.Errorf("%q must not be loopback", h)
		}
	}
	if got := callbackPort("https://auth.example.com/authorize?redirect_uri=http%3A%2F%2Fapp.localhost%3A34567%2Fcb"); got != 34567 {
		t.Errorf("callbackPort for *.localhost = %d, want 34567", got)
	}
	// The replay always goes to the fixed host, whatever loopback spelling was pasted.
	if got, err := callbackTarget("http://app.localhost:34567/cb?c=1", 34567); err != nil || got != "http://localhost:34567/cb?c=1" {
		t.Errorf("callbackTarget = %q, %v", got, err)
	}
}

func TestLoopbackHostsAreCaseInsensitive(t *testing.T) {
	if got := callbackPort("https://auth.example.com/authorize?redirect_uri=http%3A%2F%2FLOCALHOST%3A34567%2Fcb"); got != 34567 {
		t.Errorf("callbackPort with an upper-case host = %d, want 34567", got)
	}
	got, err := callbackTarget("http://LocalHost:34567/cb?code=x", 34567)
	if err != nil || got != "http://localhost:34567/cb?code=x" {
		t.Errorf("callbackTarget = %q, %v", got, err)
	}
}
