package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP endpoint (research R5, contracts/orchestrator-mcp.md §Endpoint):
// `POST /orchestrator/w/<token>/mcp`, streamable HTTP served stateless by the
// official SDK. The token names the sandbox, so every call is attributed before
// a handler runs; the handler relays the call over the orchestrator's link and
// returns its result, or the documented unreachable text while the link is
// down. One cached server per live token; revocation and renames drop it.

// EndpointPrefix is the path every worker endpoint lives under.
const EndpointPrefix = "/orchestrator/w/"

// EndpointServerName is what the endpoint announces in the MCP handshake.
const EndpointServerName = "switchboard-orchestrator"

// Tool names, fixed so the briefing can cite them.
const (
	ToolListActions      = "orchestrator_list_actions"
	ToolCallAction       = "orchestrator_call_action"
	ToolCheckRun         = "orchestrator_check_run"
	ToolReportCompletion = "orchestrator_report_completion"
)

// Tool payloads (contracts/orchestrator-mcp.md §Tools). The orchestrator
// renders the same shapes; the worker side types them so the SDK publishes
// input and output schemas.

type EmptyIn struct{}

type ArgumentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default"`
}

type ActionInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Type        string         `json:"type" jsonschema:"prompt (runs on a free worker) or reason (runs on the reasoning sandbox)"`
	WorkerTag   string         `json:"worker_tag"`
	Arguments   []ArgumentInfo `json:"arguments"`
}

type ListActionsOut struct {
	Orchestrator string       `json:"orchestrator"`
	Actions      []ActionInfo `json:"actions"`
}

type CallActionIn struct {
	Action    string            `json:"action" jsonschema:"the action's name, as listed by orchestrator_list_actions"`
	Arguments map[string]string `json:"arguments,omitempty" jsonschema:"argument values by name; required arguments must be present"`
}

type CallActionOut struct {
	RunID    string `json:"run_id"`
	Position uint32 `json:"position"`
	State    string `json:"state"`
}

type CheckRunIn struct {
	RunID string `json:"run_id" jsonschema:"the run id orchestrator_call_action returned"`
}

type CheckRunOut struct {
	RunID             string `json:"run_id"`
	Action            string `json:"action"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	Summary           string `json:"summary"`
	ClosedByDeveloper bool   `json:"closed_by_developer"`
}

type ReportIn struct {
	Summary string `json:"summary" jsonschema:"1-2 sentences on what you did (1-2000 characters)"`
}

type ReportOut struct {
	OK    bool   `json:"ok"`
	RunID string `json:"run_id"`
	State string `json:"state"`
}

// Handler serves the worker endpoints. GET and DELETE answer 405 (stateless
// mode); an unknown or revoked token answers 404.
func (s *Service) Handler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(s.serverFor, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, DisableLocalhostProtection: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SDK answers 400 when getServer yields nil; the contract wants 404
		// for a token it does not know, so resolve first (cached, cheap).
		if s.serverFor(r) == nil {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// tokenFromPath extracts <token> from /orchestrator/w/<token>/mcp.
func tokenFromPath(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, EndpointPrefix)
	if !ok {
		return "", false
	}
	tok, tail, ok := strings.Cut(rest, "/")
	if !ok || tok == "" || tail != "mcp" {
		return "", false
	}
	return tok, true
}

// serverFor resolves the request's token to its sandbox and returns that
// sandbox's cached server (nil ⇒ the SDK answers 404).
func (s *Service) serverFor(r *http.Request) *mcp.Server {
	tok, ok := tokenFromPath(r.URL.Path)
	if !ok {
		return nil
	}
	sandboxID := s.sandboxForToken(tok)
	if sandboxID == "" {
		return nil
	}
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	if srv := s.servers[tok]; srv != nil {
		return srv
	}
	sb, err := s.reg.Get(sandboxID)
	if err != nil || sb.GetController() == nil {
		return nil
	}
	srv := s.newServer(sandboxID, sb.GetController().GetName(), sb.GetController().GetReasoning())
	s.servers[tok] = srv
	return srv
}

// dropServers forgets the cached servers of these tokens.
func (s *Service) dropServers(tokens ...string) {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	for _, tok := range tokens {
		delete(s.servers, tok)
	}
}

// Instructions is the short briefing the MCP handshake carries (the managed
// CLAUDE.md block holds the long form with the action list).
func Instructions(name string, reasoning bool) string {
	role := "a worker"
	if reasoning {
		role = "the reasoning sandbox"
	}
	return fmt.Sprintf("This sandbox is %s of the switchboard orchestrator %q. Runs arrive as framed prompts; every run ends with %s (one short summary, once per run; retry if it fails). %s lists what you may delegate, %s queues it on another sandbox, %s reads a delegated run's outcome.",
		role, name, ToolReportCompletion, ToolListActions, ToolCallAction, ToolCheckRun)
}

// newServer builds the per-token server with the four tools bound to one sandbox.
func (s *Service) newServer(sandboxID, name string, reasoning bool) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: EndpointServerName, Version: s.version}, &mcp.ServerOptions{Instructions: Instructions(name, reasoning)})
	mcp.AddTool(srv, &mcp.Tool{Name: ToolListActions, Description: "List the orchestrator's actions you may delegate, with their arguments."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyIn) (*mcp.CallToolResult, ListActionsOut, error) {
			var out ListActionsOut
			err := s.relay(ctx, sandboxID, ToolListActions, nil, &out)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: ToolCallAction, Description: "Delegate work: call an action with arguments. Returns the queued run's id and position; the run executes on another sandbox."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in CallActionIn) (*mcp.CallToolResult, CallActionOut, error) {
			var out CallActionOut
			err := s.relay(ctx, sandboxID, ToolCallAction, in, &out)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: ToolCheckRun, Description: "The state, outcome and summary of a run you delegated."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in CheckRunIn) (*mcp.CallToolResult, CheckRunOut, error) {
			var out CheckRunOut
			err := s.relay(ctx, sandboxID, ToolCheckRun, in, &out)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: ToolReportCompletion, Description: "End your current run with a short summary. Call this once, before you finish; retry if it fails."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ReportIn) (*mcp.CallToolResult, ReportOut, error) {
			var out ReportOut
			err := s.relay(ctx, sandboxID, ToolReportCompletion, in, &out)
			return nil, out, err
		})
	return srv
}

// relay attributes a call to its sandbox's current controller, sends it over
// the link and decodes the orchestrator's JSON result into out. Errors become
// MCP tool errors (IsError) with this text.
func (s *Service) relay(ctx context.Context, sandboxID, tool string, in any, out any) error {
	sb, err := s.reg.Get(sandboxID)
	if err != nil || sb.GetController() == nil {
		return errors.New("this sandbox is no longer controlled by an orchestrator")
	}
	ctl := sb.GetController()
	args := "{}"
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		args = string(b)
	}
	body, err := s.Call(ctx, ctl.GetOrchestratorId(), ctl.GetName(), sandboxID, tool, args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		return fmt.Errorf("malformed result from the orchestrator: %v", err)
	}
	return nil
}
