package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func (f *fakeDaemon) SecretsAvailable() (bool, string) {
	if f.secretsReason != "" {
		return false, f.secretsReason
	}
	return true, ""
}

func (f *fakeDaemon) ListSecrets(context.Context) ([]*pb.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secretsErr != nil {
		return nil, f.secretsErr
	}
	out := make([]*pb.Secret, len(f.secrets))
	copy(out, f.secrets)
	return out, nil
}

func (f *fakeDaemon) SetSecret(_ context.Context, req *pb.SetSecretRequest) (*pb.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secretsErr != nil {
		return nil, f.secretsErr
	}
	f.setSecrets = append(f.setSecrets, req)
	scope := "global"
	if req.GetSandbox() != "" {
		scope = req.GetSandbox()
	}
	s := &pb.Secret{Scope: scope, Type: "service", Name: req.GetName(), MaskedValue: "****"}
	f.secrets = append(f.secrets, s)
	return s, nil
}

func (f *fakeDaemon) RemoveSecret(_ context.Context, req *pb.RemoveSecretRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secretsErr != nil {
		return f.secretsErr
	}
	f.removedSecrets = append(f.removedSecrets, req)
	kept := f.secrets[:0]
	for _, s := range f.secrets {
		if s.GetName() != req.GetName() {
			kept = append(kept, s)
		}
	}
	f.secrets = kept
	return nil
}

func githubSecret() *pb.Secret {
	return &pb.Secret{Scope: "global", Type: "service", Name: "github", MaskedValue: "gho_GCaw****43qy"}
}

// secretsModel opens the Secrets option of the (single, local) daemon and
// drains the initial load.
func secretsModel(t *testing.T, d *fakeDaemon) Model {
	t.Helper()
	d.baselineMet = true
	m := sized(New(d, "/work"))
	m, _ = update(m, shiftRight())
	m, _ = update(m, press("enter")) // options
	m, _ = update(m, press("j"))     // Secrets is the second option
	m, cmd := update(m, press("enter"))
	if m.screen != screenSecrets {
		t.Fatalf("expected the secrets view, got %v", m.screen)
	}
	if cmd != nil {
		if msg := runCmd(cmd); msg != nil {
			m, _ = update(m, msg)
		}
	}
	return m
}

// drainSecrets runs a command chain (done → reload) to its end.
func drainSecrets(m Model, cmd tea.Cmd) Model {
	for i := 0; i < 5 && cmd != nil; i++ {
		msg := runCmd(cmd)
		if msg == nil {
			break
		}
		m, cmd = update(m, msg)
	}
	return m
}

