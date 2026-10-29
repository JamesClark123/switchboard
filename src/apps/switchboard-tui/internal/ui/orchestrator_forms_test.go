package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// SWI-17 (FR-200..FR-204, FR-210, FR-218, FR-219): the action form's sequence
// fields, their save, the inline FR-204 checks and the action line's summary.

// defaultSeq is the sequence every action has unless edited (FR-203, FR-210).
func defaultSeq() *pb.ActionSequence {
	return &pb.ActionSequence{ModelEnabled: true, Model: pb.ModelChoice_MODEL_CHOICE_OPUS, EffortEnabled: true, Effort: pb.EffortChoice_EFFORT_CHOICE_MEDIUM, ClearEnabled: true}
}

// saveAction submits the open action form and returns the action it sent.
func saveAction(t *testing.T, m Model, d *fakeDaemon) (Model, *pb.Action) {
	t.Helper()
	before := len(d.putActions)
	m, cmd := update(m, ctrlS())
	m = feed2(m, cmd)
	if len(d.putActions) != before+1 {
		t.Fatalf("no PutAction: status %q", m.jobs.status)
	}
	return m, d.putActions[len(d.putActions)-1]
}

func TestActionFormDefaultsOnNewAction(t *testing.T) {
	m, d := jobsWith(t, jobsActions, nil, nil)
	m, _ = update(m, press("a"))
	v := m.jobs.formVals.(*actionFormVals)
	if v.model != "opus" || v.effort != "medium" || !v.clearAfter || v.pre != "" || v.post != "" {
		t.Errorf("new action defaults = %+v", v)
	}
	v.name, v.body = "triage", "Triage it"
	_, a := saveAction(t, m, d)
	if !proto.Equal(a.GetSequence(), defaultSeq()) {
		t.Errorf("sent sequence = %v", a.GetSequence())
	}
}

// A legacy action (no sequence) shows the defaults and saves them explicitly.
func TestActionFormLegacyShowsAndSavesDefaults(t *testing.T) {
	m, d := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
	m, _ = update(m, press("e"))
	v := m.jobs.formVals.(*actionFormVals)
	if v.model != "opus" || v.effort != "medium" || !v.clearAfter {
		t.Errorf("legacy action = %+v", v)
	}
	_, a := saveAction(t, m, d)
	if !proto.Equal(a.GetSequence(), defaultSeq()) {
		t.Errorf("legacy save = %v", a.GetSequence())
	}
}

// The options match the spec: no ultracode (US1 #5), off first.
func TestActionFormOptionLists(t *testing.T) {
	if strings.Join(modelOptions, ",") != "off,opus,fable,sonnet,haiku" || strings.Join(effortOptions, ",") != "off,low,medium,high,xhigh,max,auto" {
		t.Errorf("options: %v %v", modelOptions, effortOptions)
	}
	m, _ := jobsWith(t, jobsActions, nil, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 160, Height: 120})
	m, _ = update(m, press("a"))
	m.jobs.form.NextGroup() // the sequence follows the arguments, a page of their own
	for _, want := range []string{"Model", "Effort", "Clear after run", "Pre-prompts", "Post-prompts", "one prompt per line"} {
		if !strings.Contains(ansi.Strip(m.View()), want) {
			t.Errorf("form missing %q", want)
		}
	}
	for _, c := range modelOptions[1:] {
		if modelChoice(c) == pb.ModelChoice_MODEL_CHOICE_UNSPECIFIED {
			t.Errorf("model %q unmapped", c)
		}
	}
	for _, c := range effortOptions[1:] {
		if effortChoice(c) == pb.EffortChoice_EFFORT_CHOICE_UNSPECIFIED {
			t.Errorf("effort %q unmapped", c)
		}
	}
	if modelChoice("ultracode") != pb.ModelChoice_MODEL_CHOICE_UNSPECIFIED || effortChoice("ultracode") != pb.EffortChoice_EFFORT_CHOICE_UNSPECIFIED {
		t.Error("ultracode is not offered")
	}
}

