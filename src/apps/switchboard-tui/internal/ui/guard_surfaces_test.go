package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// formCase is one multi-field form for the US1 walk (SWI-9 T016).
type formCase struct {
	name string
	g    guardSurface
	// open returns the model with the form open and unchanged, and a probe
	// reporting whether a save reached the store or the daemon.
	open func(t *testing.T) (Model, func(Model) bool)
	// edit changes the form through the keys.
	edit func(m Model) Model
	// valid and invalid set values that save, or that the save refuses (nil:
	// the form has no client-side validation). Both leave the form changed.
	valid, invalid func(m *Model)
	// status is where the surface shows a refusal.
	status func(m Model) string
	// typed: the first field takes text, so type-and-delete applies.
	typed bool
}

func jobsStatus(m Model) string { return m.jobs.status }

func formCases() []formCase {
	typeX := func(m Model) Model { return typeIn(m, "x") }
	return []formCase{
		{
			name: "config", g: gConfig, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				cs := newConfigStore(t)
				m := sized(New(&fakeDaemon{manifest: testManifest()}, "/work").WithConfigs(cs))
				m, cmd := update(m, press("c"))
				m, _ = update(m, runCmd(cmd))
				return m, func(Model) bool { l, _ := cs.List(); return len(l) > 0 }
			},
			edit:    typeX,
			valid:   func(m *Model) { m.editor.vals.name = "backend" },
			invalid: func(m *Model) { m.editor.vals.name, m.editor.vals.mode = "", "clone" },
			status:  func(m Model) string { return m.editor.status },
		},
		{
			name: "mcp", g: gMcp, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				d := &fakeDaemon{}
				m := pressCmd(mcpModel(t, d), "a")
				return m, func(Model) bool { return len(d.addedMcp) > 0 }
			},
			edit:    typeX,
			valid:   func(m *Model) { m.mcpView.vals.name, m.mcpView.vals.url = "gh", "https://x/mcp" },
			invalid: func(m *Model) { m.mcpView.vals.name = "bad name" },
			status:  func(m Model) string { return m.mcpView.status },
		},
		{
			name: "secrets", g: gSecrets, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				d := &fakeDaemon{}
				m := pressCmd(secretsModel(t, d), "a")
				return m, func(Model) bool { return len(d.setSecrets) > 0 }
			},
			edit:    typeX,
			valid:   func(m *Model) { m.secretsView.vals.name, m.secretsView.vals.value = "tok", "v" },
			invalid: func(m *Model) { m.secretsView.vals.name, m.secretsView.vals.value = "a b", "v" },
			status:  func(m Model) string { return m.secretsView.status },
		},
		{
			name: "orchestrator create", g: gOrchAdmin, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
				b := &fakeDaemon{}
				m, _, _ := twoFakeHosts(t, a, b)
				m, _ = update(openDaemonOrchs(t, m, "hostb"), press("n"))
				return m, func(Model) bool { return strings.Contains(b.orchCallsJoined(), "CreateOrchestrator") }
			},
			edit:    typeX,
			valid:   func(m *Model) { m.orch.vals.name = "new-bot" },
			invalid: func(m *Model) { m.orch.vals.name = strings.Repeat("y", 65) },
			status:  func(m Model) string { return m.status },
		},
		{
			name: "orchestrator rename", g: gOrchAdmin, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
				m, _, _ := twoFakeHosts(t, a, &fakeDaemon{})
				m, _ = update(openDaemonOrchs(t, m, "hosta"), press("R"))
				return m, func(Model) bool { return strings.Contains(a.orchCallsJoined(), "RenameOrchestrator") }
			},
			edit:    typeX,
			valid:   func(m *Model) { m.orch.vals.name = "renamed" },
			invalid: func(m *Model) { m.orch.vals.name = " " },
			status:  func(m Model) string { return m.status },
		},
		{
			name: "action", g: gOpJobs, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				m, a := jobsWith(t, jobsActions, nil, nil)
				m, _ = update(m, press("a"))
				return m, func(Model) bool { return strings.Contains(a.orchCallsJoined(), "PutAction") }
			},
			edit: typeX,
			valid: func(m *Model) {
				v := m.jobs.formVals.(*actionFormVals)
				v.name, v.body = "triage", "Look"
			},
			invalid: func(m *Model) { m.jobs.formVals.(*actionFormVals).args = "1st-arg * bad name" },
			status:  jobsStatus,
		},
		{
			name: "event", g: gOpJobs, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				m, a := jobsWith(t, jobsEvents, []*pb.Action{reviewAction()}, nil)
				m, _ = update(m, press("a"))
				return m, func(Model) bool { return strings.Contains(a.orchCallsJoined(), "PutTimedEvent") }
			},
			edit: typeX,
			valid: func(m *Model) {
				v := m.jobs.formVals.(*eventFormVals)
				v.name, v.intervalMinutes = "poll", "5"
			},
			invalid: func(m *Model) { m.jobs.formVals.(*eventFormVals).args = "nonsense" },
			status:  jobsStatus,
		},
		{
			name: "call", g: gOpJobs, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				m, a := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
				m, _ = update(m, press("enter"))
				return m, func(Model) bool { return strings.Contains(a.orchCallsJoined(), "CallAction") }
			},
			edit:  func(m Model) Model { return typeIn(m, "7") },
			valid: func(m *Model) { *m.jobs.formVals.(*callFormVals).values["number"] = "7" },
			invalid: func(m *Model) {
				v := m.jobs.formVals.(*callFormVals)
				*v.values["number"], *v.values["repo"] = "", "x/y"
			},
			status: jobsStatus,
		},
		{
			name: "mark free", g: gOpJobs,
			open: func(t *testing.T) (Model, func(Model) bool) {
				runs := &pb.ListRunsResponse{InFlight: []*pb.Run{{Id: "run-r", Action: "review-pr", State: pb.RunState_RUN_STATE_AWAITING_REVIEW, SandboxId: "sb-review"}}}
				m, a := jobsWith(t, jobsOverview, []*pb.Action{reviewAction()}, runs)
				m, _ = update(m, press("m"))
				if m.jobs.formKind != "mark free" {
					t.Fatalf("m opened %q", m.jobs.formKind)
				}
				return m, func(Model) bool { return strings.Contains(a.orchCallsJoined(), "MarkWorkerFree") }
			},
			edit:   func(m Model) Model { m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}); return m },
			valid:  func(m *Model) { m.jobs.formVals.(*markFreeVals).note = "done" },
			status: jobsStatus,
		},
		{
			name: "kit section", g: gKitItem, typed: true,
			open: func(t *testing.T) (Model, func(Model) bool) {
				m := editorOn(t, &store.Kit{Name: "bare"})
				for m.kitEditor.section != secEnvironment {
					m, _ = update(m, press("j"))
				}
				m = pressCmd(m, "enter")
				if m.kitEditor.form == nil {
					t.Fatal("enter should open the environment form")
				}
				return m, func(m Model) bool { return m.kitEditor.kit.Environment != nil }
			},
			edit:    typeX,
			valid:   func(m *Model) { m.kitEditor.vals.vars = "A=1" },
			invalid: func(m *Model) { m.kitEditor.vals.vars = "NOEQUALS" },
			status:  func(m Model) string { return m.kitEditor.status },
		},
	}
}

