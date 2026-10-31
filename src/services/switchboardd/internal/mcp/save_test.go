package mcp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"google.golang.org/protobuf/proto"
)

func TestSaveNewServerStoresSecretsBeforeTheAddAndRecords(t *testing.T) {
	e := newSaveEnv(t)
	def := remote("https://x/mcp", "Authorization: Bearer ${api-key}", "X-Org: acme")
	def.Scopes = []string{"read", "write"}
	out := e.add("gh", def, true, map[string]string{"mcp:gh:api-key": "v-api-1"})
	if out.GetState() != pb.McpSaveOutcome_REGISTERED || out.GetReplaced() || !has(out.GetCreated(), "mcp:gh:api-key") {
		t.Fatalf("outcome = %v", out)
	}
	argv := e.argv()
	if !has(argv, "mcp add gh --url https://x/mcp --header Authorization: Bearer ${api-key} --header X-Org: acme --scope read --scope write --skip-auth") {
		t.Errorf("argv = %v", argv)
	}
	if hasPrefix(argv, "mcp rm") {
		t.Error("a new server is never removed first")
	}
	if !e.sec.has("mcp:gh:api-key") || e.sec.callLog()[1] != "set mcp:gh:api-key" {
		t.Errorf("secret calls = %v", e.sec.callLog())
	}
	s := e.settings()
	if !s.GetMarks()["gh"] || s.GetDefinitions()["gh"].GetUrl() != "https://x/mcp" || len(s.GetSavesInProgress()) != 0 {
		t.Errorf("settings = %v", s)
	}
	if got := e.outcomes(); len(got) != 1 || got[0].GetName() != "gh" {
		t.Errorf("outcome events = %v", got)
	}
	if e.sec.events != 1 {
		t.Errorf("secrets_changed = %d, want 1", e.sec.events)
	}
	// A second add of a listed name is refused before anything runs.
	if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: def, IsNew: true}); !errors.Is(err, ErrAlreadyRegistered) {
		t.Errorf("re-add = %v", err)
	}
}

