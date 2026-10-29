package grpc_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolText joins a tool result's text content.
func toolText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// A tool call made with the SDK client against the remote daemon's endpoint
// is attributed to the token's sandbox, relayed over the real OrchestratorLink
// to the orchestrator on the other daemon, and answered (research R5/R6).
func TestTwoDaemonsToolCallOverTheLink(t *testing.T) {
	m, n, nDir, _ := twoDaemonsAgents(t)
	ctx := context.Background()
	src := filepath.Join(nDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	sb := launch(t, n, src)
	o, _ := m.client.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: "review-bot"})
	if _, err := m.client.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: o.GetId(), Hosts: []*pb.OrchestratorHost{{HostId: "host-n", SshTarget: "me@n"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.client.AttachWorker(ctx, &pb.AttachWorkerRequest{OrchestratorId: o.GetId(), HostId: "host-n", SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.client.PutAction(ctx, &pb.PutActionRequest{OrchestratorId: o.GetId(), Action: &pb.Action{
		Name: "review-pr", Type: pb.ActionType_ACTION_TYPE_PROMPT, Body: "Review {number} in {repo}.",
		Arguments: []*pb.ActionArgument{{Name: "number", Required: true}, {Name: "repo", Required: true}},
		// No steps (SWI-17, FR-208): this test exercises the prompt delivery.
		Sequence: &pb.ActionSequence{Model: pb.ModelChoice_MODEL_CHOICE_OPUS, Effort: pb.EffortChoice_EFFORT_CHOICE_MEDIUM},
	}, SetsLanes: true, Guard: &pb.LaneGuardConfirm{UnlanedActions: []string{"review-pr"}}}); err != nil {
		t.Fatal(err)
	}

	// N's endpoint, reached as the host gateway would, with the minted token.
	tok, err := m.nReg.GetWorkerToken(sb.GetId())
	if err != nil || tok == "" {
		t.Fatalf("token = %q / %v", tok, err)
	}
	mux := http.NewServeMux()
	mux.Handle(worker.EndpointPrefix, m.nWorker.Handler())
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: hs.URL + worker.EndpointPrefix + tok + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: worker.ToolListActions, Arguments: map[string]any{}})
	if err != nil || res.IsError || !strings.Contains(toolText(res), `"name":"review-pr"`) {
		t.Fatalf("list_actions = %+v / %v", res, err)
	}
	// call_action queues a TRIGGERED run attributed to this sandbox; with one
	// worker it is delivered back to the caller.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: worker.ToolCallAction, Arguments: map[string]any{"action": "review-pr", "arguments": map[string]any{"number": "7", "repo": "acme/web"}}})
	if err != nil || res.IsError {
		t.Fatalf("call_action = %+v / %v", res, err)
	}
	var ca worker.CallActionOut
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &ca); err != nil || ca.State != "queued" || ca.Position != 1 {
		t.Fatalf("call_action out = %s / %v", b, err)
	}
	run, err := m.client.GetRun(ctx, &pb.GetRunRequest{OrchestratorId: o.GetId(), RunId: ca.RunID})
	if err != nil || run.GetEventKind() != pb.EventKind_EVENT_KIND_TRIGGERED || run.GetOrigin() != sb.GetId() || run.GetOriginLabel() != sb.GetDisplayName() {
		t.Fatalf("run = %+v / %v", run, err)
	}
	waitUntil(t, func() bool {
		got, _ := m.client.GetRun(ctx, &pb.GetRunRequest{OrchestratorId: o.GetId(), RunId: ca.RunID})
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING && got.GetSandboxId() == sb.GetId()
	})
	// A refusal travels back verbatim as a tool error.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: worker.ToolCallAction, Arguments: map[string]any{"action": "review-pr", "arguments": map[string]any{"number": "7"}}})
	if err != nil || !res.IsError || !strings.Contains(toolText(res), "argument repo is required") {
		t.Errorf("refusal = %+v / %v", res, err)
	}
	// report_completion closes the run recorded on the calling sandbox.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: worker.ToolReportCompletion, Arguments: map[string]any{"summary": "reviewed"}})
	if err != nil || res.IsError {
		t.Fatalf("report = %+v / %v", res, err)
	}
	waitUntil(t, func() bool {
		got, _ := m.client.GetRun(ctx, &pb.GetRunRequest{OrchestratorId: o.GetId(), RunId: ca.RunID})
		return got.GetState() == pb.RunState_RUN_STATE_COMPLETED && got.GetSummary() == "reviewed"
	})
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: worker.ToolCheckRun, Arguments: map[string]any{"run_id": ca.RunID}})
	if err != nil || res.IsError || !strings.Contains(toolText(res), `"state":"completed"`) || !strings.Contains(toolText(res), `"summary":"reviewed"`) {
		t.Errorf("check_run = %+v / %v", res, err)
	}
	// Detached ⇒ the token is revoked and the endpoint answers 404.
	if _, err := m.client.DetachWorker(ctx, &pb.DetachWorkerRequest{OrchestratorId: o.GetId(), SandboxId: sb.GetId(), Guard: &pb.LaneGuardConfirm{UnlanedActions: []string{"review-pr"}}}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(hs.URL+worker.EndpointPrefix+tok+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("after detach = %d, want 404", resp.StatusCode)
	}
}
