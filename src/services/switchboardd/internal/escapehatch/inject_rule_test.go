package escapehatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func readClaudeMd(t *testing.T, ws string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ws, claudeMdRelPath))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInjectRuleListsResolvedCommands(t *testing.T) {
	ws := t.TempDir()
	cmds := []*pb.EscapeHatchCommand{
		{Name: "install-deps", Command: "pnpm install", WhenToUse: "when dependencies change", ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN},
		{Name: "e2e", Command: "pnpm test:e2e", WhenToUse: "to run end-to-end tests", ConsentMode: pb.ConsentMode_CONSENT_MODE_REQUIRES_APPROVAL},
	}
	if err := Inject(ws, "sb1", testCallbackURL, cmds); err != nil {
		t.Fatal(err)
	}
	got := readClaudeMd(t, ws)
	for _, want := range []string{
		"install-deps", "when dependencies change", "auto-run",
		"e2e", "to run end-to-end tests", "requires the developer's approval",
		"OUTSIDE this sandbox", wrapperRelPath,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rule block missing %q; got:\n%s", want, got)
		}
	}
}

func TestInjectRuleIsIdempotent(t *testing.T) {
	ws := t.TempDir()
	cmds := []*pb.EscapeHatchCommand{{Name: "a", Command: "true", WhenToUse: "x", ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN}}
	for i := 0; i < 3; i++ {
		if err := Inject(ws, "sb1", testCallbackURL, cmds); err != nil {
			t.Fatal(err)
		}
	}
	got := readClaudeMd(t, ws)
	if n := strings.Count(got, ruleBeginMarker); n != 1 {
		t.Errorf("re-injection should keep exactly one block, found %d", n)
	}
}

func TestInjectRulePreservesUserContent(t *testing.T) {
	ws := t.TempDir()
	userContent := "# My project\n\nSome notes the developer wrote.\n"
	if err := os.WriteFile(filepath.Join(ws, claudeMdRelPath), []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds := []*pb.EscapeHatchCommand{{Name: "a", Command: "true", WhenToUse: "x", ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN}}
	if err := Inject(ws, "sb1", testCallbackURL, cmds); err != nil {
		t.Fatal(err)
	}
	got := readClaudeMd(t, ws)
	if !strings.Contains(got, "Some notes the developer wrote.") {
		t.Errorf("user content must be preserved; got:\n%s", got)
	}
	if !strings.Contains(got, ruleBeginMarker) {
		t.Error("rule block should be appended")
	}

	// Removing the commands strips the block but keeps user content.
	if err := Inject(ws, "sb1", testCallbackURL, nil); err != nil {
		t.Fatal(err)
	}
	got = readClaudeMd(t, ws)
	if strings.Contains(got, ruleBeginMarker) {
		t.Error("empty set should remove the rule block")
	}
	if !strings.Contains(got, "Some notes the developer wrote.") {
		t.Errorf("user content must survive block removal; got:\n%s", got)
	}
}

func TestInjectEmptySetRemovesRuleFile(t *testing.T) {
	ws := t.TempDir()
	cmds := []*pb.EscapeHatchCommand{{Name: "a", Command: "true", WhenToUse: "x", ConsentMode: pb.ConsentMode_CONSENT_MODE_AUTO_RUN}}
	if err := Inject(ws, "sb1", testCallbackURL, cmds); err != nil {
		t.Fatal(err)
	}
	// CLAUDE.md was only our block -> removed entirely when the set empties.
	if err := Inject(ws, "sb1", testCallbackURL, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, claudeMdRelPath)); !os.IsNotExist(err) {
		t.Errorf("a CLAUDE.md that held only our block should be removed, err=%v", err)
	}
}
