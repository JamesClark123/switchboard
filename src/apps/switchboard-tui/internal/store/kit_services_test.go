package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func kitWithServices(services ...KitService) *Kit {
	return &Kit{Name: "stack", Services: services}
}

func webSvc() KitService {
	return KitService{
		Name:                 "web",
		Command:              "pnpm dev --host 0.0.0.0",
		ListenPort:           3000,
		IsWebsite:            true,
		WorkingDir:           "src/apps/web",
		ReadinessTimeoutSecs: 90,
	}
}

func hostSvc() KitService {
	return KitService{Name: "worker", Command: "pnpm worker --port {{port}}", ListenPort: 7000, OnHost: true}
}

func TestServicesSidecarRoundTripsEveryField(t *testing.T) {
	kits := newKitStore(t)

	if _, err := kits.Save(kitWithServices(webSvc(), hostSvc())); err != nil {
		t.Fatal(err)
	}
	got, err := kits.Get("stack")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(got.Services))
	}
	if got.Services[0] != webSvc() {
		t.Errorf("in-sandbox service round-trip lost a field:\n got %+v\nwant %+v", got.Services[0], webSvc())
	}
	if got.Services[1] != hostSvc() {
		t.Errorf("on-host service round-trip lost a field:\n got %+v\nwant %+v", got.Services[1], hostSvc())
	}
}

// Services are switchboard-owned: the host `sbx` must never see them, so they must
// not leak into the Docker artifact.
func TestServicesAreNotRenderedIntoSpecYAML(t *testing.T) {
	kit := kitWithServices(webSvc())
	y, err := kit.SpecYAML()
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"web", "pnpm dev", "listenPort", "services"} {
		if strings.Contains(y, needle) {
			t.Errorf("spec.yaml must not mention %q — sbx kit validate would reject it:\n%s", needle, y)
		}
	}
}

func TestServicesLiveInTheirOwnSidecarFile(t *testing.T) {
	kits := newKitStore(t)
	if _, err := kits.Save(kitWithServices(webSvc())); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(kits.Dir("stack"), "services.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected kits/stack/services.yaml: %v", err)
	}
}

func TestMissingSidecarYieldsNoServices(t *testing.T) {
	kits := newKitStore(t)
	if _, err := kits.Save(&Kit{Name: "bare"}); err != nil {
		t.Fatal(err)
	}
	got, err := kits.Get("bare")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 0 {
		t.Errorf("a kit with no services must load with none, got %d", len(got.Services))
	}
}

func TestEmptyingServicesRemovesTheSidecar(t *testing.T) {
	kits := newKitStore(t)
	if _, err := kits.Save(kitWithServices(webSvc())); err != nil {
		t.Fatal(err)
	}
	if _, err := kits.Save(&Kit{Name: "stack"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(kits.Dir("stack"), "services.yaml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("removing the last service must delete the sidecar, stat err = %v", err)
	}
}

func TestToSpecProjectsServices(t *testing.T) {
	kit := kitWithServices(webSvc(), hostSvc())
	spec, err := kit.ToSpec()
	if err != nil {
		t.Fatal(err)
	}
	svcs := spec.GetServices()
	if len(svcs) != 2 {
		t.Fatalf("wire services = %d, want 2", len(svcs))
	}
	if svcs[0].GetLocation() != pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX {
		t.Errorf("OnHost=false must map to IN_SANDBOX, got %v", svcs[0].GetLocation())
	}
	if svcs[1].GetLocation() != pb.ServiceLocation_SERVICE_LOCATION_ON_HOST {
		t.Errorf("OnHost=true must map to ON_HOST, got %v", svcs[1].GetLocation())
	}
	if svcs[0].GetListenPort() != 3000 || !svcs[0].GetIsWebsite() ||
		svcs[0].GetWorkingDir() != "src/apps/web" || svcs[0].GetReadinessTimeoutSeconds() != 90 {
		t.Errorf("wire projection lost a field: %+v", svcs[0])
	}
	// The location enum must never be left unspecified — the daemon rejects that.
	for _, s := range svcs {
		if s.GetLocation() == pb.ServiceLocation_SERVICE_LOCATION_UNSPECIFIED {
			t.Errorf("service %q projected an unspecified location", s.GetName())
		}
	}
}

func TestToSpecWithNoServicesProjectsNil(t *testing.T) {
	spec, err := (&Kit{Name: "bare"}).ToSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.GetServices() != nil {
		t.Error("a kit with no services must project a nil list, not an empty one")
	}
}

func TestServiceLocationLabel(t *testing.T) {
	if got := (KitService{}).Location(); got != "in sandbox" {
		t.Errorf("default location label = %q", got)
	}
	if got := (KitService{OnHost: true}).Location(); got != "on host" {
		t.Errorf("on-host location label = %q", got)
	}
}
