package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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

	// holdRuntime takes a sandbox's runtime lock for reading, with a context that
	// a lifecycle operation cancels when it asks for the lock (SWI-26,
	// sandbox.Manager.HoldRuntimeRead): `sbx mcp load` starts a stopped sandbox,
	// so it never runs inside an operation, and a load may take a minute, so no
	// operation waits one out. nil = ungated. A refusal names the operation
	// holding the lock.
	holdRuntime func(ctx context.Context, sandboxID string) (held context.Context, unlock func(), err error)
}

// register adds the per-worker server and, when the sandbox is running, loads
// it live. An SSRF warning line the runtime prints for a loopback URL is logged,
// never treated as failure; a non-zero `add` is. `load` is skipped (and left to
// the bring-up hook) when the sandbox is not running by then.
func (g *gateway) register(ctx context.Context, sandboxID, token string, resolve func() (string, error)) error {
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
	err = g.load(ctx, sandboxID, resolve)
	if errors.Is(err, errNotRunning) {
		g.logf("[worker] %s registered; load deferred until sandbox %s is running", name, sandboxID)
		return nil
	}
	return err
}

// errLoadBusy refuses a load while a lifecycle operation holds the sandbox's
// runtime lock, or reports one that operation cancelled; Service.loadWhen
// retries it.
var errLoadBusy = errors.New("gateway load refused")

// load attaches the registration to the sandbox live, under the sandbox's runtime
// lock (read, try-lock: errLoadBusy while an operation holds it). resolve runs under
// that lock and names the sandbox to load into, or refuses (errNotRunning): a read
// before the lock could be of a sandbox an operation has stopped since.
func (g *gateway) load(ctx context.Context, sandboxID string, resolve func() (string, error)) error {
	if g == nil || g.cli == nil {
		return nil
	}
	lctx := ctx
	if g.holdRuntime != nil {
		held, unlock, err := g.holdRuntime(ctx, sandboxID)
		if err != nil {
			return fmt.Errorf("%w: %v", errLoadBusy, err)
		}
		defer unlock()
		lctx = held
	}
	ref, err := resolve()
	if err != nil {
		return err
	}
	name := ServerName(sandboxID)
	if _, err := g.cli.Load(lctx, name, ref); err != nil {
		if lctx.Err() != nil && ctx.Err() == nil {
			return errLoadBusy
		}
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
