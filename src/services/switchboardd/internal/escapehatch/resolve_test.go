package escapehatch

import (
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func cmd(name, command string, mode pb.ConsentMode) *pb.EscapeHatchCommand {
	return &pb.EscapeHatchCommand{Name: name, Command: command, WhenToUse: "when", ConsentMode: mode}
}

func auto(name, command string) *pb.EscapeHatchCommand {
	return cmd(name, command, pb.ConsentMode_CONSENT_MODE_AUTO_RUN)
}

func TestResolveLaterKitWinsOnNameCollision(t *testing.T) {
	kitA := []*pb.EscapeHatchCommand{auto("install-deps", "pnpm install")}
	kitB := []*pb.EscapeHatchCommand{auto("install-deps", "npm ci")}

	got, err := Resolve(kitA, kitB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 command after collision merge, got %d", len(got))
	}
	if got[0].GetCommand() != "npm ci" {
		t.Errorf("later kit should win: got command %q, want %q", got[0].GetCommand(), "npm ci")
	}
}

func TestResolvePreservesFirstAppearanceOrder(t *testing.T) {
	kitA := []*pb.EscapeHatchCommand{auto("a", "cmd-a"), auto("b", "cmd-b")}
	kitB := []*pb.EscapeHatchCommand{auto("b", "cmd-b2"), auto("c", "cmd-c")}

	got, err := Resolve(kitA, kitB)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("want %d commands, got %d", len(want), len(got))
	}
	for i, name := range want {
		if got[i].GetName() != name {
			t.Errorf("position %d: got %q, want %q", i, got[i].GetName(), name)
		}
	}
	// b keeps its earlier position but takes the later value.
	if got[1].GetCommand() != "cmd-b2" {
		t.Errorf("overridden command should take later value: got %q", got[1].GetCommand())
	}
}

func TestResolveRejectsUnspecifiedConsentMode(t *testing.T) {
	bad := []*pb.EscapeHatchCommand{cmd("x", "echo", pb.ConsentMode_CONSENT_MODE_UNSPECIFIED)}
	if _, err := Resolve(bad); err == nil {
		t.Fatal("expected error for unspecified consent mode")
	}
}

func TestResolveRejectsBlankNameOrCommand(t *testing.T) {
	if _, err := Resolve([]*pb.EscapeHatchCommand{auto("", "echo")}); err == nil {
		t.Error("expected error for blank name")
	}
	if _, err := Resolve([]*pb.EscapeHatchCommand{auto("x", "")}); err == nil {
		t.Error("expected error for blank command")
	}
}

func TestResolveEmptyIsEmpty(t *testing.T) {
	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %d", len(got))
	}
}

func TestCommandsFromRefsOnlySpecKits(t *testing.T) {
	refs := []*pb.KitRef{
		{Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "k1", EscapeHatch: []*pb.EscapeHatchCommand{auto("a", "cmd")}}}},
		{Ref: &pb.KitRef_Source{Source: "ghcr.io/org/kit:1.0"}}, // external => no commands
	}
	lists := CommandsFromRefs(refs)
	if len(lists) != 1 {
		t.Fatalf("want 1 command list (spec kit only), got %d", len(lists))
	}
	if len(lists[0]) != 1 || lists[0][0].GetName() != "a" {
		t.Errorf("unexpected extracted commands: %v", lists[0])
	}
}