// off round-trips and keeps the stored value; choosing a value re-enables it.
func TestActionFormOffKeepsValue(t *testing.T) {
	a := reviewAction()
	a.Sequence = &pb.ActionSequence{ModelEnabled: false, Model: pb.ModelChoice_MODEL_CHOICE_HAIKU, EffortEnabled: false, Effort: pb.EffortChoice_EFFORT_CHOICE_AUTO, ClearEnabled: false,
		PrePrompts: []string{"/setup {repo}"}, PostPrompts: []string{"/tear-down", "/notes"}}
	m, d := jobsWith(t, jobsActions, []*pb.Action{a}, nil)
	m, _ = update(m, press("e"))
	v := m.jobs.formVals.(*actionFormVals)
	if v.model != "off" || v.modelKept != "haiku" || v.effort != "off" || v.effortKept != "auto" || v.clearAfter || v.pre != "/setup {repo}" || v.post != "/tear-down\n/notes" {
		t.Fatalf("loaded = %+v", v)
	}
	m, got := saveAction(t, m, d)
	if !proto.Equal(got.GetSequence(), a.GetSequence()) {
		t.Errorf("off round-trip = %v", got.GetSequence())
	}
	m, _ = update(m, press("e"))
	v = m.jobs.formVals.(*actionFormVals)
	v.model, v.effort = "sonnet", "high"
	_, got = saveAction(t, m, d)
	if s := got.GetSequence(); !s.GetModelEnabled() || s.GetModel() != pb.ModelChoice_MODEL_CHOICE_SONNET || !s.GetEffortEnabled() || s.GetEffort() != pb.EffortChoice_EFFORT_CHOICE_HIGH {
		t.Errorf("re-enabled = %v", s)
	}
	// An unknown stored value falls back to the default it would show.
	w := &actionFormVals{}
	sequenceToForm(w, &pb.ActionSequence{})
	if w.model != "off" || w.modelKept != "opus" || w.effortKept != "medium" {
		t.Errorf("unspecified = %+v", w)
	}
}

// The prompts round-trip trimmed, empty lines dropped; a refusal is verbatim.
func TestActionFormPromptsAndRefusal(t *testing.T) {
	m, d := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
	m, _ = update(m, press("e"))
	v := m.jobs.formVals.(*actionFormVals)
	v.pre, v.post = "  /pre-setupskill  \n\n  check {repo} please\n", "\n/tear-down-skill\n  "
	m, a := saveAction(t, m, d)
	if got := a.GetSequence(); strings.Join(got.GetPrePrompts(), "|") != "/pre-setupskill|check {repo} please" || strings.Join(got.GetPostPrompts(), "|") != "/tear-down-skill" {
		t.Errorf("prompts = %v / %v", got.GetPrePrompts(), got.GetPostPrompts())
	}
	m, _ = update(m, press("e"))
	d.orchErr = status.Error(codes.InvalidArgument, "pre-prompt 2: {skill} may not be in the first word")
	m, cmd := update(m, ctrlS())
	m = feed(m, cmd)
	if m.jobs.form == nil || !strings.Contains(m.jobs.status, "pre-prompt 2: {skill} may not be in the first word") {
		t.Errorf("refusal: form %v status %q", m.jobs.form != nil, m.jobs.status)
	}
}

// The FR-204 rules are checked before the round trip, worded as the daemon's.
func TestActionFormInlineChecks(t *testing.T) {
	m, d := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
	m, _ = update(m, press("e"))
	v := m.jobs.formVals.(*actionFormVals)
	for _, c := range []struct{ pre, post, want string }{
		{"{repo} first", "", "pre-prompt 1: placeholder {repo} is in the first word; start with your own text"},
		{"ok\nuse {nope}", "", "pre-prompt 2: placeholder {nope} names no argument"},
		{"", "!git log {repo}", "post-prompt 1: a prompt starting with ! may not contain a placeholder ({repo})"},
		{strings.Repeat("p\n", 11), "", "more than 10 pre-prompts"},
		{"x " + strings.Repeat("y", maxStepPromptBytes), "", "pre-prompt 1 is larger than 16384 bytes"},
	} {
		v.pre, v.post = c.pre, c.post
		m.jobs.status = ""
		mm, _ := update(m, ctrlS())
		if !strings.Contains(mm.jobs.status, c.want) {
			t.Errorf("status %q, want %q", mm.jobs.status, c.want)
		}
	}
	if strings.Contains(d.orchCallsJoined(), "PutAction") {
		t.Error("a refused form sends nothing")
	}
	if err := checkStepPrompts("pre-prompt", []string{"/x {{literal}} {repo}"}, reviewAction().GetArguments()); err != nil {
		t.Errorf("{{ is a literal: %v", err)
	}
}