func TestSaveRequestValidation(t *testing.T) {
	e := newSaveEnv(t)
	ctx := context.Background()
	def := remote("https://x/mcp", "Authorization: Bearer ${k}")
	for name, req := range map[string]*pb.SaveMcpServerRequest{
		"empty secret":    {Name: "s", IsNew: true, Definition: def, SecretValues: map[string]string{"mcp:s:k": ""}},
		"marker":          {Name: "s", IsNew: true, Definition: def, SecretValues: map[string]string{"mcp:s:k": "Bearer ••••"}},
		"multi-line":      {Name: "s", IsNew: true, Definition: def, SecretValues: map[string]string{"mcp:s:k": "a\nb"}},
		"foreign secret":  {Name: "s", IsNew: true, Definition: def, SecretValues: map[string]string{"mcp:other:k": "v"}},
		"client w/o id":   {Name: "s", IsNew: true, Definition: def, SecretValues: map[string]string{"mcp:s:client_secret": "v"}},
		"literal":         {Name: "s", IsNew: true, Definition: remote("https://x", "X-Api-Key: abc")},
		"reserved":        {Name: "s", IsNew: true, Definition: remote("https://x", "A: ${client_secret}")},
		"scope comma":     {Name: "s", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://x", Scopes: []string{"a,b"}}},
		"local scopes":    {Name: "s", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{Command: "npx"}, Scopes: []string{"a"}}},
		"no kind":         {Name: "s", IsNew: true, Definition: &pb.McpServerDefinition{Url: "https://x"}},
		"worker prefix":   {Name: WorkerServerPrefix + "x", IsNew: true, Definition: def},
		"edit without fp": {Name: "s", Definition: def},
		"no definition":   {Name: "s", IsNew: true},
		"remote w/o url":  {Name: "s", IsNew: true, Definition: remote("")},
		"local w/o cmd":   {Name: "s", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL}},
		"remote w/ cmd":   {Name: "s", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://x", Command: &pb.AddMcpServerRequest_LocalCommand{Command: "a"}}},
		"local comma arg": {Name: "s", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{Command: "a", Args: []string{"x,y"}}}},
		"bad client id":   {Name: "s", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://x", ClientId: "a b"}},
		"bad auth server": {Name: "s", IsNew: true, Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://x", OauthAuthorizationServer: "http://x"}},
		"bad header":      {Name: "s", IsNew: true, Definition: remote("https://x", "bad")},
	} {
		_, err := e.m.Save(ctx, req)
		if !errors.Is(err, ErrInvalidArgs) && !errors.Is(err, ErrLocalNotAcknowledged) {
			t.Errorf("%s: err = %v, want INVALID_ARGUMENT", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "abc") {
			t.Errorf("%s: the literal leaked: %v", name, err)
		}
	}
	if len(e.argv()) != 0 || len(e.sec.callLog()) != 0 {
		t.Errorf("something ran: %v %v", e.argv(), e.sec.callLog())
	}
	local := &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{Command: "npx"}}
	if _, err := e.m.Save(ctx, &pb.SaveMcpServerRequest{Name: "pw", IsNew: true, Definition: local}); !errors.Is(err, ErrLocalNotAcknowledged) {
		t.Errorf("unacknowledged local = %v", err)
	}
	if _, err := e.m.Save(ctx, &pb.SaveMcpServerRequest{Name: "pw", IsNew: true, Definition: local, AcknowledgeLocalExecution: true}); err != nil {
		t.Errorf("local add = %v", err)
	}
}

func TestSaveMarkOnlyNeverReregisters(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp", "X-Org: acme"), false, nil)
	v := e.open("gh")
	e.clearLog()
	out, err := e.edit(v, v.GetDefinition(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.GetState() != pb.McpSaveOutcome_UNCHANGED_REGISTRATION {
		t.Errorf("state = %v", out.GetState())
	}
	if hasPrefix(e.argv(), "mcp rm") || hasPrefix(e.argv(), "mcp add") {
		t.Errorf("FR-403: argv = %v", e.argv())
	}
	if !e.settings().GetMarks()["gh"] {
		t.Error("mark not saved")
	}
	// A presentation-only difference (header case, spacing) is not a change either.
	v = e.open("gh")
	if out, err := e.edit(v, remote(" https://x/mcp ", "x-org :  acme"), true, nil); err != nil || out.GetState() != pb.McpSaveOutcome_UNCHANGED_REGISTRATION {
		t.Errorf("normalised save = %v, %v", out, err)
	}
}

func TestSaveSwitchboardOnlyOfUnrecordedServerCreatesNoDefinition(t *testing.T) {
	e := newSaveEnv(t)
	e.write("ext.srv", "remote http")
	v := e.open("ext")
	if v.GetHasRecord() || !hasFlag(v, "url", pb.McpFieldFlag_NOT_RECOVERABLE) {
		t.Fatalf("view = %v", v)
	}
	if _, err := e.edit(v, v.GetDefinition(), true, nil); err != nil {
		t.Fatal(err)
	}
	if e.settings().GetDefinitions()["ext"] != nil || !e.settings().GetMarks()["ext"] {
		t.Errorf("D5: settings = %v", e.settings())
	}
}

func TestSaveReplaceRemovesThenStoresThenAdds(t *testing.T) {
	e := newSaveEnv(t, WithSandboxes(fakeSandboxes{
		{Id: "sb1", DisplayName: "one", McpServers: []string{"gh"}, McpAttachMode: pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE},
		{Id: "sb2", DisplayName: "two", McpSkipped: []*pb.McpSkippedServer{{Name: "gh"}}},
		{Id: "sb3", DisplayName: "three"},
	}))
	e.add("gh", remote("https://x/mcp", "X-Org: acme"), true, nil)
	e.write("gh.auth", `[{"server_name":"gh","status":"authorized"}]`)
	v := e.open("gh")
	if v.GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED {
		t.Fatalf("auth = %v", v.GetAuthState())
	}
	e.clearLog()
	newDef := remote("https://y/mcp", "X-Org: acme", "Authorization: Bearer ${tok}")
	// An authorized server needs the discard confirmation.
	if _, err := e.edit(v, newDef, true, nil, func(r *pb.SaveMcpServerRequest) { r.ConfirmDiscardAuthorization = false }); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("unconfirmed = %v", err)
	}
	out, err := e.edit(v, newDef, true, map[string]string{"mcp:gh:tok": "v-tok"})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetState() != pb.McpSaveOutcome_REGISTERED || !out.GetReplaced() {
		t.Fatalf("outcome = %v", out)
	}
	argv := e.argv()
	rmAt, addAt := -1, -1
	for i, a := range argv {
		if a == "mcp rm gh --force" {
			rmAt = i
		}
		if strings.HasPrefix(a, "mcp add gh --url https://y/mcp") {
			addAt = i
		}
	}
	if rmAt < 0 || addAt < rmAt {
		t.Errorf("argv order = %v", argv)
	}
	if !e.sec.has("mcp:gh:tok") || e.settings().GetDefinitions()["gh"].GetUrl() != "https://y/mcp" || !e.settings().GetMarks()["gh"] {
		t.Errorf("record = %v", e.settings())
	}
	if len(out.GetAttachedSandboxes()) != 2 || !out.GetAttachedSandboxes()[0].GetExclusive() || !out.GetAttachedSandboxes()[1].GetSkippedAtLaunch() {
		t.Errorf("FR-419 = %v", out.GetAttachedSandboxes())
	}
	if !has(e.endedHooks(), "gh:registered") {
		t.Errorf("onReplaceEnded = %v", e.endedHooks())
	}
	if out.GetServer().GetName() != "gh" {
		t.Errorf("server row = %v", out.GetServer())
	}
}

type fakeSandboxes []*pb.Sandbox

func (f fakeSandboxes) List() ([]*pb.Sandbox, error) { return f, nil }

func TestSaveFingerprintRefusals(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp"), false, nil)
	v := e.open("gh")
	// The host changed the URL: STALE_CHANGED, nothing removed.
	e.write("gh.inspect", `{"url":"https://other/mcp"}`)
	e.clearLog()
	if _, err := e.edit(v, remote("https://z/mcp"), false, nil); refusalReason(err) != pb.McpSaveRefusal_STALE_CHANGED {
		t.Errorf("changed = %v", err)
	}
	if hasPrefix(e.argv(), "mcp rm") {
		t.Error("a stale save removed the server")
	}
	// A restart (new key) is STALE_RESTARTED.
	v = e.open("gh")
	e.m = e.newManager()
	if _, err := e.edit(v, remote("https://z/mcp"), false, nil); refusalReason(err) != pb.McpSaveRefusal_STALE_RESTARTED {
		t.Errorf("restarted = %v", err)
	}
	// Open succeeds, the save-time inspect fails: UNVERIFIED with nothing changed.
	v = e.open("gh")
	e.touch("inspect.fail")
	e.clearLog()
	if _, err := e.edit(v, remote("https://z/mcp"), false, nil); refusalReason(err) != pb.McpSaveRefusal_UNVERIFIED {
		t.Errorf("unverified = %v", err)
	}
	if hasPrefix(e.argv(), "mcp rm") {
		t.Error("an unverified save removed the server")
	}
	// A read that fails the same way at open and save still matches.
	v = e.open("gh")
	if _, err := e.edit(v, v.GetDefinition(), true, nil); err != nil {
		t.Errorf("consistent failure = %v", err)
	}
	e.rm("inspect.fail")
	// Edit of a vanished name: GONE, nothing removed.
	v = e.open("gh")
	e.rm("gh.srv")
	e.clearLog()
	if _, err := e.edit(v, remote("https://z/mcp"), false, nil); refusalReason(err) != pb.McpSaveRefusal_GONE {
		t.Errorf("gone = %v", err)
	}
	if hasPrefix(e.argv(), "mcp rm") {
		t.Error("removed a vanished server")
	}
}

func TestSaveRemoveFailure(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp"), true, nil)
	// rm fails and the name is still listed: STILL_REGISTERED, nothing stored.
	v := e.open("gh")
	e.touch("rm.fail")
	out, err := e.edit(v, remote("https://y/mcp", "A: ${a}"), true, map[string]string{"mcp:gh:a": "v-a"})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetState() != pb.McpSaveOutcome_STILL_REGISTERED || len(out.GetHostErrors()) == 0 || e.sec.has("mcp:gh:a") {
		t.Errorf("still registered = %v", out)
	}
	if e.settings().GetDefinitions()["gh"].GetUrl() != "https://x/mcp" || len(e.settings().GetSavesInProgress()) != 0 {
		t.Errorf("record = %v", e.settings())
	}
	if !has(e.endedHooks(), "gh:registered") {
		t.Errorf("STILL_REGISTERED ends the replace as registered: %v", e.endedHooks())
	}
	// rm fails but the name is gone after all: the save continues.
	v = e.open("gh")
	e.touch("rm.fail.removes")
	out, err = e.edit(v, remote("https://y/mcp"), true, nil)
	if err != nil || out.GetState() != pb.McpSaveOutcome_REGISTERED {
		t.Errorf("rm failed but absent = %v, %v", out, err)
	}
}

func TestSaveFailedAddHoldsPendingReplaceThenRetryAndCancel(t *testing.T) {
	e := newSaveEnv(t)
	e.sec.vals["mcp:gh:old"] = "v-old"
	e.add("gh", remote("https://x/mcp", "A: ${old}"), true, nil)
	v := e.open("gh")
	e.touch("add.fail")
	out, err := e.edit(v, remote("https://y/mcp", "A: ${old}", "B: ${new}"), true, map[string]string{"mcp:gh:new": "v-new", "mcp:gh:old": "v-old2"})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetState() != pb.McpSaveOutcome_NOT_REGISTERED || !out.GetAddStarted() || out.GetPendingReplace() == nil {
		t.Fatalf("outcome = %v", out)
	}
	if e.registered("gh") {
		t.Error("the failed add left a registration")
	}
	// Created and overwritten secrets are both rolled back for a replace.
	if !has(out.GetRolledBack(), "mcp:gh:new") || !has(out.GetRolledBack(), "mcp:gh:old") || e.sec.has("mcp:gh:new") {
		t.Errorf("rollback = %v", out)
	}
	s := e.settings()
	p := s.GetPendingReplaces()["gh"]
	if p == nil || !p.GetAttachByDefault() || s.GetMarks()["gh"] || p.GetPrevious().GetUrl() != "https://x/mcp" || p.GetRevision() != 1 || !strings.Contains(p.GetError(), "add refused") {
		t.Fatalf("pending = %v / marks %v", p, s.GetMarks())
	}
	if len(e.endedHooks()) != 0 {
		t.Errorf("a held pending replace calls no hook: %v", e.endedHooks())
	}
	// The list carries it; the state map says pending.
	_, settings, pending, err := e.m.ListAll(context.Background())
	if err != nil || len(pending) != 1 || settings.GetPendingReplaces() != nil || settings.GetDefinitions() != nil {
		t.Errorf("list = %v %v %v", settings, pending, err)
	}
	// Authorize and a new add are refused while it exists.
	if err := e.m.Authorize(context.Background(), "gh", nil, false, nil); refusalReason(err) != pb.McpSaveRefusal_BEING_REPLACED {
		t.Errorf("authorize = %v", err)
	}
	if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: remote("https://z"), IsNew: true}); refusalReason(err) != pb.McpSaveRefusal_PENDING_EXISTS {
		t.Errorf("add = %v", err)
	}
	if _, err := e.m.Add(context.Background(), &pb.AddMcpServerRequest{Name: "gh", Definition: &pb.AddMcpServerRequest_Url{Url: "https://z"}}); refusalReason(err) != pb.McpSaveRefusal_PENDING_EXISTS {
		t.Errorf("old add = %v", err)
	}
	// The definition read for a pending name returns the pending replace.
	pv := e.open("gh")
	if pv.GetPendingReplace().GetRevision() != 1 {
		t.Errorf("pending view = %v", pv)
	}
	// Retry of a stale revision: PENDING_ENDED.
	if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: p.GetDefinition(), RetryOfRevision: 9}); refusalReason(err) != pb.McpSaveRefusal_PENDING_ENDED {
		t.Errorf("stale retry = %v", err)
	}
	// Retry fails again: the pending replace is updated, previous kept.
	out, err = e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: p.GetDefinition(), RetryOfRevision: 1, AttachByDefault: true})
	if err != nil || out.GetState() != pb.McpSaveOutcome_NOT_REGISTERED {
		t.Fatalf("retry 1 = %v, %v", out, err)
	}
	p = e.settings().GetPendingReplaces()["gh"]
	if p.GetRevision() != 2 || p.GetPrevious().GetUrl() != "https://x/mcp" {
		t.Errorf("updated pending = %v", p)
	}
	// Retry with the previous definition succeeds: pending gone, mark live.
	e.rm("add.fail")
	out, err = e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: p.GetPrevious(), RetryOfRevision: 2, AttachByDefault: true})
	if err != nil || out.GetState() != pb.McpSaveOutcome_REGISTERED {
		t.Fatalf("retry 2 = %v, %v", out, err)
	}
	s = e.settings()
	if s.GetPendingReplaces()["gh"] != nil || !s.GetMarks()["gh"] || s.GetDefinitions()["gh"].GetUrl() != "https://x/mcp" {
		t.Errorf("after retry = %v", s)
	}
	if !has(e.endedHooks(), "gh:registered") {
		t.Errorf("hooks = %v", e.endedHooks())
	}

	// A second failed replace, then Cancel: pending, mark and definition dropped,
	// removal-failed secrets removed best effort.
	v = e.open("gh")
	e.touch("add.fail")
	e.sec.failRm["mcp:gh:k2"] = true
	out, err = e.edit(v, remote("https://w/mcp", "A: ${old}", "C: ${k2}"), true, map[string]string{"mcp:gh:k2": "v-k2"})
	if err != nil || !has(out.GetRollbackFailed(), "mcp:gh:k2") {
		t.Fatalf("second failure = %v, %v", out, err)
	}
	pv = e.open("gh")
	if !has(pv.GetPendingRemovalFailedListed(), "mcp:gh:k2") {
		t.Errorf("Cancel confirmation listing = %v", pv)
	}
	if _, err := e.m.CancelPending(context.Background(), "gh", 99); refusalReason(err) != pb.McpSaveRefusal_PENDING_ENDED {
		t.Errorf("stale cancel = %v", err)
	}
	resp, err := e.m.CancelPending(context.Background(), "gh", 0)
	if err != nil || !has(resp.GetLeftOnHost(), "mcp:gh:k2") {
		t.Fatalf("cancel = %v, %v", resp, err)
	}
	s = e.settings()
	if s.GetPendingReplaces()["gh"] != nil || s.GetDefinitions()["gh"] != nil || s.GetMarks()["gh"] {
		t.Errorf("after cancel = %v", s)
	}
	if !has(e.endedHooks(), "gh:removed") {
		t.Errorf("hooks = %v", e.endedHooks())
	}
}

