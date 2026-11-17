package switchboardproto

import (
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// --- SWI-34: the model catalog ---

// Model is one catalog entry: a specific Claude model as Claude Code's
// `/model` picker offers it. Display is the name the picker shows and the
// confirmation line reads back (`Set model to Fable 5.1 …`); ID is what
// `/model <ID>` takes to pin it; Family is the alias it belongs to (`/model
// fable` picks the family's latest).
type Model struct {
	Display string
	ID      string
	Family  pb.ModelChoice
}

// Models is the catalog, in the picker's order: its main rows, then those
// under "More models". Captured from Claude Code 2.1.326 on 2026-11-17, each
// ID confirmed accepted by `/model <ID>` with its Display read back
// (agent/claudeui_capture_modelid_test.go). It is the one source of truth for
// the action form's picker and the daemon's validation, updated by hand when
// Claude ships a model; there are no free-text IDs.
var Models = []Model{
	{"Opus 5.5", "claude-opus-5-5", pb.ModelChoice_MODEL_CHOICE_OPUS},
	{"Fable 5.2", "claude-fable-5-2", pb.ModelChoice_MODEL_CHOICE_FABLE},
	{"Sonnet 5.5", "claude-sonnet-5-5", pb.ModelChoice_MODEL_CHOICE_SONNET},
	{"Haiku 5.5", "claude-haiku-5-5", pb.ModelChoice_MODEL_CHOICE_HAIKU},
	{"Fable 5.1", "claude-fable-5-1", pb.ModelChoice_MODEL_CHOICE_FABLE},
	{"Haiku 4.5", "claude-haiku-4-5", pb.ModelChoice_MODEL_CHOICE_HAIKU},
	{"Sonnet 5", "claude-sonnet-5", pb.ModelChoice_MODEL_CHOICE_SONNET},
	{"Opus 5", "claude-opus-5", pb.ModelChoice_MODEL_CHOICE_OPUS},
	{"Fable 5", "claude-fable-5", pb.ModelChoice_MODEL_CHOICE_FABLE},
	{"Opus 4.8", "claude-opus-4-8", pb.ModelChoice_MODEL_CHOICE_OPUS},
	{"Opus 4.7", "claude-opus-4-7", pb.ModelChoice_MODEL_CHOICE_OPUS},
	{"Opus 4.6", "claude-opus-4-6", pb.ModelChoice_MODEL_CHOICE_OPUS},
	{"Sonnet 4.6", "claude-sonnet-4-6", pb.ModelChoice_MODEL_CHOICE_SONNET},
}

// ModelByID finds the catalog model with exactly this ID.
func ModelByID(id string) (Model, bool) {
	for _, m := range Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// ModelByDisplay finds the catalog model with this display name, ignoring
// case (a worker reads a step's expect lower-cased).
func ModelByDisplay(name string) (Model, bool) {
	for _, m := range Models {
		if strings.EqualFold(m.Display, name) {
			return m, true
		}
	}
	return Model{}, false
}

// ModelNames lists the catalog as "<Display> (<ID>)", in its order, for
// messages.
func ModelNames() []string {
	out := make([]string, 0, len(Models))
	for _, m := range Models {
		out = append(out, m.Display+" ("+m.ID+")")
	}
	return out
}

// ModelIDFromDisplay derives the ID a "<Family> <version>" display name maps
// to: claude-<family>-<major>[-<minor>], the family (letters) lower-cased,
// the version's dot a dash, and a whole version (`5`, that is 5.0) with no
// minor part. It is "" for anything else. The catalog test keeps every entry
// consistent with this rule, and a worker derives a pinned step's command
// from the name it is to read back (the command can only ever be `/model
// claude-<letters>-<digits>[-<digits>]`); it is not a way to save names
// outside the catalog.
func ModelIDFromDisplay(name string) string {
	family, version, ok := strings.Cut(name, " ")
	if !ok || !letters(family) || version == "" {
		return ""
	}
	major, minor, dotted := strings.Cut(version, ".")
	if !digits(major) || (dotted && !digits(minor)) {
		return ""
	}
	id := "claude-" + strings.ToLower(family) + "-" + major
	if minor != "" && strings.TrimLeft(minor, "0") != "" {
		id += "-" + minor
	}
	return id
}

// ModelDisplayFromID is the inverse of ModelIDFromDisplay: the "<Family>
// <major>[.<minor>]" name a well-formed ID (claude-<letters>-<digits>[-<digits>],
// lower-case) reads back as, "" for anything else. It names a pinned model the
// catalog does not know (one stored before the catalog changed).
func ModelDisplayFromID(id string) string {
	rest, ok := strings.CutPrefix(id, "claude-")
	if !ok {
		return ""
	}
	parts := strings.Split(rest, "-")
	if len(parts) < 2 || len(parts) > 3 || !letters(parts[0]) || strings.ToLower(parts[0]) != parts[0] {
		return ""
	}
	for _, v := range parts[1:] {
		if !digits(v) {
			return ""
		}
	}
	name := strings.ToUpper(parts[0][:1]) + parts[0][1:] + " " + parts[1]
	if len(parts) == 3 {
		name += "." + parts[2]
	}
	return name
}

// ModelStep is what a sequence's Model step types after `/model` and the name
// it must read back (`Set model to <expect> …`): a pinned model's ID with its
// display name, from the catalog or, for an ID the catalog lacks, derived
// from the ID; else the family alias with the family. A pin that is no
// well-formed ID at all runs as its family alias.
func ModelStep(seq *pb.ActionSequence) (typed, expect string) {
	if id := seq.GetModelId(); id != "" {
		if m, ok := ModelByID(id); ok {
			return id, m.Display
		}
		if name := ModelDisplayFromID(id); name != "" {
			return id, name
		}
	}
	family := ModelChoiceLabel(seq.GetModel())
	return strings.ToLower(family), family
}

// digits reports whether s is one or more ASCII digits.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// letters reports whether s is one or more ASCII letters.
func letters(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}