func TestSecretsOptionListsHostSecrets(t *testing.T) {
	d := &fakeDaemon{secrets: []*pb.Secret{githubSecret(), {Scope: "my-sb", Type: "service", Name: "anthropic", MaskedValue: "sk-****"}}}
	m := secretsModel(t, d)
	v := m.View()
	for _, want := range []string{"Secrets ·", "github", "gho_GCaw****43qy", "anthropic", "my-sb", "mcp:<server>:<placeholder>"} {
		if !strings.Contains(v, want) {
			t.Errorf("view should show %q; got:\n%s", want, v)
		}
	}
	if strings.Contains(v, "MCP gateway ·") {
		t.Error("the second option must open Secrets, not the gateway")
	}
	m, _ = update(m, press("j"))
	if m.secretsView.cursor != 1 {
		t.Errorf("cursor = %d", m.secretsView.cursor)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenDaemons {
		t.Error("esc should return to the daemon screen")
	}
}

func TestSecretsStoreFormSendsValueOnce(t *testing.T) {
	d := &fakeDaemon{}
	m := secretsModel(t, d)
	m = pressCmd(m, "a")
	if m.secretsView.form == nil {
		t.Fatal("a should open the store form")
	}
	if !strings.Contains(m.View(), "Typed masked") {
		t.Error("the form should explain that the value is masked")
	}
	m.secretsView.vals.name = " mcp:github:token "
	m.secretsView.vals.value = "gho_plain"
	m, cmd := update(m, ctrlS())
	if m.secretsView.form != nil || m.secretsView.vals != nil {
		t.Error("the form (and the typed value) must be dropped once the request is built")
	}
	m = drainSecrets(m, cmd)
	if len(d.setSecrets) != 1 || d.setSecrets[0].GetName() != "mcp:github:token" || d.setSecrets[0].GetValue() != "gho_plain" || d.setSecrets[0].GetSandbox() != "" {
		t.Fatalf("set = %+v", d.setSecrets)
	}
	v := m.View()
	if !strings.Contains(v, "stored mcp:github:token globally") || !strings.Contains(v, "mcp:github:token") || strings.Contains(v, "gho_plain") {
		t.Errorf("after store the list should show the new row and never the value; got:\n%s", v)
	}
	// Sandbox scope.
	m = pressCmd(m, "a")
	m.secretsView.vals.name = "openai"
	m.secretsView.vals.value = "sk-1"
	m.secretsView.vals.sandbox = "my-sb"
	m, cmd = update(m, ctrlS())
	m = drainSecrets(m, cmd)
	if len(d.setSecrets) != 2 || d.setSecrets[1].GetSandbox() != "my-sb" || !strings.Contains(m.secretsView.status, "for my-sb") {
		t.Errorf("sandbox-scoped set = %+v status %q", d.setSecrets, m.secretsView.status)
	}
	// Validation keeps the form open.
	m = pressCmd(m, "a")
	m.secretsView.vals.name = "bad name"
	m.secretsView.vals.value = "x"
	m, _ = update(m, ctrlS())
	if m.secretsView.form == nil || !strings.Contains(m.secretsView.status, "without spaces") {
		t.Errorf("bad name should keep the form open; status %q", m.secretsView.status)
	}
	m.secretsView.vals.name = "ok"
	m.secretsView.vals.value = "  "
	m, _ = update(m, ctrlS())
	if m.secretsView.form == nil || !strings.Contains(m.secretsView.status, "value is required") {
		t.Errorf("empty value should keep the form open; status %q", m.secretsView.status)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.secretsView.form != nil {
		t.Error("esc should abort the form")
	}
}

func TestSecretsRemoveBehindConfirm(t *testing.T) {
	d := &fakeDaemon{secrets: []*pb.Secret{githubSecret(), {Scope: "my-sb", Type: "service", Name: "anthropic", MaskedValue: "sk-****"}, {Scope: "global", Type: "custom", Name: "api.example.com", MaskedValue: "****"}}}
	m := secretsModel(t, d)
	m, _ = update(m, press("j")) // anthropic (sandbox-scoped)
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "for sandbox my-sb") {
		t.Fatalf("d should open the confirm naming the scope; screen %v view:\n%s", m.screen, m.View())
	}
	m, cmd := update(m, press("y"))
	m = drainSecrets(m, cmd)
	if len(d.removedSecrets) != 1 || d.removedSecrets[0].GetName() != "anthropic" || d.removedSecrets[0].GetSandbox() != "my-sb" || d.removedSecrets[0].GetType() != "service" {
		t.Fatalf("removed = %+v", d.removedSecrets)
	}
	names := make([]string, 0, len(m.secretsView.secrets))
	for _, s := range m.secretsView.secrets {
		names = append(names, s.GetName())
	}
	if m.screen != screenSecrets || !strings.Contains(m.secretsView.status, "removed anthropic") || strings.Join(names, ",") != "github,api.example.com" {
		t.Errorf("after removal the view should return and drop the row; status %q rows %v", m.secretsView.status, names)
	}
	// Custom secrets are refused with a hint, no confirm.
	m, _ = update(m, press("j"))
	m, _ = update(m, press("d"))
	if m.screen != screenSecrets || !strings.Contains(m.secretsView.status, "managed on the host") {
		t.Errorf("custom removal: screen %v status %q", m.screen, m.secretsView.status)
	}
}

func TestSecretsUnavailableAndErrors(t *testing.T) {
	d := &fakeDaemon{secretsReason: "sbx 0.30.0 is below the required 0.36.0"}
	m := secretsModel(t, d)
	if !strings.Contains(m.View(), "below the required") {
		t.Errorf("unavailable reason missing; got:\n%s", m.View())
	}
	m, cmd := update(m, press("a"))
	if m.secretsView.form != nil || cmd != nil {
		t.Error("a must be inert while unavailable")
	}
	m, cmd = update(m, press("r"))
	if cmd != nil {
		t.Error("r while still unavailable must not list")
	}
	d.secretsReason = ""
	m, cmd = update(m, press("r"))
	if cmd == nil || m.secretsView.unavailable != "" {
		t.Error("r after the host recovers should reload")
	}

	d2 := &fakeDaemon{secretsErr: errors.New("keychain locked")}
	m2 := secretsModel(t, d2)
	if !strings.Contains(m2.View(), "could not list secrets: keychain locked") {
		t.Errorf("list error should show; got:\n%s", m2.View())
	}
	m2 = pressCmd(m2, "a")
	m2.secretsView.vals.name = "github"
	m2.secretsView.vals.value = "x"
	m2, cmd = update(m2, ctrlS())
	m2 = drainSecrets(m2, cmd)
	if !strings.Contains(m2.secretsView.status, "store github failed: keychain locked") {
		t.Errorf("store failure should show; status %q", m2.secretsView.status)
	}
	// A change event for this host reloads the view; one for another host, or
	// while a form is open, does not.
	d2.mu.Lock()
	d2.secretsErr = nil
	d2.secrets = []*pb.Secret{githubSecret()}
	d2.mu.Unlock()
	mm, cmd := m2.syncSecretsView(m2.secretsView.host)
	if cmd == nil {
		t.Fatal("a change on this host should reload")
	}
	m2, _ = update(mm.(Model), runCmd(cmd))
	if len(m2.secretsView.secrets) != 1 {
		t.Errorf("reload should land the list: %v", m2.secretsView.secrets)
	}
	if _, cmd := m2.syncSecretsView("other-host"); cmd != nil {
		t.Error("another host's change must not reload")
	}
	m2 = pressCmd(m2, "a")
	if _, cmd := m2.syncSecretsView(m2.secretsView.host); cmd != nil {
		t.Error("a change while the form is open must not reload")
	}
}
