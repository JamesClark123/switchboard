package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// navToServices drills the editor into the services section.
func navToServices(t *testing.T, out Model) Model {
	t.Helper()
	for out.kitEditor.section != secServices {
		out, _ = update(out, press("j"))
	}
	out, _ = update(out, press("enter"))
	if !out.kitEditor.inSection {
		t.Fatal("enter should drill into the services section")
	}
	return out
}

// setSvcVals fills the bound form values directly, then applies the form. Driving a
// seven-field huh form (two of them Confirms) keystroke-by-keystroke is brittle; the
// bound-pointer path is exactly what applyKitForm reads, so this exercises the real
// apply logic with the same values huh would have written.
func setSvcVals(out Model, s store.KitService) Model {
	v := out.kitEditor.vals
	v.svcName = s.Name
	v.itemCommand = s.Command
	v.svcPort = strconv.Itoa(int(s.ListenPort))
	v.svcOnHost = s.OnHost
	v.svcIsWebsite = s.IsWebsite
	v.svcWorkingDir = s.WorkingDir
	if s.ReadinessTimeoutSecs > 0 {
		v.svcReadiness = strconv.Itoa(int(s.ReadinessTimeoutSecs))
	}
	out, _ = update(out, ctrlS())
	return out
}

func addService(t *testing.T, out Model, s store.KitService) Model {
	t.Helper()
	out = pressCmd(out, "a")
	if out.kitEditor.form == nil {
		t.Fatal("expected the service item form to open")
	}
	return setSvcVals(out, s)
}

func inSandboxSvc() store.KitService {
	return store.KitService{Name: "web", Command: "pnpm dev --host 0.0.0.0", ListenPort: 3000, IsWebsite: true}
}

func onHostSvc() store.KitService {
	return store.KitService{Name: "worker", Command: "pnpm worker --port {{port}}", ListenPort: 7000, OnHost: true, WorkingDir: "api"}
}

func TestKitEditorServicesSectionListed(t *testing.T) {
	out := editorOn(t, ruffKit())
	v := out.View()
	if !strings.Contains(v, "Services") {
		t.Errorf("the section list must offer Services:\n%s", v)
	}
}

// US1's independent test: one service of each execution location, every field,
// saved and reopened.
func TestKitEditorAddsOneServicePerLocationAndPersists(t *testing.T) {
	out := editorOn(t, ruffKit())
	out = navToServices(t, out)

	out = addService(t, out, inSandboxSvc())
	out = addService(t, out, onHostSvc())

	got := out.kitEditor.kit.Services
	if len(got) != 2 {
		t.Fatalf("services = %d, want 2", len(got))
	}
	if got[0] != inSandboxSvc() {
		t.Errorf("in-sandbox entry = %+v, want %+v", got[0], inSandboxSvc())
	}
	if got[1] != onHostSvc() {
		t.Errorf("on-host entry = %+v, want %+v", got[1], onHostSvc())
	}
	// Each keeps its OWN execution location and listen port (US1 scenario 2).
	if got[0].OnHost || !got[1].OnHost {
		t.Error("each service must keep its own execution location")
	}
	if got[0].ListenPort == got[1].ListenPort {
		t.Error("each service must keep its own listen port")
	}
}

func TestKitEditorServiceEditReplacesInPlace(t *testing.T) {
	out := editorOn(t, ruffKit())
	out = navToServices(t, out)
	out = addService(t, out, inSandboxSvc())

	// Re-open item 0 and change its port.
	out = pressCmd(out, "enter")
	if out.kitEditor.form == nil {
		t.Fatal("expected the item form to reopen for editing")
	}
	edited := inSandboxSvc()
	edited.ListenPort = 5173
	out = setSvcVals(out, edited)

	if n := len(out.kitEditor.kit.Services); n != 1 {
		t.Fatalf("editing must replace in place, got %d services", n)
	}
	if out.kitEditor.kit.Services[0].ListenPort != 5173 {
		t.Errorf("port = %d, want 5173", out.kitEditor.kit.Services[0].ListenPort)
	}
}

