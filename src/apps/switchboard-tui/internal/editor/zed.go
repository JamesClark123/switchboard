package editor

import (
	"net/url"
	"strings"
)

// The SSH target rules (FR-513, FR-517).
const (
	RuleTargetDash = "target-dash"
	RuleZedTarget  = "zed-target"
)

// ZedAddress builds Zed's `ssh://[user@]host[:port]/<path>` argument for a
// remote folder (FR-513; research F5: the form Zed's open_listener.rs parses,
// with the path percent-encoded as a URL path). Host entries are free text,
// so the target is normalised: a leading ssh:// is dropped, the user is split
// at the last @, and a bare IPv6 address gets brackets. The result must parse
// back to the same user, host and path, or the target can't be an authority
// (zed-target). A normalised user or host starting with - is refused
// (target-dash), as the raw target is by Precheck.
func ZedAddress(target, path string) (string, *Refusal) {
	t := strings.TrimPrefix(target, "ssh://")
	user, hostport := "", t
	if i := strings.LastIndex(t, "@"); i >= 0 {
		user, hostport = t[:i], t[i+1:]
	}
	if strings.Count(hostport, ":") > 1 && !strings.HasPrefix(hostport, "[") {
		hostport = "[" + hostport + "]"
	}
	if strings.HasPrefix(user, "-") || strings.HasPrefix(hostport, "-") {
		return "", &Refusal{Rule: RuleTargetDash, Where: WhereHostEntry}
	}
	// A ':' in the user would be URL userinfo with a password, which url.User
	// would encode and Zed decode back into the user name.
	if hostport == "" || strings.Contains(user, ":") {
		return "", &Refusal{Rule: RuleZedTarget, Where: WhereHostEntry}
	}
	u := url.URL{Scheme: "ssh", Host: hostport, Path: path}
	if user != "" {
		u.User = url.User(user)
	}
	s := u.String()
	back, err := url.Parse(s)
	if err != nil || back.Scheme != "ssh" || back.Host != hostport || back.Path != path || back.User.Username() != user || back.RawQuery != "" || back.Fragment != "" {
		return "", &Refusal{Rule: RuleZedTarget, Where: WhereHostEntry}
	}
	return s, nil
}
