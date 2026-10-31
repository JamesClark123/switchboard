package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"google.golang.org/protobuf/encoding/prototext"
)

// Tests for the PR 29 review fixes.

// A switchboard-only save whose store fails keeps a recorded rebind_pending,
// and its host error never carries the entered value.
func TestSaveStoreFailureKeepsRebindPending(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp", "A: ${a}"), false, nil)
	if _, err := e.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		s.Definitions["gh"].RebindPending = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v := e.open("gh")
	e.sec.failSet["mcp:gh:a"] = true
	out, err := e.edit(v, v.GetDefinition(), false, map[string]string{"mcp:gh:a": "VALUE-9921"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.GetRebindPending() || !e.settings().GetDefinitions()["gh"].GetRebindPending() {
		t.Errorf("rebind_pending cleared: %v / %v", out, e.settings().GetDefinitions()["gh"])
	}
	if joined := strings.Join(out.GetHostErrors(), " "); strings.Contains(joined, "VALUE-9921") || !strings.Contains(joined, "••••") {
		t.Errorf("host errors = %q", joined)
	}
}

// `mcp rm` fails and the re-list fails too: no STILL_REGISTERED is asserted;
// the in-progress record stays and recovery resolves it from a fresh list.
func TestSaveRemoveFailureWithFailedRelistRecovers(t *testing.T) {
	b := recoverBackoff
	recoverBackoff = 10 * time.Millisecond
	t.Cleanup(func() { recoverBackoff = b })
	org, team := mcprules.SecretName("gh", "org"), mcprules.SecretName("gh", "team")
	for _, removed := range []bool{true, false} {
		e := newSaveEnv(t)
		e.add("gh", remote("https://x/mcp", "X-Org: ${org}"), true, map[string]string{org: "acme"})
		v := e.open("gh")
		e.touch("rm.fail", "rm.fail.breakls")
		if removed {
			e.touch("rm.fail.removes")
		}
		// One overwritten and one created secret are planned, and the client
		// changes: none of it may be recorded, as neither the stores nor the add ran.
		next := remote("https://y/mcp", "X-Org: ${org}", "X-Team: ${team}")
		next.ClientId = "cid"
		out, err := e.edit(v, next, true, map[string]string{org: "acme2", team: "core"})
		if refusalReason(err) != pb.McpSaveRefusal_UNVERIFIED || out != nil || !strings.Contains(err.Error(), "rm refused by host") {
			t.Fatalf("removed=%v: %v / %v", removed, out, err)
		}
		rec := e.settings().GetSavesInProgress()["gh"]
		if rec == nil || e.settings().GetDefinitions()["gh"] == nil {
			t.Fatalf("removed=%v: record dropped: %v", removed, e.settings())
		}
		if !rec.GetRemoveOnly() || len(rec.GetPlannedCreatedSecrets())+len(rec.GetPlannedOverwrittenSecrets()) > 0 || !strings.Contains(rec.GetRemoveError(), "rm refused by host") {
			t.Errorf("removed=%v: record not remove-only: %v", removed, rec)
		}
		if st := e.m.stateSnapshot("gh"); !st.recovering || st.saving || e.m.ReplaceState("gh") != BeingReplaced {
			t.Errorf("removed=%v: state %+v", removed, st)
		}
		// A new save waits for the recovery.
		if _, err := e.edit(v, remote("https://y/mcp"), true, nil); !errors.Is(err, ErrRecovering) {
			t.Errorf("removed=%v: save during recovery = %v", removed, err)
		}
		e.rm("rm.fail", "rm.fail.breakls", "ls.fail")
		waitFor(t, "the recovery", func() bool { return len(e.settings().GetSavesInProgress()) == 0 && !e.m.stateSnapshot("gh").recovering })
		s := e.settings()
		if e.sec.has(team) || !e.sec.has(org) {
			t.Errorf("removed=%v: secrets changed: %v", removed, e.sec.callLog())
		}
		if removed {
			p := s.GetPendingReplaces()["gh"]
			if p == nil || s.GetDefinitions()["gh"].GetUrl() != "https://x/mcp" || p.GetDefinition().GetUrl() != "https://y/mcp" {
				t.Fatalf("removed: want a pending replace: %v", s)
			}
			if p.GetAddStarted() || len(p.GetRemovalFailedSecrets()) > 0 || len(p.GetRemovedSecrets()) > 0 ||
				!strings.Contains(p.GetError(), "turned out to be removed") || strings.Contains(p.GetError(), "daemon stopped") || strings.Contains(p.GetError(), "cannot be restored") {
				t.Errorf("removed: pending records false facts: %v", p)
			}
		} else {
			d := s.GetDefinitions()["gh"]
			if s.GetPendingReplaces()["gh"] != nil || d.GetUrl() != "https://x/mcp" || d.GetRebindPending() || d.GetClientId() != "" || !s.GetMarks()["gh"] {
				t.Errorf("kept: want the definition and mark unchanged: %v", s)
			}
		}
	}
}

// Time queued behind another holder does not count against the save's budget.
func TestSaveBudgetStartsAfterTheLock(t *testing.T) {
	st := saveTimeout
	saveTimeout = time.Second
	t.Cleanup(func() { saveTimeout = st })
	e := newSaveEnv(t)
	_, release, err := e.m.lockName(context.Background(), "gh", lockOpts{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		release()
	}()
	out, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: remote("https://x/mcp"), IsNew: true})
	if err != nil || out.GetState() != pb.McpSaveOutcome_REGISTERED {
		t.Errorf("save after a long wait = %v, %v", out, err)
	}
}

// The end of a replace is recorded in the same hold that clears the flags.
func TestReplaceEndRecordedWithTheFlags(t *testing.T) {
	e := newSaveEnv(t)
	l, release, err := e.m.lockName(context.Background(), "gh", lockOpts{})
	if err != nil {
		t.Fatal(err)
	}
	e.m.setReplacing("gh")
	e.m.clearRefusing("gh", l, func(s *nameState) { s.end(false) })
	if e.m.ReplaceState("gh") != EndedRemoved {
		t.Errorf("after clearRefusing = %v", e.m.ReplaceState("gh"))
	}
	release()
	e.m.stateMu.Lock()
	e.m.st("gh").pending = true
	e.m.stateMu.Unlock()
	e.m.clearRecovering("gh", false, true)
	if e.m.ReplaceState("gh") != EndedRegistered {
		t.Errorf("after clearRecovering = %v", e.m.ReplaceState("gh"))
	}
	e.m.stateMu.Lock()
	e.m.st("gh").pending = true
	e.m.stateMu.Unlock()
	e.m.endPending("gh")
	if e.m.ReplaceState("gh") != EndedRemoved {
		t.Errorf("after endPending = %v", e.m.ReplaceState("gh"))
	}
}

// Idle state entries are pruned at the next snapshot; a name missing from the
// map is stale for a snapshot that had it or that predates a prune.
func TestStateMapPruned(t *testing.T) {
	e := newSaveEnv(t)
	for _, n := range []string{"a", "b", "c"} {
		_, release, err := e.m.lockName(context.Background(), n, lockOpts{})
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	before := e.m.generations() // prunes a, b, c
	e.m.stateMu.Lock()
	n := len(e.m.state)
	e.m.stateMu.Unlock()
	if n != 0 {
		t.Fatalf("state entries left: %d", n)
	}
	e.m.stateMu.Lock()
	if e.m.staleFor("a", before) {
		t.Error("a pruned name is stale for the snapshot that pruned it")
	}
	e.m.stateMu.Unlock()
	// "d" comes and goes after the snapshot: stale (the epoch moved).
	_, release, _ := e.m.lockName(context.Background(), "d", lockOpts{})
	release()
	_ = e.m.generations()
	e.m.stateMu.Lock()
	if !e.m.staleFor("d", before) {
		t.Error("a name pruned after the snapshot is not stale")
	}
	e.m.stateMu.Unlock()
	// A non-idle entry is kept, and a snapshot that had it sees it missing as stale.
	e.m.endPending("e") // lastEnd = removed
	snap := e.m.generations()
	e.m.stateMu.Lock()
	_, kept := e.m.state["e"]
	delete(e.m.state, "e")
	stale := e.m.staleFor("e", snap)
	e.m.stateMu.Unlock()
	if !kept || !stale {
		t.Errorf("kept %v stale %v", kept, stale)
	}
}

// A Retry refused after its pre-step removed secrets still records and
// announces the removal.
func TestRetryRefusedAfterPreStepAnnouncesRemoval(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp"), false, nil)
	v := e.open("gh")
	e.touch("add.fail")
	e.sec.failRm["mcp:gh:k"] = true
	out, _ := e.edit(v, remote("https://y/mcp", "A: ${k}"), false, map[string]string{"mcp:gh:k": "v-k"})
	rev := out.GetPendingReplace().GetRevision()
	delete(e.sec.failRm, "mcp:gh:k")
	events := e.sec.events
	local := &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{Command: "npx"}}
	if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: local, RetryOfRevision: rev}); !errors.Is(err, ErrLocalNotAcknowledged) {
		t.Fatalf("retry = %v", err)
	}
	p := e.settings().GetPendingReplaces()["gh"]
	if e.sec.has("mcp:gh:k") || len(p.GetRemovalFailedSecrets()) != 0 || !has(p.GetRemovedSecrets(), "mcp:gh:k") || p.GetRevision() != rev {
		t.Errorf("pending = %v", p)
	}
	if e.sec.events != events+1 {
		t.Errorf("secrets_changed events = %d, want %d", e.sec.events, events+1)
	}
}

