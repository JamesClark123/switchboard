package sandbox

import (
	"context"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func webService() *pb.KitService {
	return &pb.KitService{
		Name:       "web",
		Command:    "pnpm dev --host 0.0.0.0",
		ListenPort: 3000,
		Location:   pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX,
		IsWebsite:  true,
	}
}

// The resolved service set is the enforcement set for StartSandboxService, so it
// has to survive the registry round-trip (FR-044).
func TestLaunchPersistsServices(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	sb, err := m.Launch(ctx, LaunchRequest{
		Config:   &pb.ConfigSnapshot{Name: "x"},
		Sources:  []*pb.SourceRef{src},
		Services: []*pb.KitService{webService()},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	stored, err := reg.Get(sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	got := stored.GetServices()
	if len(got) != 1 {
		t.Fatalf("services = %d, want 1", len(got))
	}
	if got[0].GetName() != "web" || got[0].GetListenPort() != 3000 ||
		got[0].GetLocation() != pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX || !got[0].GetIsWebsite() {
		t.Errorf("service round-trip lost a field: %+v", got[0])
	}
}

// A record written before feature 006 has no `services` field. Proto3 decodes the
// missing field as an empty list — this asserts there is no migration to run.
func TestPreFeatureRecordDecodesWithNoServices(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	sb, err := m.Launch(ctx, LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "x"},
		Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reg.Get(sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.GetServices()) != 0 {
		t.Errorf("a sandbox launched with no services must decode to an empty set, got %d", len(stored.GetServices()))
	}
}

func TestAddKitMergesServicesLaterKitWins(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	sb, err := m.Launch(ctx, LaunchRequest{
		Config:   &pb.ConfigSnapshot{Name: "x"},
		Sources:  []*pb.SourceRef{src},
		Services: []*pb.KitService{webService()},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	override := webService()
	override.Command = "pnpm start"
	newer := []*pb.KitService{
		override,
		{Name: "api", Command: "go run .", ListenPort: 8080, Location: pb.ServiceLocation_SERVICE_LOCATION_ON_HOST},
	}

	out, err := m.AddKit(ctx, sb.GetId(), "/kits/extra", nil, newer, nil)
	if err != nil {
		t.Fatal(err)
	}
	svcs := out.GetServices()
	if len(svcs) != 2 {
		t.Fatalf("services = %d, want 2", len(svcs))
	}
	if svcs[0].GetName() != "web" || svcs[0].GetCommand() != "pnpm start" {
		t.Errorf("the later kit must override the same-named service in place, got %+v", svcs[0])
	}
	if svcs[1].GetName() != "api" {
		t.Errorf("the new service must be appended, got %q", svcs[1].GetName())
	}
}

// An invalid declaration must fail the attach BEFORE sbx restarts the sandbox.
func TestAddKitRejectsInvalidServiceBeforeRestart(t *testing.T) {
	ctx := context.Background()
	m, _, runner, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	sb, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{Name: "x"}, Sources: []*pb.SourceRef{src}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	bad := []*pb.KitService{{Name: "web", Command: "pnpm dev", ListenPort: 0, Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX}}
	if _, err := m.AddKit(ctx, sb.GetId(), "/kits/bad", nil, bad, nil); err == nil {
		t.Fatal("an out-of-range listen_port must fail the attach")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.kitAdds) != 0 {
		t.Errorf("sbx kit add must not run when the service set is invalid, got %v", runner.kitAdds)
	}
}

// Services must survive a container recreate, like the escape-hatch allowlist.
func TestServicesSurviveRestartRecreate(t *testing.T) {
	ctx := context.Background()
	m, _, runner, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	sb, err := m.Launch(ctx, LaunchRequest{
		Config:   &pb.ConfigSnapshot{Name: "x"},
		Sources:  []*pb.SourceRef{src},
		Services: []*pb.KitService{webService()},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}
	// Force the relaunch branch: sbx often cannot resume a stopped container.
	runner.mu.Lock()
	runner.failStart = true
	runner.mu.Unlock()

	out, err := m.Restart(ctx, sb.GetId(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.GetServices()) != 1 || out.GetServices()[0].GetName() != "web" {
		t.Errorf("a container recreate must replay the service set, got %+v", out.GetServices())
	}
}