// The unsaved-changes guard sees the sequence fields.
func TestActionFormGuardSeesSequence(t *testing.T) {
	noKeyGrace(t)
	for _, edit := range []func(v *actionFormVals){
		func(v *actionFormVals) { v.model = "haiku" },
		func(v *actionFormVals) { v.effort = "off" },
		func(v *actionFormVals) { v.clearAfter = false },
		func(v *actionFormVals) { v.pre = "/x" },
		func(v *actionFormVals) { v.post = "/y" },
	} {
		m, _ := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
		m, _ = update(m, press("e"))
		if m.guardDirty(gOpJobs) {
			t.Fatal("an untouched form is clean")
		}
		edit(m.jobs.formVals.(*actionFormVals))
		if !m.guardDirty(gOpJobs) {
			t.Errorf("edit not seen by the guard: %+v", m.jobs.formVals)
		}
	}
	// A Model change made through the Select is seen too.
	m, _ := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
	m, cmd := update(m, press("e"))
	m = feed(m, cmd)
	// The arguments and sequence sit in the last group (SWI-18's lanes have
	// groups of their own): cross groups at a group's end.
	for i := 0; i < 20 && m.jobs.form.GetFocusedField().GetKey() != "model"; i++ {
		before := m.jobs.form.GetFocusedField()
		m.jobs.form.NextField()
		if m.jobs.form.GetFocusedField() == before {
			m.jobs.form.NextGroup()
		}
	}
	if m.jobs.form.GetFocusedField().GetKey() != "model" {
		t.Fatal("could not reach the Model field")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	if v := m.jobs.formVals.(*actionFormVals); v.model != "fable" || !m.guardDirty(gOpJobs) {
		t.Errorf("model %q, dirty %v", v.model, m.guardDirty(gOpJobs))
	}
}

// wideView renders m on a wide terminal, so a long row is not cut.
func wideView(m Model) string {
	m, _ = update(m, tea.WindowSizeMsg{Width: 220, Height: 50})
	return m.View()
}

func TestActionLineSequenceSummary(t *testing.T) {
	a := reviewAction()
	if got := sequenceSummary(a); got != "pre: /model opus · /effort medium — post: /clear" {
		t.Errorf("legacy summary = %q", got)
	}
	a.Sequence = &pb.ActionSequence{ModelEnabled: true, Model: pb.ModelChoice_MODEL_CHOICE_OPUS, EffortEnabled: true, Effort: pb.EffortChoice_EFFORT_CHOICE_MEDIUM, ClearEnabled: true,
		PrePrompts: []string{"/a"}, PostPrompts: []string{"/b"}}
	if got := sequenceSummary(a); got != "pre: /model opus · /effort medium · 1 custom — post: 1 custom · /clear" {
		t.Errorf("summary = %q", got)
	}
	a.Sequence = &pb.ActionSequence{PostPrompts: []string{"/b"}}
	if got := sequenceSummary(a); got != "post: 1 custom" {
		t.Errorf("post only = %q", got)
	}
	a.Sequence = &pb.ActionSequence{}
	if got := sequenceSummary(a); got != "" || strings.Contains(actionLine(a, nil), "pre:") {
		t.Errorf("no steps = %q", got)
	}
	m, _ := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
	if !strings.Contains(ansi.Strip(wideView(m)), "pre: /model opus · /effort medium — post: /clear") {
		t.Errorf("actions tab:\n%s", ansi.Strip(wideView(m)))
	}
}
