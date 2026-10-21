package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
)

// The per-worker gateway registration (research R4, contracts/orchestrator-mcp.md
// §Per-worker gateway registration): one remote MCP server `sxb-orch-<sandbox8>`
// per worker, pointing the host gateway at this daemon's token-addressed
// endpoint, loaded into the sandbox live. The token is minted here, stored only
// in the registry's worker_tokens bucket, and never travels on the wire.

// mintToken returns workerTokenBytes random bytes as lowercase hex.
func mintToken() string {
	b := make([]byte, workerTokenBytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ServerName is the gateway registration name for a sandbox: the reserved
// prefix plus the first eight characters of its id.
func ServerName(sandboxID string) string {
	short := sandboxID
	if len(short) > 8 {
		short = short[:8]
	}
	return mcp.WorkerServerPrefix + short
}

// endpointURL is the URL the gateway dials for a worker's token.
func endpointURL(hookPort, token string) string {
	return fmt.Sprintf("http://%s:%s%s%s%s", gatewayHost, hookPort, endpointPrefix, token, endpointSuffix)
}

// gateway drives the three `sbx mcp` rows for worker registrations.
type gateway struct {
	cli      *mcp.CLI
	hookPort string
	logf     func(string, ...any)
}

// register adds the per-worker server and, when the sandbox is running, loads
// it live. An SSRF warning line the runtime prints for a loopback URL is logged,
// never treated as failure; a non-zero `add` is. `load` is skipped (and left to
// the bring-up hook) when the sandbox is not running.
func (g *gateway) register(ctx context.Context, sandboxID, ref string, running bool, token string) error {
	if g == nil || g.cli == nil {
		return nil
	}
	name := ServerName(sandboxID)
	out, err := g.cli.AddRemote(ctx, name, endpointURL(g.hookPort, token))
	if err != nil {
		return fmt.Errorf("register gateway server %s: %w", name, err)
	}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && strings.Contains(strings.ToLower(l), "warn") {
			g.logf("[worker] %s: %s", name, l)
		}
	}
	if !running {
		g.logf("[worker] %s registered; load deferred until sandbox %s is running", name, sandboxID)
		return nil
	}
	return g.load(ctx, sandboxID, ref)
}

// load attaches the registration to the sandbox live.
func (g *gateway) load(ctx context.Context, sandboxID, ref string) error {
	if g == nil || g.cli == nil {
		return nil
	}
	name := ServerName(sandboxID)
	if _, err := g.cli.Load(ctx, name, ref); err != nil {
		return fmt.Errorf("load gateway server %s into %s: %w", name, ref, err)
	}
	return nil
}

// unregister removes the registration; errors are logged, never fatal — the
// token was revoked first, so the endpoint already refuses the name.
func (g *gateway) unregister(ctx context.Context, sandboxID string) {
	if g == nil || g.cli == nil {
		return
	}
	name := ServerName(sandboxID)
	if _, err := g.cli.Remove(ctx, name); err != nil {
		g.logf("[worker] remove gateway server %s: %v", name, err)
	}
}
