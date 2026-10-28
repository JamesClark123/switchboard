package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// textForm is one form of the app that has a multi-line Text field, built through
// its real builder so the test sees exactly what the user gets.
type textForm struct {
	name  string
	form  *huh.Form
	spec  *formSpec
	texts []*huh.Text
}

// textForms builds every kind of form that carries a Text field: the kit (a
// section and an item form), MCP, action and event.
func textForms(t *testing.T) []textForm {
	t.Helper()
	m := sized(kitModel(t, &fakeDaemon{}))
	var out []textForm
	add := func(name string, f *huh.Form, spec *formSpec) {
		var texts []*huh.Text
		for txt := range spec.texts {
			texts = append(texts, txt)
		}
		if len(texts) == 0 {
			t.Fatalf("%s: no Text fields registered", name)
		}
		out = append(out, textForm{name: name, form: f, spec: spec, texts: texts})
	}

	spec := newFormSpec()
	add("kit agent instructions", m.buildKitForm(formAgentInstructions, spec, &kitFormVals{}), spec)
	spec = newFormSpec()
	add("kit escape hatch", m.buildKitForm(formEscapeHatch, spec, &kitFormVals{}), spec)
	spec = newFormSpec()
	add("mcp", m.mcpForm(spec, &mcpFormVals{kind: "remote"}), spec)

	mm, _ := m.openActionForm(surfaceJobs, orchTarget{}, nil)
	op := mm.(Model).jobs.orchOpState
	add("action", op.form, op.formSpec)

	rec := &pb.Orchestrator{Actions: []*pb.Action{{Name: "triage"}}}
	mm, _ = m.openEventForm(surfaceJobs, orchTarget{rec: rec}, nil)
	op = mm.(Model).jobs.orchOpState
	add("event", op.form, op.formSpec)
	return out
}

// focusField moves the form's focus onto target, searching forward and then
// back (huh does not wrap, and callers visit fields in map order).
func focusField(t *testing.T, f *huh.Form, target huh.Field) {
	t.Helper()
	for _, step := range []func() tea.Cmd{f.NextField, f.PrevField} {
		for range 20 {
			if f.GetFocusedField() == target {
				return
			}
			step()
		}
	}
	t.Fatalf("could not focus %T", target)
}

// cmdLaunchesProcess runs cmd (and any batch or sequence inside it) and reports
// whether it would hand the terminal to an external process. Commands that do
// not return quickly (cursor-blink ticks) are skipped.
func cmdLaunchesProcess(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(50 * time.Millisecond):
		return false
	}
	if strings.Contains(fmt.Sprintf("%T", msg), "exec") {
		return true
	}
	v := reflect.ValueOf(msg)
	if v.Kind() == reflect.Slice {
		for i := range v.Len() {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok && cmdLaunchesProcess(c) {
				return true
			}
		}
	}
	return false
}

func TestFormKeyMapEditorIsKeyless(t *testing.T) {
	km := formKeyMap()
	if len(km.Text.Editor.Keys()) != 0 {
		t.Errorf("Text.Editor keys = %v, want none", km.Text.Editor.Keys())
	}
	if key.Matches(tea.KeyMsg{Type: tea.KeyCtrlE}, km.Text.Editor) {
		t.Error("ctrl+e must not match the editor binding")
	}
}

// ctrl+e on a focused Text must never launch $EDITOR, even on a form that hides
// help (where huh's ExternalEditor(false) alone would not disable it).
func TestCtrlEOnTextNeverLaunchesEditor(t *testing.T) {
	t.Setenv("EDITOR", "vi")
	for _, tf := range textForms(t) {
		tf.form.WithShowHelp(false)
		tf.form.Init()
		for _, txt := range tf.texts {
			focusField(t, tf.form, txt)
			_, cmd := tf.form.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
			if cmdLaunchesProcess(cmd) {
				t.Errorf("%s: ctrl+e on %q launches an external process", tf.name, tf.spec.texts[txt].title)
			}
		}
	}
}

func TestNoFormAdvertisesAnEditor(t *testing.T) {
	for _, tf := range textForms(t) {
		tf.form.Init()
		for _, txt := range tf.texts {
			focusField(t, tf.form, txt)
			for _, b := range tf.form.KeyBinds() {
				if b.Enabled() && strings.Contains(b.Help().Desc, "editor") {
					t.Errorf("%s: help offers %q", tf.name, b.Help().Desc)
				}
			}
		}
	}
}

func TestFormSpecNormalizeReportsRewrittenText(t *testing.T) {
	m := sized(kitModel(t, &fakeDaemon{}))

	v := &kitFormVals{agentInstructions: "a\tb"}
	spec := newFormSpec()
	m.buildKitForm(formAgentInstructions, spec, v)
	got := spec.normalize()
	if strings.Contains(v.agentInstructions, "\t") {
		t.Errorf("tab survived normalisation: %q", v.agentInstructions)
	}
	if len(got) != 1 || got[0] != "Agent instructions" {
		t.Errorf("normalize() = %v, want the agent instructions field", got)
	}

	v = &kitFormVals{agentInstructions: "one\r\ntwo\r\nthree"}
	spec = newFormSpec()
	m.buildKitForm(formAgentInstructions, spec, v)
	got = spec.normalize()
	if n := strings.Count(v.agentInstructions, "\n") + 1; n != 3 {
		t.Errorf("CRLF content has %d lines after normalisation, want 3 (%q)", n, v.agentInstructions)
	}
	if len(got) != 1 {
		t.Errorf("CRLF content not reported: %v", got)
	}
	if notice := normalizeNotice(got); !strings.Contains(notice, "line endings") {
		t.Errorf("notice %q does not mention line endings", notice)
	}

	v = &kitFormVals{agentInstructions: "plain\ntext"}
	spec = newFormSpec()
	m.buildKitForm(formAgentInstructions, spec, v)
	if got := spec.normalize(); len(got) != 0 {
		t.Errorf("clean text reported as rewritten: %v", got)
	}
}

// Every Text in the app must be built through formSpec.text, so none can bring
// the external editor back or escape the enlarged view's registry.
func TestNoTextBuiltOutsideFormSpec(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "formguard.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "huh.NewText(") {
			t.Errorf("%s builds a huh.Text directly; use formSpec.text", f)
		}
	}
}