func TestRetryPreStepRemovesCarriedRemovalFailed(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp"), false, nil)
	v := e.open("gh")
	e.touch("add.fail")
	e.sec.failRm["mcp:gh:k"] = true
	out, _ := e.edit(v, remote("https://y/mcp", "A: ${k}"), false, map[string]string{"mcp:gh:k": "v-k"})
	rev := out.GetPendingReplace().GetRevision()
	// Still failing to remove and left blank: PENDING_REMOVAL_FAILED, no change.
	e.clearLog()
	if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: remote("https://y/mcp"), RetryOfRevision: rev}); refusalReason(err) != pb.McpSaveRefusal_PENDING_REMOVAL_FAILED {
		t.Errorf("pre-step = %v", err)
	}
	if hasPrefix(e.argv(), "mcp add") {
		t.Error("the retry ran after a failed pre-step")
	}
	delete(e.sec.failRm, "mcp:gh:k")
	e.rm("add.fail")
	if out, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: remote("https://y/mcp"), RetryOfRevision: rev}); err != nil || out.GetState() != pb.McpSaveOutcome_REGISTERED {
		t.Errorf("retry = %v, %v", out, err)
	}
	if e.sec.has("mcp:gh:k") {
		t.Error("the carried secret was not removed")
	}
}

func TestSaveStoreFailureRollsBack(t *testing.T) {
	e := newSaveEnv(t)
	e.sec.vals["mcp:gh:b"] = "v-b-old"
	e.add("gh", remote("https://x/mcp", "A: ${a}", "B: ${b}", "C: ${c}"), false, nil)
	// Switchboard-only save: created removed, overwritten named as changed.
	v := e.open("gh")
	e.sec.failSet["mcp:gh:c"] = true
	out, err := e.edit(v, v.GetDefinition(), false, map[string]string{"mcp:gh:a": "v-a", "mcp:gh:b": "v-b", "mcp:gh:c": "v-c"})
	if err != nil {
		t.Fatal(err)
	}
	if !has(out.GetRolledBack(), "mcp:gh:a") || has(out.GetRolledBack(), "mcp:gh:b") || !has(out.GetOverwritten(), "mcp:gh:b") || e.sec.has("mcp:gh:a") || !e.sec.has("mcp:gh:b") {
		t.Errorf("switchboard-only rollback = %v", out)
	}
	if out.GetState() != pb.McpSaveOutcome_UNCHANGED_REGISTRATION || len(out.GetHostErrors()) == 0 {
		t.Errorf("state = %v", out)
	}
	// New server: a failed store stops before the add.
	e.clearLog()
	e.sec.failSet["mcp:nw:k"] = true
	out = e.add("nw", remote("https://n/mcp", "A: ${k}"), false, map[string]string{"mcp:nw:k": "v"})
	if out.GetState() != pb.McpSaveOutcome_NOT_REGISTERED || out.GetPendingReplace() != nil || hasPrefix(e.argv(), "mcp add") {
		t.Errorf("new server store failure = %v / %v", out, e.argv())
	}
}

