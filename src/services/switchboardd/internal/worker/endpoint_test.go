package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// linkStub plays the orchestrator's end of a link: it answers every relayed
// call through answer and records the call frames.
type linkStub struct {
	c     *pipeStream
	mu    sync.Mutex
	calls []*pb.OrchestratorLinkFrame_Call
}

func newLinkStub(t *testing.T, svc *Service, orchID, name string, answer func(call *pb.OrchestratorLinkFrame_Call) (body, errText string)) *linkStub {
	t.Helper()
	c, s := newPipe(context.Background())
	go func() { _ = svc.AcceptLink(s) }()
	if err := c.Send(hello(orchID, name)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !svc.links.Linked(orchID) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !svc.links.Linked(orchID) {
		t.Fatal("link never indexed")
	}
	st := &linkStub{c: c}
	go func() {
		for {
			fr, err := c.Recv()
			if err != nil {
				return
			}
			call := fr.GetCall()
			if call == nil {
				continue
			}
			st.mu.Lock()
			st.calls = append(st.calls, call)
			st.mu.Unlock()
			body, errText := answer(call)
			_ = c.Send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Result_{Result: &pb.OrchestratorLinkFrame_Result{
				Id: call.GetId(), Ok: errText == "", BodyJson: body, Error: errText,
			}}})
		}
	}()
	return st
}

func (st *linkStub) recorded() []*pb.OrchestratorLinkFrame_Call {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]*pb.OrchestratorLinkFrame_Call(nil), st.calls...)
}

