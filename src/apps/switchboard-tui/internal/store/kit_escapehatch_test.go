package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// escapeHatchKit is a kit whose only populated section is escape-hatch commands
// (newKitStore and sampleKit already exist in kit_test.go).
func escapeHatchKit() *Kit {
	return &Kit{
		Name: "my-kit",
		EscapeHatch: []KitEscapeHatchCommand{
			{Name: "install-deps", Command: "pnpm install", WhenToUse: "when deps change", RequiresApproval: false},
			{Name: "e2e", Command: "pnpm test:e2e", WhenToUse: "for e2e", RequiresApproval: true, WorkingDir: "app", MaxDurationSecs: 600},
		},
	}
}

func TestEscapeHatchSidecarRoundTrip(t *testing.T) {
	ks := newKitStore(t)
	if _, err := ks.Save(escapeHatchKit()); err != nil {
		t.Fatal(err)
	}
	got, err := ks.Get("my-kit")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.EscapeHatch) != 2 {
		t.Fatalf("want 2 escape-hatch commands after reload, got %d", len(got.EscapeHatch))
	}
	if got.EscapeHatch[0].Command != "pnpm install" || !got.EscapeHatch[1].RequiresApproval || got.EscapeHatch[1].WorkingDir != "app" {
		t.Errorf("sidecar did not round-trip fields: %+v", got.EscapeHatch)
	}
}

func TestEscapeHatchAbsentFromSpecYAML(t *testing.T) {
	ks := newKitStore(t)
	if _, err := ks.Save(escapeHatchKit()); err != nil {
		t.Fatal(err)
	}
	// spec.yaml is the Docker artifact and MUST NOT mention escape hatch.
	specBytes, err := os.ReadFile(filepath.Join(ks.Dir("my-kit"), "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(specBytes), "escapeHatch") || strings.Contains(string(specBytes), "install-deps") {
		t.Errorf("spec.yaml must not contain escape-hatch content:\n%s", specBytes)
	}
	// The sidecar exists separately.
	if _, err := os.Stat(filepath.Join(ks.Dir("my-kit"), "escape-hatch.yaml")); err != nil {
		t.Errorf("escape-hatch.yaml sidecar missing: %v", err)
	}
}

func TestToSpecCarriesEscapeHatchAsProto(t *testing.T) {
	spec, err := escapeHatchKit().ToSpec()
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.GetEscapeHatch()) != 2 {
		t.Fatalf("ToSpec should carry 2 commands, got %d", len(spec.GetEscapeHatch()))
	}
	if spec.GetEscapeHatch()[0].GetConsentMode() != pb.ConsentMode_CONSENT_MODE_AUTO_RUN {
		t.Errorf("auto-run command mapped wrong: %v", spec.GetEscapeHatch()[0].GetConsentMode())
	}
	if spec.GetEscapeHatch()[1].GetConsentMode() != pb.ConsentMode_CONSENT_MODE_REQUIRES_APPROVAL {
		t.Errorf("requires-approval command mapped wrong: %v", spec.GetEscapeHatch()[1].GetConsentMode())
	}
	if strings.Contains(spec.GetSpecYaml(), "escapeHatch") {
		t.Error("spec_yaml must not contain escape-hatch content")
	}
}

func TestSaveWithNoEscapeHatchRemovesSidecar(t *testing.T) {
	ks := newKitStore(t)
	if _, err := ks.Save(escapeHatchKit()); err != nil {
		t.Fatal(err)
	}
	// Re-save with the commands cleared -> sidecar removed.
	if _, err := ks.Save(&Kit{Name: "my-kit"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ks.Dir("my-kit"), "escape-hatch.yaml")); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed when no commands remain, err=%v", err)
	}
	got, err := ks.Get("my-kit")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.EscapeHatch) != 0 {
		t.Errorf("want no commands after clear, got %d", len(got.EscapeHatch))
	}
}

func TestValidateEscapeHatch(t *testing.T) {
	cases := []struct {
		name    string
		cmd     KitEscapeHatchCommand
		wantErr bool
	}{
		{"valid", KitEscapeHatchCommand{Name: "install-deps", Command: "pnpm i", WhenToUse: "x"}, false},
		{"blank name", KitEscapeHatchCommand{Name: "", Command: "pnpm i", WhenToUse: "x"}, true},
		{"non-kebab name", KitEscapeHatchCommand{Name: "Install_Deps", Command: "pnpm i", WhenToUse: "x"}, true},
		{"blank command", KitEscapeHatchCommand{Name: "x", Command: "", WhenToUse: "x"}, true},
		{"blank when-to-use", KitEscapeHatchCommand{Name: "x", Command: "pnpm i", WhenToUse: ""}, true},
		{"absolute working dir", KitEscapeHatchCommand{Name: "x", Command: "c", WhenToUse: "w", WorkingDir: "/etc"}, true},
		{"escaping working dir", KitEscapeHatchCommand{Name: "x", Command: "c", WhenToUse: "w", WorkingDir: "../../etc"}, true},
		{"nested working dir ok", KitEscapeHatchCommand{Name: "x", Command: "c", WhenToUse: "w", WorkingDir: "app/sub"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kit := &Kit{Name: "k", EscapeHatch: []KitEscapeHatchCommand{tc.cmd}}
			errs := kit.ValidateEscapeHatch()
			if tc.wantErr && len(errs) == 0 {
				t.Errorf("expected a validation error, got none")
			}
			if !tc.wantErr && len(errs) != 0 {
				t.Errorf("expected no error, got %v", errs)
			}
		})
	}
}

func TestDeleteRemovesSidecar(t *testing.T) {
	ks := newKitStore(t)
	if _, err := ks.Save(escapeHatchKit()); err != nil {
		t.Fatal(err)
	}
	if err := ks.Delete("my-kit"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ks.Dir("my-kit"), "escape-hatch.yaml")); !os.IsNotExist(err) {
		t.Errorf("deleting a kit should remove its sidecar too, err=%v", err)
	}
	if _, err := ks.Get("my-kit"); err != ErrKitNotFound {
		t.Errorf("deleted kit should be gone, err=%v", err)
	}
}

func TestGetMalformedSidecarErrors(t *testing.T) {
	ks := newKitStore(t)
	if _, err := ks.Save(&Kit{Name: "k"}); err != nil {
		t.Fatal(err)
	}
	// Corrupt the sidecar.
	if err := os.WriteFile(filepath.Join(ks.Dir("k"), "escape-hatch.yaml"), []byte("::: not yaml :::"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Get("k"); err == nil {
		t.Error("Get should surface a malformed sidecar as an error")
	}
}

func TestSaveEmptyNameErrors(t *testing.T) {
	ks := newKitStore(t)
	if _, err := ks.Save(&Kit{Name: ""}); err == nil {
		t.Error("saving a kit with no name should error")
	}
}

func TestValidateEscapeHatchDuplicateName(t *testing.T) {
	kit := &Kit{Name: "k", EscapeHatch: []KitEscapeHatchCommand{
		{Name: "dup", Command: "a", WhenToUse: "x"},
		{Name: "dup", Command: "b", WhenToUse: "y"},
	}}
	errs := kit.ValidateEscapeHatch()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "duplicate") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a duplicate-name error, got %v", errs)
	}
}