// completeForm tabs to the last field and presses enter there, as a user
// finishing the form does, until the surface reports something. It returns
// false when the last field is a Text (enter inserts a newline there).
func completeForm(t *testing.T, m *Model, g guardSurface, status func(Model) string) bool {
	t.Helper()
	for range 25 {
		if status(*m) != "" {
			return true
		}
		f := m.guardForm(g)
		if f == nil {
			t.Fatalf("the form closed; status %q", status(*m))
		}
		before := f.GetFocusedField()
		mm, cmd := update(*m, tea.KeyMsg{Type: tea.KeyTab})
		*m = settle(mm, cmd)
		if f := m.guardForm(g); f != nil && f.GetFocusedField() == before {
			if _, ok := before.(*huh.Text); ok {
				return false
			}
			mm, cmd = update(*m, tea.KeyMsg{Type: tea.KeyEnter})
			*m = settle(mm, cmd)
		}
	}
	t.Fatalf("the form never completed; status %q", status(*m))
	return false
}

// onGuard reports whether g is still the open surface.
func onGuard(m Model, g guardSurface) bool {
	a, ok := m.activeGuard()
	return ok && a == g
}

// rebaseline makes the current values the surface's baseline, as a background
// update that changed the values under the dialog would leave it.
func rebaseline(m *Model, g guardSurface) {
	switch g {
	case gConfig:
		m.editor.baseline = m.editor.vals.snapshot()
	case gMcp:
		m.mcpView.baseline = m.mcpView.mcpSnapshot()
	case gSecrets:
		m.secretsView.baseline = *m.secretsView.vals
	case gOrchAdmin:
		m.orch.baseline = *m.orch.vals
	case gOpJobs:
		m.jobs.formBaseline = snapshotOpVals(m.jobs.formVals)
	case gKitItem:
		m.kitEditor.formBaseline = *m.kitEditor.vals
	}
}

// specOf is the Text registry of g's form, when it has one.
func specOf(m Model, g guardSurface) *formSpec {
	switch g {
	case gMcp:
		return m.mcpView.spec
	case gOpJobs:
		return m.jobs.formSpec
	case gKitItem:
		return m.kitEditor.spec
	}
	return nil
}

// settle runs a key's commands and feeds their messages back, following the
// commands those updates return for a few rounds: huh completes a form two
// hops after the key (nextField, then nextGroup), and async saves reply after.
// Commands that do not return promptly (cursor blink ticks) are dropped.
func settle(m Model, cmd tea.Cmd) Model {
	for range 6 {
		if cmd == nil {
			break
		}
		var next []tea.Cmd
		for _, msg := range quickMsgs(cmd) {
			var c tea.Cmd
			m, c = update(m, msg)
			next = append(next, c)
		}
		cmd = tea.Batch(next...)
	}
	return m
}

// settleHolding is settle that keeps back the messages hold picks (a slow
// daemon's replies) and returns them.
func settleHolding(m Model, cmd tea.Cmd, hold func(tea.Msg) bool) (Model, []tea.Msg) {
	var held []tea.Msg
	for range 6 {
		if cmd == nil {
			break
		}
		var next []tea.Cmd
		for _, msg := range quickMsgs(cmd) {
			if hold(msg) {
				held = append(held, msg)
				continue
			}
			var c tea.Cmd
			m, c = update(m, msg)
			next = append(next, c)
		}
		cmd = tea.Batch(next...)
	}
	return m, held
}

// quickMsgs runs cmd (and any batch or sequence inside it) with a short timeout.
func quickMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(50 * time.Millisecond):
		return nil
	}
	if msg == nil {
		return nil
	}
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice {
		var out []tea.Msg
		for i := range v.Len() {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				out = append(out, quickMsgs(c)...)
			}
		}
		return out
	}
	return []tea.Msg{msg}
}

func TestGuardedFormsUS1(t *testing.T) {
	noKeyGrace(t)
	for _, c := range formCases() {
		t.Run(c.name, func(t *testing.T) {
			// Unchanged: esc closes at once.
			m, _ := c.open(t)
			if !onGuard(m, c.g) || m.guardDirty(c.g) {
				t.Fatalf("form not open clean: dirty=%v", m.guardDirty(c.g))
			}
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
			if m.unsaved.open || onGuard(m, c.g) {
				t.Fatal("esc on an unchanged form should close it with no dialog")
			}

			// Type-and-delete back to the original is unchanged (US1 #7).
			if c.typed {
				m, _ = c.open(t)
				m = typeIn(m, "zz")
				m, _ = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
				m, _ = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
				m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
				if m.unsaved.open || onGuard(m, c.g) {
					t.Error("an edit undone back to the original should not ask")
				}
			}

			// Changed: esc opens the dialog; Cancel leaves the form identical.
			m, saved := c.open(t)
			m = c.edit(m)
			if !m.guardDirty(c.g) {
				t.Fatal("edit did not change the form")
			}
			form := m.guardForm(c.g)
			focused, view := form.GetFocusedField(), form.View()
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
			if !m.unsaved.open || m.unsaved.surface != c.g {
				t.Fatal("esc on a changed form should open the dialog")
			}
			m, _ = update(m, press("c"))
			if m.unsaved.open || m.guardForm(c.g) != form || form.GetFocusedField() != focused || form.View() != view || !m.guardDirty(c.g) {
				t.Fatal("Cancel should return to the form exactly as it was")
			}

			// Discard closes with nothing saved.
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
			m, _ = update(m, press("d"))
			if m.unsaved.open || onGuard(m, c.g) || saved(m) {
				t.Fatalf("Discard: open=%v saved=%v", onGuard(m, c.g), saved(m))
			}

			// Save performs the surface's save.
			m, saved = c.open(t)
			m = c.edit(m)
			c.valid(&m)
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
			m, cmd := update(m, press("s"))
			m = settle(m, cmd)
			if !saved(m) || onGuard(m, c.g) {
				t.Fatalf("Save: saved=%v still open=%v status %q", saved(m), onGuard(m, c.g), c.status(m))
			}

			// Save after a background update left it unchanged just leaves.
			m, saved = c.open(t)
			m = c.edit(m)
			c.valid(&m)
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
			rebaseline(&m, c.g)
			m, cmd = update(m, press("s"))
			m = settle(m, cmd)
			if saved(m) || onGuard(m, c.g) {
				t.Fatalf("Save on a clean surface: saved=%v open=%v", saved(m), onGuard(m, c.g))
			}

			if c.invalid == nil {
				return
			}
			// A refused Save keeps the form open with the error (US1 #6).
			m, saved = c.open(t)
			m = c.edit(m)
			c.invalid(&m)
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
			m, cmd = update(m, press("s"))
			m = settle(m, cmd)
			if saved(m) || !onGuard(m, c.g) || m.unsaved.open || c.status(m) == "" {
				t.Fatalf("refused Save: saved=%v open=%v status %q", saved(m), onGuard(m, c.g), c.status(m))
			}

			// Completing the form with enter on its last field and failing
			// validation leaves a live, editable form with the typed values.
			m, _ = c.open(t)
			m = c.edit(m)
			c.invalid(&m)
			// Show the set values in the widgets: huh writes a focused field's
			// text back into its pointer as focus moves through it.
			m.reopenForm(c.g)
			if !completeForm(t, &m, c.g, c.status) {
				return // ends on a Text: enter is a newline there, nothing to complete
			}
			f := m.guardForm(c.g)
			if c.status(m) == "" || f == nil || f.State != huh.StateNormal || strings.TrimSpace(f.View()) == "" || !m.guardDirty(c.g) {
				t.Fatalf("completed + refused: status %q form %v", c.status(m), f != nil)
			}
			if spec := specOf(m, c.g); spec != nil {
				if txt, ok := f.GetFocusedField().(*huh.Text); ok {
					if _, _, found := spec.lookup(txt); !found {
						t.Error("the reopened form's Text is not registered")
					}
				}
			}
			// …and it still takes keys.
			before := f.View()
			m = typeIn(m, "q")
			if m.guardForm(c.g).View() == before {
				t.Error("the reopened form ignores typing")
			}
		})
	}
}