// structured decodes a tool result's structured content into v.
func structured(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestEndpointServesTheFourToolsWithAttribution(t *testing.T) {
	e := newEnv(t, "")
	e.sandbox(t, "sb-1", "proj", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: "sb-1", Controller: controller("o1", "review-bot"), Briefing: "b"}); err != nil {
		t.Fatal(err)
	}
	tok := e.svc.tokenOf("sb-1")
	if tok == "" {
		t.Fatal("attach should have minted a token")
	}
	stub := newLinkStub(t, e.svc, "o1", "review-bot", func(call *pb.OrchestratorLinkFrame_Call) (string, string) {
		switch call.GetTool() {
		case ToolListActions:
			return `{"orchestrator":"review-bot","actions":[{"name":"review-pr","description":"Review a pull request","type":"prompt","worker_tag":"","arguments":[{"name":"number","description":"PR number","required":true,"default":""}]}]}`, ""
		case ToolCallAction:
			if strings.Contains(call.GetArgsJson(), `"nope"`) {
				return "", "unknown action nope"
			}
			return `{"run_id":"run-1","position":2,"state":"queued"}`, ""
		case ToolCheckRun:
			return `{"run_id":"run-1","action":"review-pr","state":"completed","reason":"","summary":"done","closed_by_developer":false}`, ""
		case ToolReportCompletion:
			return `{"ok":true,"run_id":"run-1","state":"completed"}`, ""
		}
		return "", "unknown tool " + call.GetTool()
	})

	mux := http.NewServeMux()
	mux.Handle(EndpointPrefix, e.svc.Handler())
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	url := hs.URL + EndpointPrefix + tok + "/mcp"

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(bg, &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	init := sess.InitializeResult()
	if init.ServerInfo.Name != EndpointServerName || init.ServerInfo.Version != "test" {
		t.Errorf("server info = %+v", init.ServerInfo)
	}
	if !strings.Contains(init.Instructions, `orchestrator "review-bot"`) || !strings.Contains(init.Instructions, "a worker") || !strings.Contains(init.Instructions, ToolReportCompletion) {
		t.Errorf("instructions = %q", init.Instructions)
	}

	tools, err := sess.ListTools(bg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if tl.Description == "" {
			t.Errorf("tool %s has no description", tl.Name)
		}
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "orchestrator_call_action,orchestrator_check_run,orchestrator_list_actions,orchestrator_report_completion" {
		t.Errorf("tools = %s", got)
	}

	// list_actions: structured output decoded from the relayed body.
	res, err := sess.CallTool(bg, &mcp.CallToolParams{Name: ToolListActions, Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_actions = %+v / %v", res, err)
	}
	var la ListActionsOut
	structured(t, res, &la)
	if la.Orchestrator != "review-bot" || len(la.Actions) != 1 || la.Actions[0].Name != "review-pr" || !la.Actions[0].Arguments[0].Required {
		t.Errorf("list_actions = %+v", la)
	}
	// call_action: the arguments are relayed verbatim; refusals are tool errors.
	res, err = sess.CallTool(bg, &mcp.CallToolParams{Name: ToolCallAction, Arguments: map[string]any{"action": "review-pr", "arguments": map[string]any{"number": "412"}}})
	if err != nil || res.IsError {
		t.Fatalf("call_action = %+v / %v", res, err)
	}
	var ca CallActionOut
	structured(t, res, &ca)
	if ca.RunID != "run-1" || ca.Position != 2 || ca.State != "queued" {
		t.Errorf("call_action = %+v", ca)
	}
	res, err = sess.CallTool(bg, &mcp.CallToolParams{Name: ToolCallAction, Arguments: map[string]any{"action": "nope"}})
	if err != nil || !res.IsError || !strings.Contains(resultText(res), "unknown action nope") {
		t.Errorf("refused call_action = %+v / %v", res, err)
	}
	res, err = sess.CallTool(bg, &mcp.CallToolParams{Name: ToolCheckRun, Arguments: map[string]any{"run_id": "run-1"}})
	if err != nil || res.IsError {
		t.Fatalf("check_run = %+v / %v", res, err)
	}
	var cr CheckRunOut
	structured(t, res, &cr)
	if cr.State != "completed" || cr.Summary != "done" {
		t.Errorf("check_run = %+v", cr)
	}
	res, err = sess.CallTool(bg, &mcp.CallToolParams{Name: ToolReportCompletion, Arguments: map[string]any{"summary": "Left 3 comments"}})
	if err != nil || res.IsError {
		t.Fatalf("report = %+v / %v", res, err)
	}
	var ro ReportOut
	structured(t, res, &ro)
	if !ro.OK || ro.RunID != "run-1" {
		t.Errorf("report = %+v", ro)
	}
	// A missing required input is refused before anything is relayed.
	res, err = sess.CallTool(bg, &mcp.CallToolParams{Name: ToolCheckRun, Arguments: map[string]any{}})
	if err == nil && !res.IsError {
		t.Errorf("check_run without run_id should be refused: %+v", res)
	}

	// Attribution: every relayed frame named the token's sandbox and carried
	// the arguments; the handler never had to know.
	calls := stub.recorded()
	if len(calls) != 5 {
		t.Fatalf("relayed calls = %d, want 5", len(calls))
	}
	for _, c := range calls {
		if c.GetSandboxId() != "sb-1" {
			t.Errorf("call %s attributed to %q, want sb-1", c.GetTool(), c.GetSandboxId())
		}
	}
	if a := calls[1].GetArgsJson(); !strings.Contains(a, `"action":"review-pr"`) || !strings.Contains(a, `"number":"412"`) {
		t.Errorf("call_action args = %s", a)
	}
	if a := calls[4].GetArgsJson(); a != `{"summary":"Left 3 comments"}` {
		t.Errorf("report args = %s", a)
	}

	// Unknown token ⇒ 404; GET ⇒ 405 (stateless); a malformed path ⇒ 404.
	for _, tc := range []struct {
		method, url string
		want        int
	}{
		{http.MethodPost, hs.URL + EndpointPrefix + "deadbeef/mcp", http.StatusNotFound},
		{http.MethodPost, hs.URL + EndpointPrefix + tok + "/other", http.StatusNotFound},
		{http.MethodPost, hs.URL + EndpointPrefix + tok, http.StatusNotFound},
		{http.MethodGet, url, http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(tc.method, tc.url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, strings.TrimPrefix(tc.url, hs.URL), resp.StatusCode, tc.want)
		}
	}

	// Link down ⇒ the documented unreachable text as a tool error.
	_ = stub.c.CloseSend()
	deadline := time.Now().Add(2 * time.Second)
	for e.svc.links.Linked("o1") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	res, err = sess.CallTool(bg, &mcp.CallToolParams{Name: ToolListActions, Arguments: map[string]any{}})
	if err != nil || !res.IsError || !strings.Contains(resultText(res), `orchestrator "review-bot" is unreachable right now; retry in a moment — a failed report does not end the run`) {
		t.Errorf("link down = %+v / %v", res, err)
	}

	// A rename rebuilds the cached server so the handshake names the new name.
	nn := "triage-bot"
	if _, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: "sb-1", OrchestratorId: "o1", Name: &nn}); err != nil {
		t.Fatal(err)
	}
	sess2, err := client.Connect(bg, &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sess2.InitializeResult().Instructions, `orchestrator "triage-bot"`) {
		t.Errorf("instructions after rename = %q", sess2.InitializeResult().Instructions)
	}
	_ = sess2.Close()

	// Detach revokes the token: 404, and the cached server is dropped.
	if _, err := e.svc.Detach(bg, "sb-1", "o1", false); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("after detach = %d, want 404", resp.StatusCode)
	}
	e.svc.srvMu.Lock()
	n := len(e.svc.servers)
	e.svc.srvMu.Unlock()
	if n != 0 {
		t.Errorf("cached servers after detach = %d", n)
	}
}

func TestEndpointInstructionsAndPathParsing(t *testing.T) {
	if s := Instructions("review-bot", true); !strings.Contains(s, "the reasoning sandbox") || !strings.Contains(s, `"review-bot"`) {
		t.Errorf("reasoning instructions = %q", s)
	}
	for p, want := range map[string]string{
		"/orchestrator/w/abc/mcp": "abc", "/orchestrator/w//mcp": "", "/orchestrator/w/abc": "", "/orchestrator/w/abc/mcp/x": "", "/hook": "",
	} {
		got, ok := tokenFromPath(p)
		if got != want || ok != (want != "") {
			t.Errorf("tokenFromPath(%q) = %q, %v", p, got, ok)
		}
	}
	// The relay refuses a sandbox that lost its controller before reaching the link.
	e := newEnv(t, "")
	e.sandbox(t, "sb-2", "loose", pb.SandboxState_SANDBOX_STATE_RUNNING)
	var out ListActionsOut
	if err := e.svc.relay(bg, "sb-2", ToolListActions, nil, &out); err == nil || !strings.Contains(err.Error(), "no longer controlled") {
		t.Errorf("relay without controller = %v", err)
	}
}