func TestSaveIdentityBinding(t *testing.T) {
	e := newSaveEnv(t)
	cdef := func(url, id string) *pb.McpServerDefinition {
		d := remote(url)
		d.ClientId = id
		return d
	}
	// A brand-new add with a client secret: the clear's expected failure (no
	// :identity yet) is CLEARED and left out of host_errors.
	out := e.add("sl", cdef("https://x/mcp", "c1"), false, map[string]string{"mcp:sl:client_secret": "v-cs"})
	if out.GetIdentity() != pb.McpSaveOutcome_CLEARED || len(out.GetHostErrors()) != 0 {
		t.Errorf("first add = %v", out)
	}
	// Client change without a secret: left in place, marked, public-client offer.
	v := e.open("sl")
	out, err := e.edit(v, cdef("https://x/mcp", "c2"), false, nil)
	if err != nil || out.GetIdentity() != pb.McpSaveOutcome_LEFT_IN_PLACE || !out.GetRebindPending() {
		t.Fatalf("left in place = %v, %v", out, err)
	}
	if !has(out.GetUnusedSecrets(), "mcp:sl:client_secret") || !has(out.GetUnusedSecrets(), mcprules.IdentityName("sl")) {
		t.Errorf("unused = %v", out.GetUnusedSecrets())
	}
	if !e.settings().GetDefinitions()["sl"].GetRebindPending() {
		t.Error("rebind_pending not recorded")
	}
	rows, _, _ := e.m.List(context.Background())
	if !findServer(rows, "sl").GetRebindPending() {
		t.Error("the row does not show rebind pending")
	}
	// A secrets-only save storing the client secret on a marked server clears it.
	e.sec.vals[mcprules.IdentityName("sl")] = "bound"
	v = e.open("sl")
	out, err = e.edit(v, v.GetDefinition(), false, map[string]string{"mcp:sl:client_secret": "v-cs2"})
	if err != nil || out.GetIdentity() != pb.McpSaveOutcome_CLEARED || out.GetRebindPending() || e.sec.has(mcprules.IdentityName("sl")) {
		t.Errorf("secrets-only clear = %v, %v", out, err)
	}
	// A failed clear that is still listed: NOT_CLEARED, mark set.
	e.sec.vals[mcprules.IdentityName("sl")] = "bound"
	e.sec.failRm[mcprules.IdentityName("sl")] = true
	v = e.open("sl")
	out, _ = e.edit(v, cdef("https://x/mcp", "c3"), false, map[string]string{"mcp:sl:client_secret": "v-cs3"})
	if out.GetIdentity() != pb.McpSaveOutcome_NOT_CLEARED || !out.GetRebindPending() || len(out.GetHostErrors()) != 1 {
		t.Errorf("not cleared = %v", out)
	}
	// Unreadable list on the re-check: UNCONFIRMED with both errors.
	v = e.open("sl")
	e.sec.listErr = nil
	out, _ = func() (*pb.McpSaveOutcome, error) {
		// The snapshot needs the list; fail it only for the re-check.
		testHookStep = func(step string) {
			if step == "stored" {
				e.sec.mu.Lock()
				e.sec.listErr = errors.New("list broke")
				e.sec.mu.Unlock()
			}
		}
		defer func() { testHookStep = nil; e.sec.listErr = nil }()
		return e.edit(v, cdef("https://x/mcp", "c4"), false, map[string]string{"mcp:sl:client_secret": "v-cs4"})
	}()
	if out.GetIdentity() != pb.McpSaveOutcome_UNCONFIRMED || len(out.GetHostErrors()) != 2 {
		t.Errorf("unconfirmed = %v", out)
	}
	// FR-413 / R9: removing the client secret and its binding clears the mark.
	delete(e.sec.failRm, mcprules.IdentityName("sl"))
	resp, err := e.m.RemoveSecrets(context.Background(), "sl", []string{"mcp:sl:client_secret", mcprules.IdentityName("sl")})
	if err != nil || resp.GetRebindPending() || len(resp.GetRemoved()) != 2 {
		t.Errorf("remove secrets = %v, %v", resp, err)
	}
	if e.settings().GetDefinitions()["sl"].GetRebindPending() {
		t.Error("rebind_pending kept after the removal")
	}
	// Clearing the client ID offers the client secret and binding too.
	e.sec.vals["mcp:sl:client_secret"] = "x"
	v = e.open("sl")
	out, _ = e.edit(v, remote("https://x/mcp"), false, nil)
	if !has(out.GetUnusedSecrets(), "mcp:sl:client_secret") {
		t.Errorf("client cleared unused = %v", out.GetUnusedSecrets())
	}
}