// A stored value the textarea would rewrite (a tab) is normalised before the
// baseline: tabbing past it without editing is not a change (FR-011), and the
// surface says the text will be rewritten.
func TestGuardTabbedPastTextIsUnchanged(t *testing.T) {
	m := editorOn(t, &store.Kit{Name: "k", Environment: &store.KitEnvironment{Variables: map[string]string{"A": "b\tc"}}})
	for m.kitEditor.section != secEnvironment {
		m, _ = update(m, press("j"))
	}
	m = pressCmd(m, "enter")
	if !strings.Contains(m.kitEditor.status, "will be rewritten when saved") {
		t.Errorf("status %q should announce the rewrite", m.kitEditor.status)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.kitEditor.form != nil {
		t.Fatal("a normalised text tabbed past should close with no dialog")
	}
}

// An op form whose save the daemon refuses stays open with the refusal; one
// whose save succeeds while the dialog is up closes, dialog and all.
func TestGuardOpFormAsyncOutcomes(t *testing.T) {
	noKeyGrace(t)
	m, a := jobsWith(t, jobsActions, nil, nil)
	m, _ = update(m, press("a"))
	v := m.jobs.formVals.(*actionFormVals)
	v.name, v.body = "triage", "Look"
	a.orchErr = errBoom{}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, cmd := update(m, press("s"))
	m = settle(m, cmd)
	if m.jobs.form == nil || !strings.Contains(m.jobs.status, "boom") {
		t.Fatalf("refused save: form %v status %q", m.jobs.form != nil, m.jobs.status)
	}
	a.orchErr = nil
	// ctrl+s, then keys while the call is in flight: nothing asks or sends it
	// again (review run-465 #2); the success reply closes the form.
	calls := len(a.putActions) // PutAction itself, not the un-laned save's check
	m, cmd = update(m, ctrlS())
	if !m.jobs.formSaving || m.jobs.status != savingText {
		t.Fatalf("in flight: saving %v status %q", m.jobs.formSaving, m.jobs.status)
	}
	for _, k := range []tea.KeyMsg{ctrlS(), press("x")} {
		var c tea.Cmd
		if m, c = update(m, k); c != nil || m.unsaved.open {
			t.Fatalf("%s during the call: cmd %v dialog %v", k, c != nil, m.unsaved.open)
		}
	}
	// A reply to something else on the surface does not end the save (run-470 #1).
	m, _ = update(m, orchDoneMsg{status: "cancelled run-1", sink: surfaceJobs})
	m, _ = update(m, orchDoneMsg{status: "detached"}) // sink-less: the zero sink is the detail
	if !m.jobs.formSaving {
		t.Fatal("an unrelated reply cleared the in-flight save")
	}
	if _, c := update(m, ctrlS()); c != nil {
		t.Fatal("ctrl+s re-sent the call after an unrelated reply")
	}
	m, _ = update(m, nudgeMsg{}) // a forwarded message never re-submits
	m = settle(m, cmd)
	if m.jobs.form != nil || m.unsaved.open || m.jobs.formSaving {
		t.Fatalf("after success: form %v dialog %v", m.jobs.form != nil, m.unsaved.open)
	}
	if n := len(a.putActions) - calls; n != 1 {
		t.Errorf("the save sent %d PutAction calls, want 1", n)
	}
	// A form completed by enter, whose reply is a refusal, comes back alive with
	// the values typed.
	m, a = jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
	m, _ = update(m, press("enter"))
	m = typeIn(m, "7")
	a.orchErr = errBoom{}
	if !completeForm(t, &m, gOpJobs, jobsStatus) {
		t.Fatal("the call form should complete on enter")
	}
	cv := m.jobs.formVals.(*callFormVals)
	if m.jobs.form == nil || m.jobs.form.State != huh.StateNormal || *cv.values["number"] != "7" || !strings.Contains(m.jobs.status, "boom") {
		t.Fatalf("refused after completion: form %v status %q", m.jobs.form != nil, m.jobs.status)
	}
}

// While a save hangs, esc leaves at once (run-470 #2); the late replies land on
// the footer and never touch a form opened since (run-470 #3).
func TestGuardOpFormLeaveWhileSaving(t *testing.T) {
	noKeyGrace(t)
	for _, refused := range []bool{false, true} {
		m, a := jobsWith(t, jobsActions, nil, nil)
		m, _ = update(m, press("a"))
		v := m.jobs.formVals.(*actionFormVals)
		v.name, v.body = "triage", "Look"
		if refused {
			a.orchErr = errBoom{}
		}
		m, cmd := update(m, ctrlS())
		reply := quickMsgs(cmd) // held back: the daemon is slow
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
		if m.jobs.form != nil || m.unsaved.open || !strings.Contains(m.status, "saving") {
			t.Fatalf("esc while saving: form %v dialog %v status %q", m.jobs.form != nil, m.unsaved.open, m.status)
		}
		if m.guardDirty(gOpJobs) {
			t.Error("a saving form must not count as unsaved")
		}
		// A new form is open, and its own save in flight, when the old reply lands.
		m, _ = update(m, press("a"))
		nv := m.jobs.formVals.(*actionFormVals)
		nv.name, nv.body = "second", "Again"
		a.orchErr = nil
		m, cmd2 := update(m, ctrlS())
		for _, msg := range reply {
			m, _ = update(m, msg)
		}
		if m.jobs.form == nil || !m.jobs.formSaving || strings.Contains(m.jobs.status, "boom") {
			t.Errorf("refused=%v: the old reply touched the new form (open %v saving %v status %q)", refused, m.jobs.form != nil, m.jobs.formSaving, m.jobs.status)
		}
		// Named, and saying its values are gone (review run-475).
		if refused && (!strings.Contains(m.status, "save action triage on alpha-bot: ") || !strings.Contains(m.status, "boom") || !strings.Contains(m.status, "values were not kept")) {
			t.Errorf("the old refusal should reach the footer, named: %q", m.status)
		}
		// Its own reply still closes it.
		m = settle(m, cmd2)
		if m.jobs.form != nil {
			t.Errorf("refused=%v: the new form's own reply should close it", refused)
		}
	}
}

// A form completed by enter stays completed while its call is in flight:
// forwarded messages neither revive it nor re-submit it (run-470 #6).
func TestGuardCompletedOpFormWaitsWhileSaving(t *testing.T) {
	m, a := jobsWith(t, jobsActions, []*pb.Action{reviewAction()}, nil)
	m, _ = update(m, press("enter"))
	m = typeIn(m, "7")
	calls := strings.Count(a.orchCallsJoined(), "CallAction")
	for range 3 { // to the last field (focus); tab's advance runs in its cmd
		mm, c := update(m, tea.KeyMsg{Type: tea.KeyTab})
		m = settle(mm, c)
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, pending := settleHolding(m, cmd, func(msg tea.Msg) bool { _, ok := msg.(orchDoneMsg); return ok })
	if !m.jobs.formSaving || m.jobs.form == nil || m.jobs.form.State != huh.StateCompleted {
		t.Fatalf("enter should complete the form and send it: saving %v status %q", m.jobs.formSaving, m.jobs.status)
	}
	for range 3 {
		m, _ = update(m, nudgeMsg{})
	}
	if m.jobs.form.State != huh.StateCompleted {
		t.Error("a forwarded message revived the form mid-save")
	}
	if n := strings.Count(a.orchCallsJoined(), "CallAction") - calls; n != 1 {
		t.Errorf("%d CallAction calls, want 1", n)
	}
	for _, msg := range pending {
		m, _ = update(m, msg)
	}
	if m.jobs.form != nil {
		t.Error("the reply should close the form")
	}
}

// quits reports whether cmd (or a batch inside it) would quit the program.
func quits(cmd tea.Cmd) bool {
	for _, msg := range quickMsgs(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			return true
		}
	}
	return false
}

// ctrl+c in a form is the same guarded exit as esc, and never quits (US2).
func TestGuardCtrlCOnForms(t *testing.T) {
	noKeyGrace(t)
	for _, c := range formCases() {
		t.Run(c.name, func(t *testing.T) {
			m, _ := c.open(t)
			m, cmd := update(m, ctrlC)
			if m.unsaved.open || onGuard(m, c.g) || quits(cmd) {
				t.Fatalf("ctrl+c on an unchanged form: dialog=%v open=%v quit=%v", m.unsaved.open, onGuard(m, c.g), quits(cmd))
			}
			m, saved := c.open(t)
			from := m.screen
			m = c.edit(m)
			m, cmd = update(m, ctrlC)
			if !m.unsaved.open || quits(cmd) {
				t.Fatal("ctrl+c on a changed form should open the dialog, not quit")
			}
			m, _ = update(m, press("d"))
			want := from
			if c.g == gConfig {
				want = screenList // the editor's esc goes back to the list
			}
			if onGuard(m, c.g) || saved(m) || m.screen != want {
				t.Fatalf("Discard after ctrl+c: open=%v screen=%v, want %v", onGuard(m, c.g), m.screen, want)
			}
		})
	}
}

// The kit overview is guarded by the kit's fingerprint: an applied but unsaved
// item makes esc/q ask; d leaves the stored kit alone, s writes it.
func TestGuardKitOverview(t *testing.T) {
	noKeyGrace(t)
	open := func(t *testing.T) Model {
		m := openSection(t, &store.Kit{Name: "k"}, secInstall)
		m = pressCmd(m, "a")
		m = typeIn(m, "make deps")
		m, _ = update(m, ctrlS())
		m, _ = update(m, press("esc")) // the item list's esc: back to the overview, not an exit
		if m.unsaved.open || m.kitEditor.inSection || m.screen != screenKitEditor {
			t.Fatalf("esc on the item list should return to the overview: dialog=%v inSection=%v", m.unsaved.open, m.kitEditor.inSection)
		}
		return m
	}
	for _, k := range []string{"esc", "q"} {
		m := open(t)
		m, _ = update(m, press(k))
		if !m.unsaved.open || m.unsaved.surface != gKitOverview {
			t.Fatalf("%s on a changed overview should ask", k)
		}
	}
	m := open(t)
	m, _ = update(m, press("q"))
	m, _ = update(m, press("d"))
	if m.screen != screenKitPicker {
		t.Fatalf("Discard should return to the picker, screen %v", m.screen)
	}
	if k, err := m.kits.Get("k"); err != nil || k.Setup != nil && len(k.Setup.Install) != 0 {
		t.Errorf("Discard wrote the kit: %+v %v", k, err)
	}
	m = open(t)
	m, _ = update(m, press("q"))
	m, _ = update(m, press("s"))
	if k, err := m.kits.Get("k"); err != nil || k.Setup == nil || len(k.Setup.Install) != 1 || m.screen != screenKitPicker {
		t.Errorf("Save should write the kit and return to the picker: %+v %v screen %v", k, err, m.screen)
	}
}

func TestGuardKitOverviewCleanCases(t *testing.T) {
	noKeyGrace(t)
	// Applying an item form unchanged leaves the overview clean.
	m := openSection(t, &store.Kit{Name: "k", Setup: &store.KitSetup{Install: []store.KitInstallCommand{{Command: "make"}}}}, secInstall)
	m = pressCmd(m, "enter")
	if m.kitEditor.form == nil {
		t.Fatal("enter should edit the item")
	}
	m, _ = update(m, ctrlS())
	m, _ = update(m, press("q")) // item list → overview
	m, _ = update(m, press("q"))
	if m.unsaved.open || m.screen != screenKitPicker {
		t.Fatalf("an unchanged apply should leave the kit clean: dialog=%v screen=%v", m.unsaved.open, m.screen)
	}

	// A migrated v1 kit opened and left is not a change.
	m = editorOn(t, &store.Kit{Name: "legacy"})
	dir := m.kits.Dir("legacy")
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), []byte("schemaVersion: \"1\"\nkind: mixin\nname: legacy\ncommands:\n  install:\n    - command: echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k, err := m.kits.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	mm, _ := m.enterKitEditor(k)
	m, _ = update(mm.(Model), press("q"))
	if m.unsaved.open || m.screen != screenKitPicker {
		t.Fatalf("a migrated kit left untouched should not ask: dialog=%v", m.unsaved.open)
	}

	// A new unnamed kit with an install command is a change; Save keeps it
	// open with the name error.
	m = kitModel(t, &fakeDaemon{})
	mm, _ = m.enterKitEditor(nil)
	m = mm.(Model)
	m.kitEditor.section = secInstall
	m, _ = update(m, press("enter"))
	m = pressCmd(m, "a")
	m = typeIn(m, "make")
	m, _ = update(m, ctrlS())
	m, _ = update(m, press("q"))
	m, _ = update(m, press("q"))
	if !m.unsaved.open {
		t.Fatal("an unnamed kit with an item should ask")
	}
	m, _ = update(m, press("s"))
	if m.screen != screenKitEditor || m.kitEditor.status != "name required (Identity)" {
		t.Fatalf("Save of an unnamed kit: screen %v status %q", m.screen, m.kitEditor.status)
	}
}

// Keys a guard or the app uses elsewhere are typed into text fields (US2 #5).
func TestGuardTextKeysAreTyped(t *testing.T) {
	for _, name := range []string{"Input", "Text"} {
		var m Model
		var get func(Model) string
		if name == "Input" {
			m = pressCmd(secretsModel(t, &fakeDaemon{}), "a")
			get = func(m Model) string { return m.secretsView.vals.name }
		} else {
			m = openSection(t, &store.Kit{Name: "k"}, secEnvironment)
			get = func(m Model) string { return m.kitEditor.vals.vars }
		}
		screen := m.screen
		m = typeIn(m, "q,")
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyShiftRight})
		if got := get(m); got != "q," || m.screen != screen || m.unsaved.open {
			t.Errorf("%s: value %q screen %v dialog %v, want q, typed and nothing else", name, got, m.screen, m.unsaved.open)
		}
	}
}