// add_started is set only when the add process started.
func TestAddStartedOnlyWhenTheProcessStarted(t *testing.T) {
	e := newSaveEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j := &saveJob{m: e.m, name: "gh", def: remote("https://x/mcp"), out: &pb.McpSaveOutcome{}}
	if j.addStep(ctx) || j.out.GetAddStarted() || len(j.out.GetHostErrors()) == 0 {
		t.Errorf("expired = %v", j.out)
	}
	e.touch("add.fail")
	j.out = &pb.McpSaveOutcome{}
	if j.addStep(context.Background()) || !j.out.GetAddStarted() {
		t.Errorf("failed add = %v", j.out)
	}
}

// One secrets_changed for a switchboard-only save whose persist was retried.
func TestPersistRetryEmitsSecretsChangedOnce(t *testing.T) {
	d, b := persistRetryDelay, persistBackoff
	persistRetryDelay, persistBackoff = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { persistRetryDelay, persistBackoff = d, b })
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp", "A: ${a}"), false, nil)
	v := e.open("gh")
	events := e.sec.events
	e.store.setFail(1000)
	if _, err := e.edit(v, v.GetDefinition(), true, map[string]string{"mcp:gh:a": "v"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	e.store.setFail(0)
	waitFor(t, "the lock", func() bool { return e.m.stateSnapshot("gh").lockers == 0 && !e.m.stateSnapshot("gh").recording })
	e.sec.mu.Lock()
	got := e.sec.events
	e.sec.mu.Unlock()
	if got != events+1 {
		t.Errorf("secrets_changed = %d, want %d", got, events+1)
	}
}

// Host-reported header entries that are not "token-name: value" never reach
// the view; the view lists them as an unsupported setting.
func TestMalformedInspectHeaderNeverShown(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp", "X-Org: a"), false, nil)
	e.write("gh.inspect", `{"url":"https://x/mcp","headers":["Authorization=Bearer sk-live-SECRET","Bad Name: sk-live-SECRET2","X-Org: a"]}`)
	v := e.open("gh")
	b := prototext.Format(v)
	if strings.Contains(b, "sk-live") || !has(v.GetUnsupportedSettings(), "malformed header") || !has(v.GetDefinition().GetHeaders(), "X-Org: a") {
		t.Errorf("view = %v", v)
	}
}

// A notice on stderr does not break the --json reads.
func TestJSONReadsIgnoreStderr(t *testing.T) {
	e := newSaveEnv(t)
	d := remote("https://x/mcp")
	d.Scopes = []string{"read"}
	e.add("gh", d, false, nil)
	e.write("gh.inspect", `{"url":"https://moved/mcp"}`)
	e.write("gh.auth", `[{"server_name":"gh","status":"authorized","default_scopes":["read"]}]`)
	e.touch("notice")
	v := e.open("gh")
	if v.GetDefinition().GetUrl() != "https://moved/mcp" || v.GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED {
		t.Errorf("view = %v", v)
	}
	servers, _, _, err := e.m.ListAll(context.Background())
	if err != nil || len(servers) != 1 || servers[0].GetAuthState() != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED {
		t.Errorf("list = %v, %v", servers, err)
	}
	e.touch("inspect.fail")
	if _, err := e.m.cli.runJSON(context.Background(), argvInspect("gh")...); err == nil || !strings.Contains(err.Error(), "inspect broke") {
		t.Errorf("failed read = %v", err)
	}
}

// A server the host re-registered as the other kind shows none of the
// record's fields of the old kind, and the change is flagged.
func TestKindChangedOnHost(t *testing.T) {
	e := newSaveEnv(t)
	d := remote("https://x/mcp", "X-Org: a")
	d.Scopes = []string{"read"}
	e.add("gh", d, false, nil)
	e.write("gh.srv", "stdio")
	v := e.open("gh")
	def := v.GetDefinition()
	if def.GetKind() != pb.McpServerKind_MCP_SERVER_KIND_LOCAL || def.GetUrl() != "" || len(def.GetHeaders()) != 0 || len(def.GetScopes()) != 0 ||
		!hasFlag(v, "kind", pb.McpFieldFlag_CHANGED_ON_HOST) || !hasFlag(v, "command", pb.McpFieldFlag_NOT_RECOVERABLE) {
		t.Errorf("remote → local view = %v", v)
	}
	// And back: a recorded local server listed as remote carries no command.
	local := &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{Command: "npx"}}
	if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "lc", Definition: local, IsNew: true, AcknowledgeLocalExecution: true}); err != nil {
		t.Fatal(err)
	}
	e.write("lc.srv", "remote http")
	e.write("lc.inspect", `{"url":"https://r/mcp"}`)
	v = e.open("lc")
	if v.GetDefinition().GetCommand() != nil || v.GetDefinition().GetUrl() != "https://r/mcp" || !hasFlag(v, "kind", pb.McpFieldFlag_CHANGED_ON_HOST) {
		t.Errorf("local → remote view = %v", v)
	}
	if _, err := e.edit(v, v.GetDefinition(), false, nil); err != nil {
		t.Errorf("save of the reloaded view = %v", err)
	}
}

