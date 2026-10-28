package worker

import "time"

// Every bound is a package constant (research R15).
const (
	// workerTokenBytes is the size of the per-worker URL token minted at attach.
	workerTokenBytes = 32
	// gatewayHost is the host the loopback registration points the gateway at
	// (research R4). ⚠ Documentation-derived: quickstart Scenario 0 may flip it
	// to "host.docker.internal" if the gateway dials from inside the sandbox.
	gatewayHost = "127.0.0.1"
	// endpointPrefix is the route on the hook HTTP server: /orchestrator/w/<token>/mcp.
	endpointPrefix = "/orchestrator/w/"
	endpointSuffix = "/mcp"

	// Step delivery (SWI-17, contracts/worker-step.md §3 and §6). The worst
	// case, submitWindow × (1 + submitRePresses) + modalGrace + stepExtension
	// plus the guard and typing, stays under the orchestrator's 15 s
	// deliverTimeout: keep it so when changing any of them.
	//
	// submitWindow is how long the submit check waits for a turn to start
	// after the Enter (and after each re-press).
	submitWindow = 2500 * time.Millisecond
	// submitRePresses bounds the Enter re-presses for a step still in the box.
	submitRePresses = 3
	// modalGrace is how long a picker or dialog may wait for a needs-input
	// report (a skill's own question) before the step is not accepted. Capped
	// at 2 s by the bound above.
	modalGrace = time.Second
	// stepExtension is the one extension a check may take, shared by a stale
	// screen's repaint and Clear's post-clear poll (clearSettle, T046).
	stepExtension = time.Second
	// stepPoll is how often the submit check reads the screen.
	stepPoll = 50 * time.Millisecond
)