// --- US3: inline inputs, launch, reasoning setup, sources ---

func TestGuardRenameAndTag(t *testing.T) {
	noKeyGrace(t)
	d := &fakeDaemon{}
	m := listModel(t, d)
	sb := d.sandboxes[0]
	open := func() Model {
		mm, _ := m.enterRename(sb, "")
		return mm.(Model)
	}
	// Whitespace around the name is not a change.
	r := open()
	r = typeIn(r, " ")
	r, _ = update(r, tea.KeyMsg{Type: tea.KeyEsc})
	if r.unsaved.open || r.screen != screenList {
		t.Fatal("a whitespace-only change should close with no dialog")
	}
	// Emptied: Save stays open with the message; Discard keeps the name.
	r = open()
	r.rename.SetValue("")
	r, _ = update(r, tea.KeyMsg{Type: tea.KeyEsc})
	r, _ = update(r, press("s"))
	if r.screen != screenRename || r.status != "name can't be empty" {
		t.Fatalf("empty Save: screen %v status %q", r.screen, r.status)
	}
	r, _ = update(r, tea.KeyMsg{Type: tea.KeyEsc})
	r, _ = update(r, press("d"))
	if r.screen != screenList || r.busy[sb.GetId()] != "" || sb.GetDisplayName() != "feature-work" {
		t.Fatalf("Discard: screen %v busy %q name %q", r.screen, r.busy[sb.GetId()], sb.GetDisplayName())
	}
	// Changed: Save renames, as enter does.
	r = open()
	r = typeIn(r, "-2")
	r, _ = update(r, tea.KeyMsg{Type: tea.KeyEsc})
	r, cmd := update(r, press("s"))
	runCmd(cmd)
	if r.screen != screenList || sb.GetDisplayName() != "feature-work-2" {
		t.Fatalf("Save: screen %v name %q", r.screen, sb.GetDisplayName())
	}

	// Tag: clearing it is a change, and Save clears it.
	sb.Tag = "wip"
	mm, _ := m.enterTag(sb, "")
	tg := mm.(Model)
	tg.tagInput.SetValue("")
	tg, _ = update(tg, tea.KeyMsg{Type: tea.KeyEsc})
	if !tg.unsaved.open {
		t.Fatal("clearing the tag should ask")
	}
	tg, cmd = update(tg, press("s"))
	runCmd(cmd)
	if tg.screen != screenList || d.lastTagID != sb.GetId() || d.lastTag != "" {
		t.Fatalf("Save should clear the tag: %q %q", d.lastTagID, d.lastTag)
	}
	// ctrl+c in the input is unchanged: nothing happens.
	mm, _ = m.enterTag(sb, "")
	tg = typeIn(mm.(Model), "x")
	tg, _ = update(tg, ctrlC)
	if tg.screen != screenTag || tg.unsaved.open {
		t.Error("ctrl+c on the tag input should do nothing")
	}
}