func TestRemoveSecretsRules(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp"), false, nil)
	if _, err := e.m.RemoveSecrets(context.Background(), "gh", []string{"github"}); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("foreign = %v", err)
	}
	if _, err := e.m.RemoveSecrets(context.Background(), "gh", []string{"mcp:gh:a:endpoint"}); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("endpoint = %v", err)
	}
	e.m.mu.Lock()
	e.m.inflight["gh"] = 0
	e.m.mu.Unlock()
	if _, err := e.m.RemoveSecrets(context.Background(), "gh", []string{"mcp:gh:client_secret"}); refusalReason(err) != pb.McpSaveRefusal_AUTH_IN_PROGRESS {
		t.Errorf("during auth = %v", err)
	}
	// Header secrets are allowed during an authorization (FR-424); a failed
	// removal still listed is reported.
	e.sec.vals["mcp:gh:a"] = "v"
	e.sec.failRm["mcp:gh:a"] = true
	resp, err := e.m.RemoveSecrets(context.Background(), "gh", []string{"mcp:gh:a"})
	if err != nil || !has(resp.GetFailed(), "mcp:gh:a") || len(resp.GetHostErrors()) != 1 {
		t.Errorf("header removal = %v, %v", resp, err)
	}
}

func TestSaveDuringAuthorization(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp", "A: ${a}"), false, nil)
	v := e.open("gh")
	e.m.mu.Lock()
	e.m.inflight["gh"] = 0
	e.m.mu.Unlock()
	e.clearLog()
	// A URL-only edit: AUTH_IN_PROGRESS, no rm, nothing stored.
	if _, err := e.edit(v, remote("https://y/mcp", "A: ${a}"), false, map[string]string{"mcp:gh:a": "v"}); refusalReason(err) != pb.McpSaveRefusal_AUTH_IN_PROGRESS {
		t.Errorf("replace = %v", err)
	}
	if hasPrefix(e.argv(), "mcp rm") || e.sec.has("mcp:gh:a") {
		t.Error("something ran")
	}
	// A header-secret-only save is allowed.
	if out, err := e.edit(v, v.GetDefinition(), false, map[string]string{"mcp:gh:a": "v"}); err != nil || out.GetState() != pb.McpSaveOutcome_UNCHANGED_REGISTRATION {
		t.Errorf("secrets-only = %v, %v", out, err)
	}
	// A request carrying the client secret is refused at take time.
	d := proto.Clone(v.GetDefinition()).(*pb.McpServerDefinition)
	d.ClientId = "c"
	if _, err := e.edit(v, d, false, map[string]string{"mcp:gh:client_secret": "v"}); refusalReason(err) != pb.McpSaveRefusal_AUTH_IN_PROGRESS {
		t.Errorf("client secret = %v", err)
	}
	if _, err := e.m.Remove(context.Background(), "gh"); refusalReason(err) != pb.McpSaveRefusal_AUTH_IN_PROGRESS {
		t.Errorf("remove = %v", err)
	}
}

