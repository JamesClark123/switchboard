package editor

import "strings"

// The rules Precheck reports besides the Custom command's and the target's.
const (
	RuleHostLookup = "host-lookup"
	RuleNoRemote   = "no-remote"
)

// Request is one `v` press (data-model §4).
type Request struct {
	Editor Editor
	// Bins holds the raw override values by editor ID (FR-502).
	Bins map[string]string
	// Custom is the session's Custom command, raw.
	Custom string
	// SSHTarget is the host's SSH target; "" = local (FR-520).
	SSHTarget string
	// HostErr is a failed host lookup: `v` refuses for every editor (FR-520).
	HostErr error
	// WorkspacePath is the folder from the daemon: "" for Precheck, set for Resolve.
	WorkspacePath string
	// Home is the developer's home directory; "" = unknown (FR-531).
	Home string
}

// Plan is what to start.
type Plan struct {
	// Argv is the command line; Argv[0] is the executable as resolved.
	Argv []string
	// Executable is Argv[0], for messages (FR-532); the caller cuts it.
	Executable string
	// RemoteNoHost: Custom on a remote sandbox with no {host}, so the
	// completion warns that the path is on the remote host (FR-515).
	RemoteNoHost bool
	// EnvVar is where to fix a built-in editor; "" for Custom.
	EnvVar string
}

// prepared is what Precheck and Resolve share: the executable and, for Custom,
// the expanded words; for Zed on a remote sandbox, the address.
type prepared struct {
	exe     string
	custom  []string // Custom: the words after ~ expansion, markers intact
	address string   // Zed remote: the ssh:// address
}

// prepare runs every check that needs no workspace path, in the contract's
// order (§3 steps 1–5): the host lookup, remote support, the Custom command's
// rules and home, the SSH target, then the executable.
func prepare(req Request) (prepared, *Refusal) {
	var p prepared
	if req.HostErr != nil {
		return p, &Refusal{Rule: RuleHostLookup, Detail: req.HostErr.Error(), Where: WhereHostsFile}
	}
	remote := req.SSHTarget != ""
	e := req.Editor
	if remote && e.Remote == RemoteNone {
		return p, &Refusal{Rule: RuleNoRemote, Where: WhereSettings}
	}
	if e.ID == Custom {
		words, err := Split(req.Custom)
		if err == nil {
			err = Check(words)
		}
		var args []string
		if err == nil {
			args, err = Expand(words, req.Home)
		}
		if err != nil {
			ce, _ := err.(*CheckError)
			where := WhereSettings
			if ce != nil && ce.Rule == RuleNoHome {
				where = WhereEnv
			}
			rule := "invalid"
			if ce != nil {
				rule = ce.Rule
			}
			return p, &Refusal{Rule: rule, Where: where}
		}
		p.custom = args
		p.exe = args[0]
	} else {
		p.exe, _ = Command(e, req.Bins)
	}
	if remote {
		if strings.HasPrefix(req.SSHTarget, "-") {
			return p, &Refusal{Rule: RuleTargetDash, Where: WhereHostEntry}
		}
		if e.Remote == RemoteSSHAddress {
			addr, ref := ZedAddress(req.SSHTarget, req.WorkspacePath)
			if ref != nil {
				return p, ref
			}
			p.address = addr
		}
	}
	return p, nil
}

// Precheck runs every check that doesn't need the workspace path (FR-526) and
// returns the executable to look up, or the refusal.
func Precheck(req Request) (exe string, ref *Refusal) {
	p, ref := prepare(req)
	if ref != nil {
		return "", ref
	}
	return p.exe, nil
}

// Resolve runs the checks again with the workspace path (contract §3 steps
// 6–8): the path checks, with the character limit for a remote sandbox with
// Custom or the selected editor's own variable overriding its default, then
// the argv per editor.
func Resolve(req Request) (Plan, *Refusal) {
	p, ref := prepare(req)
	if ref != nil {
		return Plan{}, ref
	}
	e := req.Editor
	remote := req.SSHTarget != ""
	_, overridden := Command(e, req.Bins)
	limited := remote && (e.ID == Custom || overridden)
	if ref := checkPath(req.WorkspacePath, limited); ref != nil {
		if ref.Rule == RulePathLimited {
			ref.Where = WhereSettings
			if e.ID != Custom {
				ref.Where = e.EnvVar
			}
		}
		return Plan{}, ref
	}
	plan := Plan{Executable: p.exe, EnvVar: e.EnvVar}
	if e.ID == Custom {
		argv, _ := Substitute(p.custom, req.WorkspacePath, req.SSHTarget)
		plan.Argv = argv
		plan.RemoteNoHost = remote && !HasHostMarker(p.custom)
		return plan, nil
	}
	switch {
	case !remote:
		plan.Argv = []string{p.exe, req.WorkspacePath}
	case e.Remote == RemoteSSHAddress:
		plan.Argv = []string{p.exe, p.address}
	default:
		plan.Argv = []string{p.exe, "--remote", "ssh-remote+" + req.SSHTarget, req.WorkspacePath}
	}
	return plan, nil
}
