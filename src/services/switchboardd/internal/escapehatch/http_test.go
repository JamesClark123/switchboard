package escapehatch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// serviceWith builds a Service over a single sandbox with the given allowlist and a
// prompt capture channel.
func serviceWith(t *testing.T, ws string, commands ...*pb.EscapeHatchCommand) (*Service, chan string) {
	t.Helper()
	sb := &pb.Sandbox{Id: "sb1", WorkspacePath: ws, EscapeHatchCommands: commands, Agent: &pb.AgentSession{Spec: &pb.AgentSpec{Kind: "claude"}}}
	prompts := make(chan string, 4)
	svc := New(&fakeSandboxes{sb: sb}, &fakeEmitter{}, func(id string, spec *pb.AgentSpec, text string) error {
		prompts <- text
		return nil
	})
	svc.SetAgentAuth(func(sandboxID, gen, token string) bool {
		return sandboxID == "sb1" && gen == "1" && token == "tok-1"
	})
	return svc, prompts
}

func postRun(t *testing.T, svc *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postRunAs(t, svc, body, "1", "tok-1")
}

// postRunAs posts a run request with the given agent generation and token
// headers (none when token is empty).
func postRunAs(t *testing.T, svc *Service, body, gen, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/escape-hatch/run", strings.NewReader(body))
	if token != "" {
		req.Header.Set(HeaderToken, token)
		req.Header.Set(HeaderGen, gen)
	}
	rec := httptest.NewRecorder()
	svc.HandleRun(rec, req)
	return rec
}

// Only the agent's own process tree runs a command (SWI-17, research R13).
func TestHandleRunNeedsALiveAgentToken(t *testing.T) {
	svc, _ := serviceWith(t, t.TempDir(), shCmd("x", "true"))
	body := `{"sandbox_id":"sb1","name":"x"}`
	rec := postRunAs(t, svc, body, "", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no agent token in this environment; run the escape hatch from the agent's session") {
		t.Errorf("tokenless = %d %q", rec.Code, rec.Body.String())
	}
	for _, c := range [][2]string{{"1", "wrong"}, {"2", "tok-1"}} {
		if rec := postRunAs(t, svc, body, c[0], c[1]); rec.Code != http.StatusForbidden {
			t.Errorf("gen %s token %s = %d, want 403", c[0], c[1], rec.Code)
		}
	}
	if rec := postRunAs(t, svc, `{"sandbox_id":"sb2","name":"x"}`, "1", "tok-1"); rec.Code != http.StatusForbidden {
		t.Errorf("another sandbox's token = %d, want 403", rec.Code)
	}
	if len(svc.ListRuns("sb1")) != 0 {
		t.Error("a refused call runs nothing")
	}
	svc.SetAgentAuth(nil)
	if rec := postRun(t, svc, body); rec.Code != http.StatusForbidden {
		t.Errorf("no token check configured = %d, want 403", rec.Code)
	}
}

func TestHandleRunUnknownNameIs404AndRunsNothing(t *testing.T) {
	svc, prompts := serviceWith(t, t.TempDir(), shCmd("install-deps", "true"))
	rec := postRun(t, svc, `{"sandbox_id":"sb1","name":"rm-rf"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 for an unknown command", rec.Code)
	}
	if len(svc.ListRuns("sb1")) != 0 {
		t.Error("an unknown command must create no run and execute nothing")
	}
	select {
	case <-prompts:
		t.Error("nothing should have been delivered to the agent")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHandleRunCannotInjectCommandString(t *testing.T) {
	// The stored command writes "SAFE"; the attacker-supplied "command" field must be
	// ignored — only the allowlisted string may run (SC-004).
	ws := t.TempDir()
	svc, _ := serviceWith(t, ws, shCmd("safe", "echo SAFE"))
	rec := postRun(t, svc, `{"sandbox_id":"sb1","name":"safe","command":"echo PWNED"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	run := waitTerminal(t, svc, "sb1")
	if !strings.Contains(run.GetOutput(), "SAFE") || strings.Contains(run.GetOutput(), "PWNED") {
		t.Errorf("only the allowlisted command may run; got output %q", run.GetOutput())
	}
}

func TestHandleRunBadMethodAndPayload(t *testing.T) {
	svc, _ := serviceWith(t, t.TempDir(), shCmd("x", "true"))

	req := httptest.NewRequest(http.MethodGet, "/escape-hatch/run", nil)
	rec := httptest.NewRecorder()
	svc.HandleRun(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be 405, got %d", rec.Code)
	}

	if rec := postRun(t, svc, `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON should be 400, got %d", rec.Code)
	}
	if rec := postRun(t, svc, `{"sandbox_id":"sb1"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing name should be 400, got %d", rec.Code)
	}
}

func TestHandleRunRespondsImmediately(t *testing.T) {
	// A slow command must not hold the HTTP response open (async, research R1).
	svc, _ := serviceWith(t, t.TempDir(), shCmd("slow", "sleep 3"))
	start := time.Now()
	rec := postRun(t, svc, `{"sandbox_id":"sb1","name":"slow"}`)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("response should be immediate, took %v", elapsed)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "running") {
		t.Errorf("want an immediate running response, got %d %q", rec.Code, rec.Body.String())
	}
}

// waitTerminal polls until the sandbox's newest run reaches a terminal state.
func waitTerminal(t *testing.T, svc *Service, sandboxID string) *pb.EscapeHatchRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs := svc.ListRuns(sandboxID)
		if len(runs) > 0 && isTerminal(runs[len(runs)-1].GetStatus()) {
			return runs[len(runs)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state in time")
	return nil
}
