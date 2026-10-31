package mcprules

import (
	"sort"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// OfflineAccess is the refresh-token scope the runtime may add on its own.
const OfflineAccess = "offline_access"

func sortStrings(s []string) { sort.Strings(s) }

// Normalize returns a comparable copy of def (research R6): URL trimmed,
// headers keyed by lower-cased name with the value trimmed and sorted, scopes
// sorted and de-duplicated without a runtime-added offline_access (one not in
// recorded), command fields trimmed, and the daemon-owned fields
// (rebind_pending, literal_on_host) cleared. nil stays nil.
func Normalize(def, recorded *pb.McpServerDefinition) *pb.McpServerDefinition {
	if def == nil {
		return nil
	}
	out := proto.Clone(def).(*pb.McpServerDefinition)
	out.Url = strings.TrimSpace(out.Url)
	out.ClientId = strings.TrimSpace(out.ClientId)
	out.OauthAuthorizationServer = strings.TrimSpace(out.OauthAuthorizationServer)
	out.RebindPending = false
	out.LiteralOnHost = nil
	var hs []string
	for _, h := range out.Headers {
		if strings.TrimSpace(h) == "" {
			continue
		}
		n, v := SplitHeader(h)
		hs = append(hs, strings.ToLower(n)+": "+v)
	}
	sort.Strings(hs)
	out.Headers = hs
	keepOffline := false
	for _, s := range recorded.GetScopes() {
		if s == OfflineAccess {
			keepOffline = true
		}
	}
	seen := map[string]bool{}
	var sc []string
	for _, s := range out.Scopes {
		if s == "" || seen[s] || s == OfflineAccess && !keepOffline {
			continue
		}
		seen[s] = true
		sc = append(sc, s)
	}
	sort.Strings(sc)
	out.Scopes = sc
	if c := out.Command; c != nil {
		c.Command = strings.TrimSpace(c.Command)
		c.Dir = strings.TrimSpace(c.Dir)
		if len(c.Args) == 0 {
			c.Args = nil
		}
		if c.Command == "" && c.Dir == "" && len(c.Args) == 0 {
			out.Command = nil
		}
	}
	return out
}

// Equal compares two definitions in normalised form. Each side keeps its own
// offline_access, so adding or removing it is a change; a runtime-added one is
// dropped before the comparison by Normalize(hostScopes, recorded).
func Equal(a, b *pb.McpServerDefinition) bool {
	return proto.Equal(Normalize(a, a), Normalize(b, b))
}

// RegistrationChanged reports whether new differs from old in any definition
// field (the mark is not part of a definition). A missing old is a change.
func RegistrationChanged(old, new *pb.McpServerDefinition) bool {
	if old == nil || new == nil {
		return old != new
	}
	return !Equal(old, new)
}

// ClientChanged reports an OAuth client change (FR-415): the client ID or the
// authorization server differs, or the URL differs while either side has a
// client ID. A missing side is no client change.
func ClientChanged(old, new *pb.McpServerDefinition) bool {
	if old == nil || new == nil {
		return false
	}
	o, n := Normalize(old, nil), Normalize(new, nil)
	if o.ClientId != n.ClientId || o.OauthAuthorizationServer != n.OauthAuthorizationServer {
		return true
	}
	return o.Url != n.Url && (o.ClientId != "" || n.ClientId != "")
}
