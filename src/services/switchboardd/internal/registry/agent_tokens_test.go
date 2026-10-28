package registry

import (
	"errors"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestAgentTokensPersistPerGeneration(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []AgentToken{{Gen: 0, Token: "x"}, {Gen: 1}} {
		if err := r.PutAgentToken("sb", bad); err == nil {
			t.Errorf("PutAgentToken(%+v) must fail", bad)
		}
	}
	if err := r.PutAgentToken("a/b", AgentToken{Gen: 1, Token: "x"}); err == nil {
		t.Error("a sandbox id with a slash must be refused")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(r.PutAgentToken("sb", AgentToken{Gen: 2, Token: "t2"}))
	must(r.PutAgentToken("sb", AgentToken{Gen: 1, Token: "t1"})) // a lower one never lowers the counter
	must(r.PutAgentToken("other", AgentToken{Gen: 7, Token: "o7"}))
	must(r.SetAgentMain("sb", 2, 4242, "991"))
	if err := r.SetAgentMain("sb", 9, 1, ""); !errors.Is(err, ErrAgentTokenNotFound) {
		t.Errorf("SetAgentMain on a missing generation = %v", err)
	}
	must(r.Close())

	// Reopened: everything survives a daemon restart.
	r, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	all, err := r.ListAgentTokens()
	must(err)
	sb := all["sb"]
	if sb == nil || sb.Counter != 2 || len(sb.Tokens) != 2 {
		t.Fatalf("sb = %+v", sb)
	}
	if got := sb.Tokens[1]; got.Gen != 2 || got.Token != "t2" || got.MainPID != 4242 || got.MainStart != "991" {
		t.Errorf("gen 2 = %+v", got)
	}
	if all["other"].Counter != 7 {
		t.Errorf("other = %+v", all["other"])
	}

	must(r.DeleteAgentToken("sb", 1))
	must(r.DeleteAgentTokens("other", false))
	all, _ = r.ListAgentTokens()
	if len(all["sb"].Tokens) != 1 || all["other"].Counter != 7 || len(all["other"].Tokens) != 0 {
		t.Errorf("after retiring: sb %+v, other %+v", all["sb"], all["other"])
	}
	must(r.DeleteAgentTokens("other", true))
	all, _ = r.ListAgentTokens()
	if _, ok := all["other"]; ok {
		t.Error("a purge removes the counter too")
	}
}

// A token never rides on the sandbox record (ListSandboxes).
func TestAgentTokenNeverOnTheSandboxRecord(t *testing.T) {
	r := openTemp(t)
	if err := r.Put(&pb.Sandbox{Id: "sb"}); err != nil {
		t.Fatal(err)
	}
	if err := r.PutAgentToken("sb", AgentToken{Gen: 1, Token: "secret-token-value"}); err != nil {
		t.Fatal(err)
	}
	list, _ := r.List()
	b, _ := proto.Marshal(list[0])
	if len(list) != 1 || containsBytes(b, "secret-token-value") {
		t.Error("the token must not be on the sandbox record")
	}
}

func containsBytes(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
