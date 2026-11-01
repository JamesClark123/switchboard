package mcprules

import (
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func def(url string, headers []string, scopes []string, client string) *pb.McpServerDefinition {
	return &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: url, Headers: headers, Scopes: scopes, ClientId: client}
}

func TestNormalizeEqual(t *testing.T) {
	a := def(" https://x ", []string{"X-B: 2", "x-a :1"}, []string{"write", "read", "read"}, "")
	b := def("https://x", []string{"X-A: 1", "X-B: 2"}, []string{"read", "write"}, "")
	if !Equal(a, b) {
		t.Fatalf("not equal: %v vs %v", Normalize(a, b), Normalize(b, a))
	}
	rec := def("https://x", nil, []string{OfflineAccess}, "")
	if n := Normalize(def("", nil, []string{"read", OfflineAccess}, ""), rec); len(n.Scopes) != 2 {
		t.Errorf("recorded offline_access dropped: %v", n.Scopes)
	}
	if Equal(a, def("https://x", []string{"X-A: 1", "X-B: 3"}, nil, "")) {
		t.Error("header value change missed")
	}
	if Normalize(nil, nil) != nil {
		t.Error("nil")
	}
	local := &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{Command: " npx "}}
	if !Equal(local, &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{Command: "npx"}}) {
		t.Error("command trim")
	}
	if Normalize(&pb.McpServerDefinition{Command: &pb.AddMcpServerRequest_LocalCommand{}}, nil).Command != nil {
		t.Error("empty command kept")
	}
}

func TestRegistrationAndClientChanged(t *testing.T) {
	old := def("https://x", nil, nil, "cid")
	marked := def("https://x", nil, nil, "cid")
	marked.RebindPending = true
	marked.LiteralOnHost = []string{"Authorization"}
	if RegistrationChanged(old, marked) {
		t.Error("daemon-owned fields count as a change")
	}
	if !RegistrationChanged(nil, old) || RegistrationChanged(nil, nil) {
		t.Error("nil handling")
	}
	for _, tc := range []struct {
		new  *pb.McpServerDefinition
		want bool
	}{
		{def("https://x", []string{"A: 1"}, nil, "cid"), false},
		{def("https://y", nil, nil, "cid"), true},
		{def("https://x", nil, nil, "other"), true},
		{def("https://y", nil, nil, ""), true},
	} {
		if got := ClientChanged(old, tc.new); got != tc.want {
			t.Errorf("%v: %v", tc.new, got)
		}
	}
	if ClientChanged(def("https://x", nil, nil, ""), def("https://y", nil, nil, "")) {
		t.Error("URL change without a client is a client change")
	}
	as := def("https://x", nil, nil, "cid")
	as.OauthAuthorizationServer = "https://idp"
	if !ClientChanged(old, as) || ClientChanged(nil, old) {
		t.Error("auth server")
	}
}

func TestEqualCountsAnEditedOfflineAccess(t *testing.T) {
	rec := &pb.McpServerDefinition{Scopes: []string{"read"}}
	edited := &pb.McpServerDefinition{Scopes: []string{"read", OfflineAccess}}
	if Equal(rec, edited) || !RegistrationChanged(rec, edited) || !RegistrationChanged(edited, rec) {
		t.Error("adding or removing offline_access is a registration change")
	}
	if !Equal(edited, &pb.McpServerDefinition{Scopes: []string{OfflineAccess, "read", "read"}}) {
		t.Error("order and duplicates are not a change")
	}
}
