package store

import (
	"strings"
	"testing"
)

func validService() KitService {
	return KitService{Name: "web", Command: "pnpm dev --host 0.0.0.0", ListenPort: 3000}
}

func TestValidateServicesAcceptsAValidDeclaration(t *testing.T) {
	kit := &Kit{Name: "stack", Services: []KitService{validService(), hostSvc()}}
	if errs := kit.ValidateServices(); len(errs) != 0 {
		t.Errorf("valid services rejected: %v", errs)
	}
}

func TestValidateServicesNamesTheOffendingField(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*KitService)
		wantIn  string
		wantSvc string // the label must identify WHICH service
	}{
		{"missing name", func(s *KitService) { s.Name = "" }, "name is required", "#1"},
		{"non-kebab name", func(s *KitService) { s.Name = "Web Server" }, "kebab-case", "Web Server"},
		{"missing command", func(s *KitService) { s.Command = "  " }, "command is required", "web"},
		{"zero port", func(s *KitService) { s.ListenPort = 0 }, "between 1 and 65535", "web"},
		{"port too high", func(s *KitService) { s.ListenPort = 70000 }, "between 1 and 65535", "web"},
		{"absolute working dir", func(s *KitService) { s.WorkingDir = "/etc" }, "working dir", "web"},
		{"escaping working dir", func(s *KitService) { s.WorkingDir = "../../etc" }, "working dir", "web"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := validService()
			tc.mutate(&svc)
			errs := (&Kit{Name: "stack", Services: []KitService{svc}}).ValidateServices()
			if len(errs) == 0 {
				t.Fatal("want a rejection, got none")
			}
			joined := strings.Join(errs, "; ")
			if !strings.Contains(joined, tc.wantIn) {
				t.Errorf("errors %q must mention %q", joined, tc.wantIn)
			}
			if !strings.Contains(joined, tc.wantSvc) {
				t.Errorf("errors %q must identify the service (%q)", joined, tc.wantSvc)
			}
		})
	}
}

func TestValidateServicesRejectsDuplicateNameWithinOneKit(t *testing.T) {
	a, b := validService(), validService()
	b.ListenPort = 4000
	errs := (&Kit{Name: "stack", Services: []KitService{a, b}}).ValidateServices()
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, "; "), "duplicate name") {
		t.Errorf("two services named %q in one kit must be rejected, got %v", a.Name, errs)
	}
}

// {{port}} exists to dodge host-port collisions. An in-sandbox service has its own
// network namespace, so the token there is a mistake, not a no-op — the daemon
// publishes the DECLARED port, so substituting a different one would break the map.
func TestValidateServicesRejectsPortTokenOnInSandboxService(t *testing.T) {
	svc := validService()
	svc.Command = "pnpm dev --port {{port}}"
	errs := (&Kit{Name: "stack", Services: []KitService{svc}}).ValidateServices()
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, "; "), "{{port}}") {
		t.Errorf("{{port}} on an in-sandbox service must be rejected, got %v", errs)
	}

	svc.OnHost = true
	if errs := (&Kit{Name: "stack", Services: []KitService{svc}}).ValidateServices(); len(errs) != 0 {
		t.Errorf("{{port}} on an on-host service is the supported form: %v", errs)
	}
}

func TestValidateServicesAllowsNestedWorkingDir(t *testing.T) {
	svc := validService()
	svc.WorkingDir = "src/apps/web"
	if errs := (&Kit{Name: "stack", Services: []KitService{svc}}).ValidateServices(); len(errs) != 0 {
		t.Errorf("a nested working dir must be accepted: %v", errs)
	}
}

func TestValidateServicesReportsEveryViolation(t *testing.T) {
	// A developer with several broken entries should see them all, not just the first.
	bad := KitService{Name: "", Command: "", ListenPort: 0}
	errs := (&Kit{Name: "stack", Services: []KitService{bad}}).ValidateServices()
	if len(errs) < 3 {
		t.Errorf("want one error per violated field, got %v", errs)
	}
}

func TestValidateServicesOnEmptyKit(t *testing.T) {
	if errs := (&Kit{Name: "stack"}).ValidateServices(); len(errs) != 0 {
		t.Errorf("a kit with no services is valid, got %v", errs)
	}
}
