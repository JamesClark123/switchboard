package worker

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
)
