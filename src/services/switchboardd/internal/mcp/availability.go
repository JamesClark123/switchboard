package mcp

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxkit"
)

// Availability is whether this host's MCP gateway can be managed, with a human
// reason when it cannot (FR-079). Probed once at daemon startup and carried in
// DaemonInfo; every gateway RPC checks it first.
type Availability struct {
	Available   bool
	Reason      string
	SbxVersion  string // parsed MAJOR.MINOR.PATCH ("" when unparseable)
	BaselineMet bool   // SbxVersion >= sbxkit.MinSbxVersion
}

// probeTimeout bounds each startup probe command.
const probeTimeout = 10 * time.Second

// Probe checks, in order: the CLI resolves on PATH; `--version` parses and meets
// the baseline; `mcp ls` answers (a sign-in prompt in its output means the host
// is not logged in). The first failure yields the reason.
func Probe(ctx context.Context, bin string) Availability {
	cli := &CLI{Bin: bin}
	if _, err := exec.LookPath(bin); err != nil {
		return Availability{Reason: fmt.Sprintf("the sandbox CLI %q is not installed on this host", bin)}
	}
	vctx, cancel := context.WithTimeout(ctx, probeTimeout)
	out, err := cli.run(vctx, argvVersion()...)
	cancel()
	if err != nil {
		return Availability{Reason: "the sandbox CLI did not report its version: " + err.Error()}
	}
	v, ok := sbxkit.ParseVersion(out)
	if !ok {
		return Availability{Reason: fmt.Sprintf("could not parse the sandbox CLI version from %q", strings.TrimSpace(out))}
	}
	a := Availability{SbxVersion: v, BaselineMet: sbxkit.AtLeast(v, sbxkit.MinSbxVersion)}
	if !a.BaselineMet {
		a.Reason = fmt.Sprintf("host sbx %s is below the minimum %s required for the MCP gateway and kit schema 2", v, sbxkit.MinSbxVersion)
		return a
	}
	lctx, cancel := context.WithTimeout(ctx, probeTimeout)
	out, err = cli.run(lctx, argvList()...)
	cancel()
	if err != nil {
		if looksSignedOut(out) {
			a.Reason = "this host is not signed in to the sandbox tooling — run `sbx login` there"
		} else {
			a.Reason = "`sbx mcp` is not usable on this host: " + err.Error()
		}
		return a
	}
	if looksSignedOut(out) {
		a.Reason = "this host is not signed in to the sandbox tooling — run `sbx login` there"
		return a
	}
	a.Available = true
	return a
}

func looksSignedOut(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "not authenticated") || strings.Contains(l, "sbx login") || strings.Contains(l, "sign in") || strings.Contains(l, "signed in")
}
