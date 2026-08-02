package portforward

import (
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func svc(name, cmd string, port uint32, loc pb.ServiceLocation) *pb.KitService {
	return &pb.KitService{Name: name, Command: cmd, ListenPort: port, Location: loc}
}

const (
	inSandbox = pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX
	onHost    = pb.ServiceLocation_SERVICE_LOCATION_ON_HOST
)

func TestResolveLaterKitWins(t *testing.T) {
	first := []*pb.KitService{svc("web", "pnpm dev", 3000, inSandbox)}
	second := []*pb.KitService{svc("web", "pnpm start", 4000, onHost)}

	out, err := Resolve(first, second)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 service, got %d", len(out))
	}
	if out[0].GetCommand() != "pnpm start" || out[0].GetListenPort() != 4000 {
		t.Errorf("later kit must win, got %q/%d", out[0].GetCommand(), out[0].GetListenPort())
	}
	if out[0].GetLocation() != onHost {
		t.Errorf("override must carry the later definition's location, got %v", out[0].GetLocation())
	}
}

func TestResolvePreservesFirstAppearanceOrder(t *testing.T) {
	first := []*pb.KitService{
		svc("api", "go run .", 8080, inSandbox),
		svc("web", "pnpm dev", 3000, inSandbox),
	}
	// "web" is overridden by a later kit; it must keep its original position so the
	// client's list does not reshuffle on re-attach.
	second := []*pb.KitService{
		svc("web", "pnpm dev --host", 3000, inSandbox),
		svc("worker", "pnpm worker", 7000, onHost),
	}

	out, err := Resolve(first, second)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := []string{out[0].GetName(), out[1].GetName(), out[2].GetName()}
	want := []string{"api", "web", "worker"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if out[1].GetCommand() != "pnpm dev --host" {
		t.Errorf("overridden service kept position but must take the later value, got %q", out[1].GetCommand())
	}
}

func TestResolveRejectsInvalidDeclarations(t *testing.T) {
	tests := []struct {
		name    string
		service *pb.KitService
		wantIn  string
	}{
		{"empty name", svc("", "pnpm dev", 3000, inSandbox), "empty name"},
		{"empty command", svc("web", "   ", 3000, inSandbox), "empty command"},
		{"zero port", svc("web", "pnpm dev", 0, inSandbox), "outside 1-65535"},
		{"port too high", &pb.KitService{Name: "web", Command: "x", ListenPort: 70000, Location: inSandbox}, "outside 1-65535"},
		{"unspecified location", svc("web", "pnpm dev", 3000, pb.ServiceLocation_SERVICE_LOCATION_UNSPECIFIED), "must set a location"},
		{"absolute working dir", &pb.KitService{Name: "web", Command: "x", ListenPort: 3000, Location: inSandbox, WorkingDir: "/etc"}, "escapes the workspace"},
		{"escaping working dir", &pb.KitService{Name: "web", Command: "x", ListenPort: 3000, Location: inSandbox, WorkingDir: "../secrets"}, "escapes the workspace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve([]*pb.KitService{tc.service})
			if err == nil {
				t.Fatalf("want rejection, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q must mention %q", err, tc.wantIn)
			}
		})
	}
}

func TestResolveAcceptsNestedWorkingDir(t *testing.T) {
	s := &pb.KitService{Name: "web", Command: "pnpm dev", ListenPort: 3000, Location: inSandbox, WorkingDir: "src/apps/web"}
	if _, err := Resolve([]*pb.KitService{s}); err != nil {
		t.Fatalf("nested working_dir must be accepted: %v", err)
	}
}

func TestResolveSkipsNilAndEmptyLists(t *testing.T) {
	out, err := Resolve(nil, []*pb.KitService{nil}, []*pb.KitService{svc("web", "x", 1, inSandbox)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 service, got %d", len(out))
	}
}

func TestServicesFromRefsOnlyAuthoredKits(t *testing.T) {
	refs := []*pb.KitRef{
		{Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "a", Services: []*pb.KitService{svc("web", "x", 3000, inSandbox)}}}},
		{Ref: &pb.KitRef_Source{Source: "ghcr.io/org/kit:1.0"}}, // opaque: contributes nothing
	}
	lists := ServicesFromRefs(refs)
	if len(lists) != 1 {
		t.Fatalf("external source kits must contribute no service list, got %d lists", len(lists))
	}
}

func TestLookupIsTheAllowlistCheck(t *testing.T) {
	sb := &pb.Sandbox{Services: []*pb.KitService{svc("web", "pnpm dev", 3000, inSandbox)}}
	if _, ok := Lookup(sb, "web"); !ok {
		t.Error("declared service must resolve")
	}
	if _, ok := Lookup(sb, "not-declared"); ok {
		t.Error("an undeclared name must NOT resolve — this is the allowlist boundary")
	}
}
