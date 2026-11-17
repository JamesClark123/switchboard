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
		if got := ModelDisplayFromID(m.ID); got != m.Display {
			t.Errorf("ModelDisplayFromID(%q) = %q, want %q", m.ID, got, m.Display)
		}
	}
	if got := ModelNames(); len(got) != len(Models) || got[0] != Models[0].Display+" ("+Models[0].ID+")" || !slices.Contains(got, "Fable 5 (claude-fable-5)") {
		t.Errorf("ModelNames = %q", got)
	}
}

func TestModelDisplayFromID(t *testing.T) {
	cases := map[string]string{
		"claude-fable-5-1":   "Fable 5.1",
		"claude-fable-5":     "Fable 5",
		"claude-opus-4-10":   "Opus 4.10",
		"claude-zed-12":      "Zed 12",
		"":                   "",
		"fable-5":            "",
		"claude-fable":       "",
		"claude-fable-":      "",
		"claude-fable-5-":    "",
		"claude-fable-5-1-2": "",
		"claude-Fable-5":     "",
		"claude-fable5":      "",
		"claude-fab le-5":    "",
		"claude--5":          "",
		"claude-fable-5.1":   "",
		"claude-fable-5-0":   "",
		"claude-fable-5-00":  "",
		"claude-fable-5-05":  "Fable 5.05",
	}
	for id, want := range cases {
		if got := ModelDisplayFromID(id); got != want {
			t.Errorf("ModelDisplayFromID(%q) = %q, want %q", id, got, want)
		}
		if want != "" && ModelIDFromDisplay(want) != id {
			t.Errorf("ModelIDFromDisplay(%q) = %q, want %q back", want, ModelIDFromDisplay(want), id)
		}
	}
}

func TestModelStep(t *testing.T) {
	cases := []struct {
		name          string
		seq           *pb.ActionSequence
		typed, expect string
	}{
		{"alias", &pb.ActionSequence{Model: pb.ModelChoice_MODEL_CHOICE_HAIKU}, "haiku", "Haiku"},
		{"catalog pin", &pb.ActionSequence{Model: pb.ModelChoice_MODEL_CHOICE_FABLE, ModelId: "claude-fable-5"}, "claude-fable-5", "Fable 5"},
		{"pin outside the catalog", &pb.ActionSequence{Model: pb.ModelChoice_MODEL_CHOICE_FABLE, ModelId: "claude-fable-9-2"}, "claude-fable-9-2", "Fable 9.2"},
		{"malformed pin", &pb.ActionSequence{Model: pb.ModelChoice_MODEL_CHOICE_FABLE, ModelId: "claude-fable"}, "fable", "Fable"},
		{"no model", &pb.ActionSequence{}, "unknown", "unknown"},
	}
	for _, c := range cases {
		if typed, expect := ModelStep(c.seq); typed != c.typed || expect != c.expect {
			t.Errorf("%s: ModelStep = %q, %q, want %q, %q", c.name, typed, expect, c.typed, c.expect)
		}
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
		"Opus2 5":    "",
		"Op.us 5":    "",
		"Ünz 5":      "",
	}
	for name, want := range cases {
		if got := ModelIDFromDisplay(name); got != want {
			t.Errorf("ModelIDFromDisplay(%q) = %q, want %q", name, got, want)
		}
	}
}