// With no record, a host-reported offline_access scope is kept.
func TestUnrecordedOfflineAccessKept(t *testing.T) {
	e := newSaveEnv(t)
	e.write("ext.srv", "remote http")
	e.write("ext.auth", `[{"server_name":"ext","status":"unauthorized","default_scopes":["read","offline_access"]}]`)
	if v := e.open("ext"); !has(v.GetDefinition().GetScopes(), "offline_access") {
		t.Errorf("scopes = %v", v.GetDefinition().GetScopes())
	}
}

// The definition read refuses a name no save accepts.
func TestDefinitionChecksTheName(t *testing.T) {
	e := newSaveEnv(t)
	for _, n := range []string{"a:b", "x:client_secret", WorkerServerPrefix + "abc", "has space"} {
		if _, err := e.m.Definition(context.Background(), n, true); !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%q = %v", n, err)
		}
	}
}

// The URL rule and control characters are refused before any host change.
func TestSaveRefusesBadURLAndControlCharacters(t *testing.T) {
	e := newSaveEnv(t)
	for _, def := range []*pb.McpServerDefinition{
		remote("ftp://x/mcp"),
		remote("https://x/a b"),
		remote("https://x/mcp", "X-A: a\x00b"),
		{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{Command: "npx", Args: []string{"a\x00"}}},
	} {
		if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: def, IsNew: true, AcknowledgeLocalExecution: true}); !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%v = %v", def, err)
		}
	}
	e.add("gh", remote("https://x/mcp"), false, nil)
	v := e.open("gh")
	e.clearLog()
	if _, err := e.edit(v, remote("x.example/mcp"), false, nil); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("replace with a bad URL = %v", err)
	}
	if hasPrefix(e.argv(), "mcp rm") {
		t.Error("a bad URL removed the server")
	}
}