func TestSaveLiteralOnHost(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp", "Authorization: Bearer ${k}"), false, nil)
	e.write("gh.inspect", `{"url":"https://x/mcp","headers":["Authorization: Bearer sk-live-SECRET99"]}`)
	v := e.open("gh")
	b, _ := proto.Marshal(v)
	if bytes.Contains(b, []byte("sk-live-SECRET99")) {
		t.Fatal("the literal reached the view")
	}
	if !hasFlag(v, "headers/Authorization", pb.McpFieldFlag_LITERAL_ON_HOST) || v.GetDefinition().GetHeaders()[0] != "Authorization: Bearer ••••" {
		t.Fatalf("view = %v", v)
	}
	e.clearLog()
	// A mark-only save keeps the redacted header, recorded redacted.
	out, err := e.edit(v, v.GetDefinition(), true, nil)
	if err != nil || out.GetState() != pb.McpSaveOutcome_UNCHANGED_REGISTRATION || hasPrefix(e.argv(), "mcp add") {
		t.Fatalf("mark-only = %v, %v", out, err)
	}
	if rec := e.settings().GetDefinitions()["gh"]; !has(rec.GetLiteralOnHost(), "Authorization") || rec.GetHeaders()[0] != "Authorization: Bearer ••••" {
		t.Errorf("record = %v", rec)
	}
	// A URL change with it unconverted is refused; the argv never holds ••••.
	v = e.open("gh")
	d := proto.Clone(v.GetDefinition()).(*pb.McpServerDefinition)
	d.Url = "https://y/mcp"
	if _, err := e.edit(v, d, false, nil); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("re-register = %v", err)
	}
	for _, a := range e.argv() {
		if strings.Contains(a, mcprules.RedactMarker) {
			t.Errorf("argv holds the marker: %s", a)
		}
	}
	// A typed literal in a re-registering save is refused like any other.
	if _, err := e.edit(v, remote("https://y/mcp", "Authorization: Bearer typed-literal"), false, nil); !errors.Is(err, ErrInvalidArgs) || strings.Contains(err.Error(), "typed-literal") {
		t.Errorf("typed literal = %v", err)
	}
}

