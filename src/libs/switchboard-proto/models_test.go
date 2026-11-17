package switchboardproto

import (
	"slices"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// TestModelCatalogConsistent keeps every catalog entry coherent (SWI-34):
// unique names and IDs, each ID the one its display name derives, each in
// the family its ID starts with, found by both lookups.
func TestModelCatalogConsistent(t *testing.T) {
	if len(Models) == 0 {
		t.Fatal("empty catalog")
	}
	seenID, seenDisplay := map[string]bool{}, map[string]bool{}
	for _, m := range Models {
		if seenID[m.ID] || seenDisplay[m.Display] {
			t.Errorf("%+v: duplicate", m)
		}
		seenID[m.ID], seenDisplay[m.Display] = true, true
		if got := ModelIDFromDisplay(m.Display); got != m.ID {
			t.Errorf("%q: ID %q, want %q from the display name", m.Display, m.ID, got)
		}
		family := ModelChoiceLabel(m.Family)
		if m.Family == pb.ModelChoice_MODEL_CHOICE_UNSPECIFIED || family == "unknown" {
			t.Errorf("%q: family %v", m.Display, m.Family)
		}
		if !strings.HasPrefix(m.ID, "claude-"+strings.ToLower(family)+"-") || !strings.HasPrefix(m.Display, family+" ") {
			t.Errorf("%q (%s): not a %s model", m.Display, m.ID, family)
		}
		if got, ok := ModelByID(m.ID); !ok || got != m {
			t.Errorf("ModelByID(%q) = %+v, %v", m.ID, got, ok)
		}
		if got, ok := ModelByDisplay(m.Display); !ok || got != m {
			t.Errorf("ModelByDisplay(%q) = %+v, %v", m.Display, got, ok)
		}
	}
	if got := ModelDisplays(); len(got) != len(Models) || got[0] != Models[0].Display || !slices.Contains(got, "Fable 5") {
		t.Errorf("ModelDisplays = %q", got)
	}
}

func TestModelLookups(t *testing.T) {
	for _, id := range []string{"", "claude-bogus-1", "fable", "Fable 5.1", "claude-fable-5-1 "} {
		if m, ok := ModelByID(id); ok {
			t.Errorf("ModelByID(%q) = %+v", id, m)
		}
	}
	if m, ok := ModelByDisplay("fable 5.1"); !ok || m.ID != "claude-fable-5-1" || m.Family != pb.ModelChoice_MODEL_CHOICE_FABLE {
		t.Errorf("ModelByDisplay(fable 5.1) = %+v, %v", m, ok)
	}
	for _, name := range []string{"", "Fable", "fable", "claude-fable-5-1", "Fable 5.1 ", "Fable 5.10"} {
		if m, ok := ModelByDisplay(name); ok {
			t.Errorf("ModelByDisplay(%q) = %+v", name, m)
		}
	}
}

func TestModelIDFromDisplay(t *testing.T) {
	cases := map[string]string{
		"Fable 5.1":  "claude-fable-5-1",
		"Fable 5":    "claude-fable-5",
		"Fable 5.0":  "claude-fable-5",
		"Haiku 4.5":  "claude-haiku-4-5",
		"Opus 4.10":  "claude-opus-4-10",
		"Sonnet 10":  "claude-sonnet-10",
		"":           "",
		"Opus":       "",
		"Opus five":  "",
		"Opus 5.1.2": "",
		"Opus 5.":    "",
		"Opus .5":    "",
		" Opus 5":    "",
		"Opus 5 ":    "",
		"Opus  5":    "",
		"Op us 5":    "",
	}
	for name, want := range cases {
		if got := ModelIDFromDisplay(name); got != want {
			t.Errorf("ModelIDFromDisplay(%q) = %q, want %q", name, got, want)
		}
	}
}
