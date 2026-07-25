package escapehatch

import (
	"encoding/json"
	"net/http"
	"strings"
)

// runRequest is the body the injected wrapper POSTs to /escape-hatch/run. It carries
// only the sandbox id and the command NAME — never a command string. The name is
// resolved against the sandbox's persisted allowlist; there is no field by which the
// agent can influence what runs (SC-004).
type runRequest struct {
	SandboxID string `json:"sandbox_id"`
	Name      string `json:"name"`
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SandboxID) == "" || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "sandbox_id and name are required", http.StatusBadRequest)
		return
	}

	run, err := s.Invoke(req.SandboxID, req.Name)
	if err != nil {
		// Unknown name or sandbox -> 404, nothing ran. The wrapper prints this and the
		// agent learns the command is unavailable.
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "command not available"})
		return
	}
	writeJSON(w, http.StatusOK, runResponse{RunID: run.GetId(), Status: statusSlug(run.GetStatus())})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