func TestResolvedUncheckableHeaders(t *testing.T) {
	inspectReturnsTemplates = false
	t.Cleanup(func() { inspectReturnsTemplates = true })
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp", "Authorization: Bearer ${k}", "X-Org: ${org}"), false, nil)
	e.write("gh.inspect", `{"headers":[{"name":"Authorization","value":"Bearer resolved-1"},{"name":"X-Org","value":"acme"}]}`)
	v := e.open("gh")
	if !hasFlag(v, "headers/Authorization", pb.McpFieldFlag_RESOLVED_UNCHECKABLE) || v.GetDefinition().GetHeaders()[0] != "Authorization: Bearer ${k}" {
		t.Fatalf("view = %v", v)
	}
	e.clearLog()
	if out, err := e.edit(v, v.GetDefinition(), true, nil); err != nil || out.GetState() != pb.McpSaveOutcome_UNCHANGED_REGISTRATION || hasPrefix(e.argv(), "mcp rm") {
		t.Errorf("mark-only = %v, %v (%v)", out, err, e.argv())
	}
	// A re-registering save registers the template.
	v = e.open("gh")
	d := proto.Clone(v.GetDefinition()).(*pb.McpServerDefinition)
	d.Url = "https://y/mcp"
	if out, err := e.edit(v, d, false, nil); err != nil || out.GetState() != pb.McpSaveOutcome_REGISTERED {
		t.Errorf("re-register = %v, %v", out, err)
	}
	if !hasPrefix(e.argv(), "mcp add gh --url https://y/mcp --header Authorization: Bearer ${k}") {
		t.Errorf("argv = %v", e.argv())
	}
}

// AddMcpServer (old clients) keeps today's validation; a literal it accepts is
// recorded redacted (FR-422, D1).
func TestOldClientAddRecordsLiteralsRedacted(t *testing.T) {
	e := newSaveEnv(t)
	if _, err := e.m.Add(context.Background(), &pb.AddMcpServerRequest{Name: "old", Definition: &pb.AddMcpServerRequest_Url{Url: "https://x"}, Headers: []string{"X-Api-Key: abc"}}); err != nil {
		t.Fatal(err)
	}
	rec := e.settings().GetDefinitions()["old"]
	if rec.GetHeaders()[0] != "X-Api-Key: ••••" || !has(rec.GetLiteralOnHost(), "X-Api-Key") {
		t.Errorf("record = %v", rec)
	}
	v := e.open("old")
	if !hasFlag(v, "headers/X-Api-Key", pb.McpFieldFlag_LITERAL_ON_HOST) {
		t.Errorf("view = %v", v)
	}
	// Remove drops the definition with the mark.
	if _, err := e.m.Remove(context.Background(), "old"); err != nil || e.settings().GetDefinitions()["old"] != nil {
		t.Errorf("remove = %v / %v", err, e.settings())
	}
}