func TestGuardHostAndGroupAdd(t *testing.T) {
	noKeyGrace(t)
	s, _ := store.New(t.TempDir())
	m := sized(New(&fakeDaemon{}, "/work").WithHosts(client.NewManager(), s.Hosts(), "").WithGroups(s.Groups(), nil))
	m, cmd := update(m, press("h"))
	m, _ = update(m, runCmd(cmd))
	m, _ = update(m, press("a"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.hosts.adding {
		t.Fatal("esc on an empty host input should close it")
	}
	m, _ = update(m, press("a"))
	m = typeIn(m, "box")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("s"))
	if hosts, _ := s.Hosts().List(); len(hosts) != 1 || m.hosts.adding {
		t.Fatalf("Save should add the host: %d", len(hosts))
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc}) // back to the list
	m, cmd = update(m, press("g"))
	m, _ = update(m, runCmd(cmd))
	m, _ = update(m, press("a"))
	m = typeIn(m, "team")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("d"))
	if groups, _ := s.Groups().List(); len(groups) != 0 || m.groups.adding {
		t.Fatalf("Discard should add nothing: %d", len(groups))
	}
}

// launchWith opens the launch wizard over root with two folders to pick.
func launchWith(t *testing.T) (Model, *fakeDaemon) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"lib", "proj"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDaemon{}
	m := pressCmd(sized(New(d, root)), "n")
	if m.screen != screenLaunch || len(m.launch.entries) < 2 {
		t.Fatalf("launch wizard not open: %v %d entries", m.screen, len(m.launch.entries))
	}
	return m, d
}