// A tab in a host command's argument is allowed, as in a header value.
func TestCheckCommandAllowsTabs(t *testing.T) {
	if err := checkCommand(&pb.AddMcpServerRequest_LocalCommand{Command: "npx", Args: []string{"a\tb"}, Dir: "/tmp"}); err != nil {
		t.Errorf("tab = %v", err)
	}
	for _, bad := range []string{"a\x00", "a\nb", "a\x1bb"} {
		if err := checkCommand(&pb.AddMcpServerRequest_LocalCommand{Command: "npx", Args: []string{bad}}); !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%q = %v", bad, err)
		}
	}
}

// A Retry that restores the previous URL of a server registered by hand is not
// held to the URL rule (the host accepted it once); a different URL still is.
func TestRetryPreviousKeepsAHandRegisteredURL(t *testing.T) {
	e := newSaveEnv(t)
	e.write("ext.srv", "remote http")
	e.write("ext.inspect", `{"url":"https://x/a b"}`)
	v := e.open("ext")
	e.touch("add.fail")
	out, err := e.edit(v, remote("https://y/mcp"), false, nil)
	if err != nil || out.GetState() != pb.McpSaveOutcome_NOT_REGISTERED {
		t.Fatalf("replace = %v, %v", out, err)
	}
	p := e.settings().GetPendingReplaces()["ext"]
	if p.GetPrevious().GetUrl() != "https://x/a b" {
		t.Fatalf("previous = %v", p.GetPrevious())
	}
	e.rm("add.fail")
	if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "ext", Definition: remote("ftp://z/mcp"), RetryOfRevision: p.GetRevision()}); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("retry with a new bad URL = %v", err)
	}
	out, err = e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "ext", Definition: p.GetPrevious(), RetryOfRevision: p.GetRevision()})
	if err != nil || out.GetState() != pb.McpSaveOutcome_REGISTERED || !e.registered("ext") {
		t.Errorf("retry previous = %v, %v", out, err)
	}
}