func TestSaveCallerCancelStillPersists(t *testing.T) {
	e := newSaveEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	testHookStep = func(step string) {
		if step == "in-progress" {
			cancel()
			<-release
		}
	}
	defer func() { testHookStep = nil }()
	done := make(chan error, 1)
	go func() {
		_, err := e.m.Save(ctx, &pb.SaveMcpServerRequest{Name: "gh", Definition: remote("https://x/mcp"), IsNew: true})
		done <- err
	}()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller err = %v", err)
	}
	close(release)
	waitFor(t, "the detached save to persist", func() bool { return e.settings().GetDefinitions()["gh"] != nil })
	waitFor(t, "the outcome event", func() bool { return len(e.outcomes()) == 1 })
}

func TestSaveForwardBudgetExpiredStillRollsBack(t *testing.T) {
	e := newSaveEnv(t)
	e.touch("add.hang")
	// The add times out on the mutate bound; the rollback runs on its own context.
	mutateWas := mutateTimeout
	_ = mutateWas
	ctx := context.Background()
	start := time.Now()
	out, err := saveWithShortBudget(e, ctx, &pb.SaveMcpServerRequest{Name: "gh", Definition: remote("https://x/mcp", "A: ${a}"), IsNew: true, SecretValues: map[string]string{"mcp:gh:a": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetState() != pb.McpSaveOutcome_NOT_REGISTERED || !has(out.GetRolledBack(), "mcp:gh:a") || e.sec.has("mcp:gh:a") {
		t.Errorf("outcome = %v", out)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

// saveWithShortBudget runs a save whose forward budget is 1 s.
func saveWithShortBudget(e *saveEnv, ctx context.Context, req *pb.SaveMcpServerRequest) (*pb.McpSaveOutcome, error) {
	def, err := checkSaveRequest(req.GetName(), req)
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	l, release, err := e.m.lockName(sctx, req.GetName(), lockOpts{})
	if err != nil {
		return nil, err
	}
	defer release()
	var out *pb.McpSaveOutcome
	var rerr error
	j := &saveJob{m: e.m, l: l, name: req.GetName(), req: req, def: def}
	j.run(sctx, func(o *pb.McpSaveOutcome, err error) { out, rerr = o, err })
	return out, rerr
}

func TestSaveLeakTest(t *testing.T) {
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	values := map[string]string{"mcp:gh:a": "LEAK-A-7731", "mcp:gh:b": "LEAK-B-7732", "mcp:gh:client_secret": "LEAK-CS-7733"}
	e := newSaveEnv(t)
	var errs []string
	note := func(out *pb.McpSaveOutcome, err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
		b, _ := proto.Marshal(out)
		errs = append(errs, string(b))
	}
	def := remote("https://x/mcp", "A: ${a}", "B: ${b}")
	def.ClientId = "c"
	note(e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: def, IsNew: true, SecretValues: values}))
	// Failure + rollback + pending, retry, cancel.
	v := e.open("gh")
	e.touch("add.fail")
	e.sec.failRm["mcp:gh:b"] = true
	d2 := proto.Clone(def).(*pb.McpServerDefinition)
	d2.Url = "https://y/mcp"
	note(e.edit(v, d2, false, values))
	note(e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: d2, RetryOfRevision: 1, SecretValues: values}))
	_, err := e.m.CancelPending(context.Background(), "gh", 0)
	note(nil, err)
	// A refused typed literal.
	note(e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "lit", Definition: remote("https://x", "Authorization: Bearer LEAK-LIT-7734"), IsNew: true}))
	// A failed store.
	e.rm("add.fail")
	e.sec.failSet["mcp:gh:a"] = true
	note(e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: def, IsNew: true, SecretValues: values}))

	raw, _ := proto.Marshal(e.settings())
	var events []string
	e.mu.Lock()
	for _, ev := range e.events {
		b, _ := proto.Marshal(ev)
		events = append(events, string(b))
	}
	e.mu.Unlock()
	haystack := strings.Join(append(append(append(errs, events...), e.argv()...), string(raw), logBuf.String(), strings.Join(e.sec.callLog(), " ")), "\n")
	for _, v := range append([]string{"LEAK-LIT-7734"}, "LEAK-A-7731", "LEAK-B-7732", "LEAK-CS-7733") {
		if strings.Contains(haystack, v) {
			t.Errorf("SC-007: %s leaked", v)
		}
	}
}

func hasFlag(v *pb.McpDefinitionView, field string, kind pb.McpFieldFlag_Kind) bool {
	for _, f := range v.GetFlags() {
		if f.GetField() == field && f.GetKind() == kind {
			return true
		}
	}
	return false
}