func TestGuardLaunchWizard(t *testing.T) {
	noKeyGrace(t)
	m, _ := launchWith(t)
	m, _ = update(m, press(" "))
	m, _ = update(m, press("j"))
	m, _ = update(m, press(" "))
	// The name field's esc is a step back, not an exit.
	m, _ = update(m, press("N"))
	m = typeIn(m, "box")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.screen != screenLaunch || m.launch.naming {
		t.Fatal("esc in the name field should only leave the field")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.unsaved.open || m.unsaved.surface != gLaunch || !strings.Contains(m.View(), "This launch has choices") {
		t.Fatal("esc with two folders picked should ask")
	}
	m, _ = update(m, press("s"))
	if m.screen != screenList || len(m.launching) != 1 {
		t.Fatalf("Save should launch: screen %v launching %d", m.screen, len(m.launching))
	}
	// Nothing picked: esc leaves at once.
	m, _ = launchWith(t)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.screen != screenList {
		t.Fatal("esc on an untouched wizard should close it")
	}
}

func TestGuardReasoningSetupOrderAndPicker(t *testing.T) {
	noKeyGrace(t)
	m, _ := editedReasoningSetup(t)
	// Un-marking and re-marking a pre-filled folder moves it last: a change.
	m.launch.order = append([]string{}, m.launch.order[1], m.launch.order[0])
	m.launch.cloneMode = false
	if !m.guardDirty(gReasoning) {
		t.Fatal("a re-ordered selection is a different rebuild")
	}
	// The kit picker's esc is a step back, not an exit.
	m.launch.kitPick = true
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.screen != screenLaunch {
		t.Fatal("esc in the kit picker should only close the picker")
	}
	if h := m.renderHelp(m.launchHelp()); !strings.Contains(h, "rebuild") || strings.Contains(h, "orchestrator") {
		t.Errorf("setup help %q should offer rebuild and no N/H/O", h)
	}
}

// openSourcesOverlay opens S on the one sandbox (sources api, web).
func openSourcesOverlay(t *testing.T) (Model, *fakeDaemon) {
	t.Helper()
	d := &fakeDaemon{}
	m, _ := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	if m.screen != screenSources {
		t.Fatalf("S should open the sources overlay, screen %v", m.screen)
	}
	return m, d
}

func TestGuardSourcesOverlay(t *testing.T) {
	noKeyGrace(t)
	m, _ := openSourcesOverlay(t)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.screen != screenList {
		t.Fatal("esc with no marks should close the overlay")
	}
	m, d := openSourcesOverlay(t)
	m, _ = update(m, press(" "))
	m, _ = update(m, press("q"))
	if !m.unsaved.open || !strings.Contains(m.View(), "These marked folders") {
		t.Fatal("q with a folder marked should ask")
	}
	// Save → the removal confirm (y only); n keeps the marks and the overlay.
	m, _ = update(m, press("s"))
	if m.screen != screenConfirm || !m.confirm.yOnly || m.confirm.onAccept == nil {
		t.Fatalf("Save should open the y-only removal confirm: %v %+v", m.screen, m.confirm.yOnly)
	}
	m, _ = update(m, press("n"))
	if m.screen != screenSources || len(m.sourcesView.marked) != 1 {
		t.Fatalf("n: screen %v marks %d", m.screen, len(m.sourcesView.marked))
	}
	// enter@Save, then repeated enters, remove nothing.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	for range 4 {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if m.screen != screenConfirm || len(d.removedSources) != 0 {
		t.Fatalf("enters must not remove: screen %v removed %v", m.screen, d.removedSources)
	}
	// y removes, lands on the list (the zero-value screen) and resets the overlay.
	m, cmd := update(m, press("y"))
	runCmd(cmd)
	if m.screen != screenList || m.sourcesView.sandboxID != "" || len(d.removedSources["sb-1"]) != 1 {
		t.Fatalf("y: screen %v overlay %+v removed %v", m.screen, m.sourcesView.sandboxID, d.removedSources)
	}
	// The overlay's own removal key still accepts enter.
	m, d = openSourcesOverlay(t)
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm || m.confirm.yOnly {
		t.Fatal("d should open an ordinary confirm")
	}
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	runCmd(cmd)
	if m.screen != screenSources || len(d.removedSources["sb-1"]) != 1 {
		t.Fatalf("enter on d's confirm: screen %v removed %v", m.screen, d.removedSources)
	}
	// ctrl+c on the overlay is unchanged: nothing.
	m, _ = update(m, ctrlC)
	if m.screen != screenSources || m.unsaved.open {
		t.Error("ctrl+c on the overlay should do nothing")
	}
}

