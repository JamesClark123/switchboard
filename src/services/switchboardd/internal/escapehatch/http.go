package escapehatch

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// The headers the wrapper identifies the agent with (agent.HookServer's).
const (
	HeaderToken = "X-Sxb-Token"
	HeaderGen   = "X-Sxb-Gen"
)

// maxRunBody bounds a run request's body.
const maxRunBody = 1 << 20

// ErrNoAgentToken refuses a call made outside the agent's process tree.
var ErrNoAgentToken = errors.New("no agent token in this environment; run the escape hatch from the agent's session")

// runRequest is the body the injected wrapper POSTs to /escape-hatch/run. It carries
// the sandbox id, the command NAME, and — for commands that permit it — an optional
// workspace selector and argument string. It never carries a command string: the
// name resolves against the sandbox's persisted allowlist, and workspace/args are
// validated against that command's declared constraints (SC-004).
type runRequest struct {
	SandboxID string `json:"sandbox_id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	Args      string `json:"args"`
}

type runResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// HandleRun is the POST /escape-hatch/run handler (contracts/escape-hatch-http.md).
// It validates the name against the allowlist, enqueues the run, and responds
// immediately (async) — long runs never hold the connection open. An unknown name
// (or sandbox) is a 404 with nothing executed.
func (s *Service) HandleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRunBody)).Decode(&req); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SandboxID) == "" || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "sandbox_id and name are required", http.StatusBadRequest)
		return
	}

	// Only the agent's own process tree may run a command (SWI-17, research
	// R13): the call must carry a live generation's token of this sandbox.
	switch tok := r.Header.Get(HeaderToken); {
	case tok == "":
		writeJSON(w, http.StatusForbidden, errorResponse{Error: ErrNoAgentToken.Error()})
		return
	case s.agentAuth == nil || !s.agentAuth(req.SandboxID, r.Header.Get(HeaderGen), tok):
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "unknown agent token"})
		return
	}

	run, err := s.Invoke(req.SandboxID, req.Name, req.Workspace, req.Args)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidArgs), errors.Is(err, ErrInvalidWorkspace):
			// The command exists but the agent's arguments/workspace are rejected.
			// 400 with the reason so the agent can correct and retry.
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		default:
			// Unknown name or sandbox -> 404, nothing ran. The wrapper prints this and
			// the agent learns the command is unavailable.
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "command not available"})
		}
		return
	}
	writeJSON(w, http.StatusOK, runResponse{RunID: run.GetId(), Status: statusSlug(run.GetStatus())})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