// A Retry with a bad URL is refused before its pre-step touches the host: the
// carried removal-failed secret is neither removed nor moved off the list.
func TestRetryBadURLRefusedBeforePreStep(t *testing.T) {
	e := newSaveEnv(t)
	e.add("gh", remote("https://x/mcp"), false, nil)
	v := e.open("gh")
	e.touch("add.fail")
	e.sec.failRm["mcp:gh:k"] = true
	out, _ := e.edit(v, remote("https://y/mcp", "A: ${k}"), false, map[string]string{"mcp:gh:k": "v-k"})
	rev := out.GetPendingReplace().GetRevision()
	delete(e.sec.failRm, "mcp:gh:k")
	calls := len(e.sec.callLog())
	if _, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: "gh", Definition: remote("ftp://y/mcp"), RetryOfRevision: rev}); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("retry = %v", err)
	}
	p := e.settings().GetPendingReplaces()["gh"]
	if got := e.sec.callLog()[calls:]; len(got) != 0 || !e.sec.has("mcp:gh:k") || !has(p.GetRemovalFailedSecrets(), "mcp:gh:k") {
		t.Errorf("secret calls %v, pending %v", got, p)
	}
}

// The legacy Add applies the placeholder-name rule (FR-408).
func TestLegacyAddChecksPlaceholders(t *testing.T) {
	e := newSaveEnv(t)
	_, err := e.m.Add(context.Background(), &pb.AddMcpServerRequest{Name: "gh", Definition: &pb.AddMcpServerRequest_Url{Url: "https://x/mcp"},
		Headers: []string{"Authorization: Bearer ${client_secret}"}})
	if !errors.Is(err, ErrInvalidArgs) || e.registered("gh") {
		t.Errorf("err = %v", err)
	}
}