func TestGuardSourcesAddBrowser(t *testing.T) {
	noKeyGrace(t)
	m, _ := openSourcesOverlay(t)
	m = pressCmd(m, "a")
	if !m.sourcesView.browsing {
		t.Fatal("a should open the add browser")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.sourcesView.browsing || m.screen != screenSources {
		t.Fatal("esc with nothing selected should go back to the overlay")
	}
	m = pressCmd(m, "a")
	m, _ = update(m, press(" "))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.unsaved.open || m.unsaved.surface != gSourcesBrowse {
		t.Fatal("esc with a folder selected should ask")
	}
	m, _ = update(m, press("d"))
	if m.unsaved.open || m.sourcesView.browsing || m.screen != screenSources {
		t.Fatalf("Discard should return to the overlay: browsing %v screen %v", m.sourcesView.browsing, m.screen)
	}
}

// Review run-465: values a widget cleans on build are the baseline, and the
// notice about it goes with the form.
func TestGuardBaselineIsTheCleanedValue(t *testing.T) {
	noKeyGrace(t)
	m := kitFormOn(t, &store.Kit{Name: "bare", Description: "line one\nline two"}, formIdentity)
	if !strings.Contains(m.kitEditor.status, "Description: ") {
		t.Errorf("the rewrite should be announced: %q", m.kitEditor.status)
	}
	for range 5 {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.kitEditor.form != nil {
		t.Error("an untouched Identity form with a multi-line description should close at once")
	}
	if strings.Contains(m.kitEditor.status, "will be rewritten") {
		t.Errorf("the notice outlived its form: %q", m.kitEditor.status)
	}

	d := &fakeDaemon{}
	l := listModel(t, d)
	sb := d.sandboxes[0]
	sb.DisplayName, sb.Tag = "my\tbox", "a\tb"
	r, _ := l.enterRename(sb, "")
	if r, _ = update(r.(Model), tea.KeyMsg{Type: tea.KeyEsc}); r.(Model).unsaved.open {
		t.Error("rename of a name with a tab should not ask when untouched")
	}
	tg, _ := l.enterTag(sb, "")
	if tg, _ = update(tg.(Model), tea.KeyMsg{Type: tea.KeyEsc}); tg.(Model).unsaved.open {
		t.Error("tag with a tab should not ask when untouched")
	}
}

// Review run-475: a list-opened mark-free left mid-save still refreshes the
// list, and reopening it cannot send the same call while the first is out.
func TestGuardMarkFreeLeftWhileSaving(t *testing.T) {
	noKeyGrace(t)
	a := &fakeDaemon{hostID: "test-host", orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host"}}}}
	a.sandboxes = []*pb.Sandbox{{Id: "w1", DisplayName: "worker-one", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Controller: &pb.SandboxController{OrchestratorId: "o-a", Name: "alpha-bot", HostId: "test-host", Run: &pb.WorkerRun{Id: "run-1", Action: "review-pr", NeedsReview: true}}}}
	m := withSandboxes(sized(New(a, "/work")), a.sandboxes)
	m, _ = update(m, press("m"))
	m, cmd := update(m, ctrlS())
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Fatalf("esc while saving returns to the list, got %v", m.screen)
	}
	// The row still offers m, but no second dialog opens for the run being
	// closed: one would outlive it (review run-482).
	m, _ = update(m, press("m"))
	if m.screen != screenList || m.orchDetail.form != nil || !strings.Contains(m.status, "mark free of run-1 is still saving") {
		t.Errorf("a second mark-free dialog opened mid-call (screen %v, status %q)", m.screen, m.status)
	}
	m, reload := update(m, runCmd(cmd))
	if n := strings.Count(a.orchCallsJoined(), "MarkWorkerFree"); n != 1 {
		t.Errorf("%d MarkWorkerFree calls, want 1", n)
	}
	if !m.listLoading || reload == nil {
		t.Error("the late success should reload the sandbox list")
	}
	if len(m.opCalls) != 0 {
		t.Errorf("the reply should release its in-flight record: %v", m.opCalls)
	}
}

// Review run-475: the notice names a call form's argument as its title does.
func TestGuardCallFormNoticeUsesTitles(t *testing.T) {
	act := &pb.Action{Name: "triage", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "x",
		Arguments: []*pb.ActionArgument{{Name: "sandboxID", Required: true, DefaultValue: "a\tb"}}}
	m, _ := jobsWith(t, jobsActions, []*pb.Action{act}, nil)
	m, _ = update(m, press("enter"))
	if m.jobs.form == nil || !strings.HasPrefix(m.jobs.status, "sandboxID: line endings") || strings.Contains(m.jobs.status, "sandboxID *") {
		t.Errorf("the notice should name the argument as titled: %q", m.jobs.status)
	}
}

// Review run-502: a save names what it saves, so another action's reply never
// settles a reopened form's refusal.
func TestGuardSaveLabelNamesTheAction(t *testing.T) {
	m, _ := jobsWith(t, jobsActions, nil, nil)
	saveNew := func(m Model, name string) (Model, tea.Cmd) {
		m, _ = update(m, press("a"))
		v := m.jobs.formVals.(*actionFormVals)
		v.name, v.body = name, "Look"
		return update(m, ctrlS())
	}
	m, cmdX := saveNew(m, "x")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, cmdZ := saveNew(m, "z")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, again := saveNew(m, "x")
	if again != nil || !strings.Contains(m.jobs.status, "the same save action x on alpha-bot is still saving") {
		t.Fatalf("the reopened save should be refused, named: %q", m.jobs.status)
	}
	m, _ = update(m, runCmd(cmdZ))
	if !strings.Contains(m.jobs.status, "save action x on alpha-bot is still saving") {
		t.Errorf("z's reply settled x's refusal: %q", m.jobs.status)
	}
	m, _ = update(m, runCmd(cmdX))
	if !strings.Contains(m.jobs.status, "the earlier save action x on alpha-bot finished") {
		t.Errorf("x's reply should settle its refusal: %q", m.jobs.status)
	}
}

// Review run-482: a call form reopened while the same call saves stops saying
// so once that call answers.
func TestGuardReopenedFormSettlesOnReply(t *testing.T) {
	act := &pb.Action{Name: "triage", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "x"}
	m, _ := jobsWith(t, jobsActions, []*pb.Action{act}, nil)
	m, _ = update(m, press("enter"))
	m, cmd := update(m, ctrlS())
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("enter"))
	m, cmd2 := update(m, ctrlS())
	if cmd2 != nil || !strings.Contains(m.jobs.status, "still saving") {
		t.Fatalf("the same call went out twice (status %q)", m.jobs.status)
	}
	m, _ = update(m, runCmd(cmd))
	if m.jobs.form == nil || strings.Contains(m.jobs.status, "still saving") || !strings.Contains(m.jobs.status, "the earlier call triage on alpha-bot finished: queued") {
		t.Errorf("the reopened form should name the earlier call's outcome: %q", m.jobs.status)
	}
	if len(m.opCalls) != 0 {
		t.Errorf("the reply should release its in-flight record: %v", m.opCalls)
	}
}

// Review run-482: the in-flight key holds the values as sent, so a form
// differing only in surrounding whitespace is the same call.
func TestOpCallKeyTrimsLikeSend(t *testing.T) {
	a, b, empty := " x ", "x", " "
	call := func(v, w *string) *orchOpState {
		return &orchOpState{formKind: "call", formVals: &callFormVals{values: map[string]*string{"a": v, "b": w}}}
	}
	if opCallKey(call(&a, &empty)) != opCallKey(call(&b, &empty)) {
		t.Error("call values differing only in whitespace should share a key")
	}
	act := func(name string) *orchOpState {
		return &orchOpState{formKind: "action", formVals: &actionFormVals{name: name, args: "x *\n"}}
	}
	if opCallKey(act("deploy ")) != opCallKey(act(" deploy")) {
		t.Error("action names differing only in whitespace should share a key")
	}
	if opCallKey(act("deploy")) == opCallKey(act("deploy2")) {
		t.Error("different actions should not share a key")
	}
	mf := func(run string) *orchOpState {
		return &orchOpState{formKind: "mark free", formVals: &markFreeVals{sandboxID: "w1", runID: run, host: "h", orchID: "o"}}
	}
	if opCallKey(mf("run-1")) == opCallKey(mf("run-2")) {
		t.Error("a mark-free is keyed by its run")
	}
}

// Review run-491: a dialog left open while its run was closed elsewhere says so
// when its ctrl+s closes another run instead.
func TestMarkFreeFlagsAnotherRunClosed(t *testing.T) {
	a := &fakeDaemon{hostID: "test-host", orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host"}}}}
	a.sandboxes = []*pb.Sandbox{{Id: "w1", DisplayName: "worker-one", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Controller: &pb.SandboxController{OrchestratorId: "o-a", Name: "alpha-bot", HostId: "test-host", Run: &pb.WorkerRun{Id: "run-1", Action: "review-pr", NeedsReview: true}}}}
	m := withSandboxes(sized(New(a, "/work")), a.sandboxes)
	m, _ = update(m, press("m"))
	m, cmd := update(m, ctrlS())
	m, _ = update(m, runCmd(cmd))
	if strings.HasPrefix(m.status, "error") || !strings.Contains(m.status, "closed run-1") {
		t.Fatalf("the dialog's own run closing is a plain success: %q", m.status)
	}
	a.freedRunID = "run-2" // run-1 was closed elsewhere; run-2 now awaits review
	m, _ = update(m, press("m"))
	m, cmd = update(m, ctrlS())
	m, _ = update(m, runCmd(cmd))
	if !strings.HasPrefix(m.status, "error: mark free closed run-2 as completed, not run-1") {
		t.Errorf("closing another run should be flagged: %q", m.status)
	}
	if got := markFreeMismatch("run-1", &pb.Run{}); !strings.Contains(got, "run-1 was no longer awaiting review; the worker was marked free") {
		t.Errorf("closing no run should be flagged too: %q", got)
	}
}

// Review run-491: the reopened form's notice settles even when the form was
// edited after the refusal.
func TestGuardReopenedFormSettlesAfterEdit(t *testing.T) {
	act := &pb.Action{Name: "triage", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "x",
		Arguments: []*pb.ActionArgument{{Name: "who"}}}
	m, _ := jobsWith(t, jobsActions, []*pb.Action{act}, nil)
	m, _ = update(m, press("enter"))
	m, cmd := update(m, ctrlS())
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("enter"))
	m, _ = update(m, ctrlS())
	*m.jobs.formVals.(*callFormVals).values["who"] = "someone" // edited after the refusal
	m, _ = update(m, runCmd(cmd))
	if !strings.Contains(m.jobs.status, "the earlier call triage on alpha-bot finished") {
		t.Errorf("the notice should name the earlier call's outcome: %q", m.jobs.status)
	}
}

// The event form's snapshot holds its days slice: comparing must not panic, and
// an empty selection reads as none.
func TestEventSnapshotCompares(t *testing.T) {
	base := snapshotOpVals(&eventFormVals{name: "e", days: []int{}})
	if !opValsEqual(base, snapshotOpVals(&eventFormVals{name: "e"})) {
		t.Error("no days and an empty selection should compare equal")
	}
	v := &eventFormVals{name: "e", days: []int{1, 3}}
	snap := snapshotOpVals(v)
	v.days[0] = 2 // huh writing in place must not reach the snapshot
	if opValsEqual(snap, snapshotOpVals(v)) {
		t.Error("a changed day should make the form dirty")
	}
	// Days are a set (review run-502): huh rewrites them Monday first, a loaded
	// window lists Sunday first, and neither reads as an edit or a new call.
	sunMon, monSun := &eventFormVals{name: "e", days: []int{0, 1}}, &eventFormVals{name: "e", days: []int{1, 0}}
	if !opValsEqual(snapshotOpVals(sunMon), snapshotOpVals(monSun)) {
		t.Error("the same days in another order should compare equal")
	}
	if !reflect.DeepEqual(sentOpVals(sunMon), sentOpVals(monSun)) {
		t.Error("the same days in another order should send the same call")
	}
	if sunMon.days[0] != 0 || monSun.days[0] != 1 {
		t.Error("normalising must not reorder the form's own days")
	}
}

// Review run-510: event names need not be unique, so an edited event's save is
// labelled with its id and another same-named event's reply never settles it.
func TestGuardSaveLabelNamesTheEventID(t *testing.T) {
	evs := []*pb.TimedEvent{
		{Id: "tev-1", Name: "poll", Action: "triage", IntervalSeconds: 900, Enabled: true},
		{Id: "tev-2", Name: "poll", Action: "triage", IntervalSeconds: 1800, Enabled: true},
	}
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host",
		Actions: []*pb.Action{{Name: "triage", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "x"}}, TimedEvents: evs}}}}
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsEvents
	edit := func(m Model, down int) (Model, tea.Cmd) {
		for i := 0; i < down; i++ {
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
		}
		m, _ = update(m, press("e"))
		return update(m, ctrlS())
	}
	m, cmd1 := edit(m, 0)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, cmd2 := edit(m, 1)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyUp})
	m, again := edit(m, 0)
	if again != nil || !strings.Contains(m.jobs.status, "the same save event poll (tev-1) on alpha-bot is still saving") {
		t.Fatalf("the reopened save should be refused, named with its id: %q", m.jobs.status)
	}
	m, _ = update(m, runCmd(cmd2))
	if !strings.Contains(m.jobs.status, "save event poll (tev-1) on alpha-bot is still saving") {
		t.Errorf("tev-2's reply settled tev-1's refusal: %q", m.jobs.status)
	}
	m, _ = update(m, runCmd(cmd1))
	if !strings.Contains(m.jobs.status, "the earlier save event poll (tev-1) on alpha-bot finished") {
		t.Errorf("tev-1's reply should settle its refusal: %q", m.jobs.status)
	}
}

