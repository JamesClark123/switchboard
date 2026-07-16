package ui

import (
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func refreshableSandbox() *pb.Sandbox {
	return &pb.Sandbox{
		Id:          "sb-1",
		DisplayName: "feature-work",
		State:       pb.SandboxState_SANDBOX_STATE_RUNNING,
		Sources:     []*pb.SourceRef{{Path: "/home/u/code/api"}, {Path: "/home/u/code/web"}},
	}
}

// listModel returns a sized model on the sandbox list with one refreshable row.
func listModel(t *testing.T, d *fakeDaemon) Model {
	t.Helper()
	d.sandboxes = []*pb.Sandbox{refreshableSandbox()}
	m := sized(New(d, "test"))
	return withSandboxes(m, d.sandboxes)
}

// "F" must open the gate, not act. A destructive action firing on a single
// keypress is the bug this dialog exists to prevent.
func TestRefreshKeyOpensConfirmation(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)

	out, _ := update(m, press("F"))
	got := out
	if got.screen != screenConfirm {
		t.Fatalf("screen = %v, want screenConfirm", got.screen)
	}
	if d.refreshedID != "" {
		t.Error("refresh fired before the user confirmed")
	}
}

// The dialog must state the consequence and name the repos being re-copied.
func TestConfirmationNamesConsequenceAndRepos(t *testing.T) {
	m := listModel(t, &fakeDaemon{})
	out, _ := update(m, press("F"))
	view := out.View()

	for _, want := range []string{"Refresh sandbox?", "feature-work", "api", "web", "uncommitted", "cannot be undone"} {
		if !strings.Contains(view, want) {
			t.Errorf("confirmation should mention %q; got:\n%s", want, view)
		}
	}
}

// Cancelling must return to the list without acting.
func TestConfirmationCancelDoesNotRefresh(t *testing.T) {
	for _, key := range []string{"n", "esc", "q"} {
		d := &fakeDaemon{}
		m := listModel(t, d)
		out, _ := update(m, press("F"))
		out, cmd := update(out, press(key))
		if got := out.screen; got != screenList {
			t.Errorf("%q: screen = %v, want screenList", key, got)
		}
		if cmd != nil {
			runCmd(cmd)
		}
		if d.refreshedID != "" {
			t.Errorf("%q cancelled the dialog but the refresh still ran", key)
		}
	}
}

// An unrecognised key must not be read as consent.
func TestConfirmationIgnoresOtherKeys(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	out, _ := update(m, press("F"))
	out, cmd := update(out, press("x"))
	if got := out.screen; got != screenConfirm {
		t.Errorf("stray key changed screen to %v; the dialog should stay open", got)
	}
	if cmd != nil {
		runCmd(cmd)
	}
	if d.refreshedID != "" {
		t.Error("a stray key triggered the refresh")
	}
}

func TestConfirmAcceptRunsRefresh(t *testing.T) {
	for _, key := range []string{"y", "enter"} {
		d := &fakeDaemon{}
		m := listModel(t, d)
		out, _ := update(m, press("F"))
		out, cmd := update(out, press(key))
		got := out
		if got.screen != screenList {
			t.Errorf("%q: screen = %v, want screenList", key, got.screen)
		}
		// The row shows a spinner while the refresh is in flight.
		if got.busy["sb-1"] != "refresh" {
			t.Errorf("%q: busy = %v, want the row marked refreshing", key, got.busy)
		}
		if cmd == nil {
			t.Fatalf("%q: expected a refresh command", key)
		}
		msg := runCmd(cmd)
		if d.refreshedID != "sb-1" {
			t.Errorf("%q: refreshed %q, want sb-1", key, d.refreshedID)
		}
		if _, ok := msg.(statusMsg); !ok {
			t.Errorf("%q: msg = %T, want statusMsg", key, msg)
		}
	}
}

// A daemon error must surface rather than being swallowed into a success status.
func TestRefreshErrorSurfaces(t *testing.T) {
	d := &fakeDaemon{refreshErr: errBoom{}}
	m := listModel(t, d)
	out, _ := update(m, press("F"))
	_, cmd := update(out, press("y"))
	msg := runCmd(cmd)
	if _, ok := msg.(errMsg); !ok {
		t.Fatalf("msg = %T, want errMsg", msg)
	}
}

// A sandbox with no recorded sources cannot be re-seeded; the dialog must not open.
func TestRefreshWithoutSourcesIsRefused(t *testing.T) {
	d := &fakeDaemon{}
	sb := refreshableSandbox()
	sb.Sources = nil
	d.sandboxes = []*pb.Sandbox{sb}
	m := withSandboxes(sized(New(d, "test")), d.sandboxes)

	out, _ := update(m, press("F"))
	got := out
	if got.screen != screenList {
		t.Errorf("screen = %v, want the list (no dialog)", got.screen)
	}
	if !strings.Contains(got.status, "no sources") {
		t.Errorf("status = %q, want it to explain why", got.status)
	}
}

// "r" reloads the list and must never be confused with the destructive "F".
func TestReloadKeyIsNotRefreshSandbox(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	out, cmd := update(m, press("r"))
	if got := out.screen; got != screenList {
		t.Errorf("'r' opened %v; it should just reload the list", got)
	}
	if cmd != nil {
		runCmd(cmd)
	}
	if d.refreshedID != "" {
		t.Error("'r' triggered a destructive sandbox refresh")
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
