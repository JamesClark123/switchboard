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

// ModelDisplays lists the catalog's display names in its order, for messages.
func ModelDisplays() []string {
	out := make([]string, 0, len(Models))
	for _, m := range Models {
		out = append(out, m.Display)
	}
	return out
}

// ModelIDFromDisplay derives the ID a "<Family> <version>" display name maps
// to: claude-<family>-<major>[-<minor>], the family lower-cased, the version's
// dot a dash, and a whole version (`5`, that is 5.0) with no minor part. It
// is "" for anything else. The catalog test keeps every entry consistent with
// this rule; it is not a way to accept names outside the catalog.
func ModelIDFromDisplay(name string) string {
	family, version, ok := strings.Cut(name, " ")
	if !ok || family == "" || version == "" || strings.ContainsAny(family, " .") {
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