// Review run-517: new events have no id, so two same-named (or unnamed) creates
// share a label; the refusal is settled by the refused-for call's token, so only
// that call's reply settles it.
func TestGuardRefusalSettlesByCallNotLabel(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host",
		Actions: []*pb.Action{{Name: "triage", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "x"}}}}}}
	for _, name := range []string{"poll", ""} {
		m := openJobs(sized(New(a, "/work")))
		m.jobs.tab = jobsEvents
		saveNew := func(m Model, minutes string) (Model, tea.Cmd) {
			m, _ = update(m, press("a"))
			v := m.jobs.formVals.(*eventFormVals)
			v.name, v.intervalMinutes = name, minutes
			return update(m, ctrlS())
		}
		m, cmd15 := saveNew(m, "15")
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
		m, cmd30 := saveNew(m, "30")
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
		m, again := saveNew(m, "15")
		if again != nil || !strings.Contains(m.jobs.status, "is still saving") {
			t.Fatalf("%q: the reopened save should be refused: %q", name, m.jobs.status)
		}
		m, _ = update(m, runCmd(cmd30))
		if !strings.Contains(m.jobs.status, "is still saving") {
			t.Errorf("%q: the 30m create's reply settled the 15m refusal: %q", name, m.jobs.status)
		}
		m, _ = update(m, runCmd(cmd15))
		if !strings.Contains(m.jobs.status, "finished") {
			t.Errorf("%q: the 15m create's reply should settle its refusal: %q", name, m.jobs.status)
		}
	}
	unnamed := &orchOpState{formKind: "event", formVals: &eventFormVals{id: "tev-1"}, formTarget: orchTarget{name: "alpha-bot"}}
	if got := opCallLabel(unnamed); got != "save event (tev-1) on alpha-bot" {
		t.Errorf("an unnamed edited event's label: %q", got)
	}
}