func TestKitEditorServiceDelete(t *testing.T) {
	out := editorOn(t, ruffKit())
	out = navToServices(t, out)
	out = addService(t, out, inSandboxSvc())
	out = addService(t, out, onHostSvc())

	out, _ = update(out, press("d"))
	if n := len(out.kitEditor.kit.Services); n != 1 {
		t.Fatalf("services after delete = %d, want 1", n)
	}
	if out.kitEditor.kit.Services[0].Name != "worker" {
		t.Errorf("the wrong service was deleted, left %q", out.kitEditor.kit.Services[0].Name)
	}
}

func TestKitEditorServiceRejectsMissingFields(t *testing.T) {
	tests := []struct {
		name   string
		svc    store.KitService
		wantIn string
	}{
		{"no name", store.KitService{Command: "pnpm dev", ListenPort: 3000}, "name and command are required"},
		{"no command", store.KitService{Name: "web", ListenPort: 3000}, "name and command are required"},
		{"no port", store.KitService{Name: "web", Command: "pnpm dev"}, "listen port"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := editorOn(t, ruffKit())
			out = navToServices(t, out)
			out = pressCmd(out, "a")
			out = setSvcVals(out, tc.svc)

			if len(out.kitEditor.kit.Services) != 0 {
				t.Fatal("an invalid entry must not be added")
			}
			if !strings.Contains(out.kitEditor.status, tc.wantIn) {
				t.Errorf("status = %q, want it to mention %q", out.kitEditor.status, tc.wantIn)
			}
		})
	}
}

func TestKitEditorServiceRejectsNonNumericPort(t *testing.T) {
	out := editorOn(t, ruffKit())
	out = navToServices(t, out)
	out = pressCmd(out, "a")
	v := out.kitEditor.vals
	v.svcName, v.itemCommand, v.svcPort = "web", "pnpm dev", "three thousand"
	out, _ = update(out, ctrlS())

	if len(out.kitEditor.kit.Services) != 0 {
		t.Fatal("a non-numeric port must not be accepted")
	}
	if !strings.Contains(out.kitEditor.status, "number") {
		t.Errorf("status = %q, want it to say the port must be a number", out.kitEditor.status)
	}
}

// US1 scenario 5: abandoning an edit must leave the stored kit untouched.
func TestKitEditorAbandonedServiceEditMutatesNothing(t *testing.T) {
	out := editorOn(t, ruffKit())
	out = navToServices(t, out)
	out = pressCmd(out, "a")

	v := out.kitEditor.vals
	v.svcName, v.itemCommand, v.svcPort = "web", "pnpm dev", "3000"
	// esc, not ctrl+s.
	out, _ = update(out, press("esc"))

	if n := len(out.kitEditor.kit.Services); n != 0 {
		t.Errorf("an abandoned edit must add nothing, got %d services", n)
	}
}

func TestKitEditorServiceItemLabelShowsPortAndLocation(t *testing.T) {
	out := editorOn(t, ruffKit())
	out = navToServices(t, out)
	out = addService(t, out, onHostSvc())

	label := out.kitItemLabel(secServices, 0)
	for _, want := range []string{"worker", ":7000", "on host"} {
		if !strings.Contains(label, want) {
			t.Errorf("item label %q must show %q", label, want)
		}
	}
}

func TestKitEditorServiceSectionCount(t *testing.T) {
	out := editorOn(t, ruffKit())
	if got := out.kitSectionCount(secServices); got != "—" {
		t.Errorf("empty services count = %q, want a dash", got)
	}
	out = navToServices(t, out)
	out = addService(t, out, inSandboxSvc())
	if got := out.kitSectionCount(secServices); got != "1" {
		t.Errorf("services count = %q, want 1", got)
	}
}

// Saving must run the switchboard-owned validation, not just the Docker one.
func TestKitEditorSaveRejectsInvalidService(t *testing.T) {
	out := editorOn(t, ruffKit())
	out = navToServices(t, out)
	// Add a valid one through the form, then corrupt it the way a hand-edit would.
	out = addService(t, out, inSandboxSvc())
	out.kitEditor.kit.Services[0].Name = "Not Kebab"

	out, _ = update(out, ctrlS())
	if !strings.Contains(out.kitEditor.status, "kebab-case") {
		t.Errorf("save status = %q, want the kebab-case rejection", out.kitEditor.status)
	}
}
