package mcprules

import (
	"net/url"
	"regexp"
	"strings"
)

// envNameRe is a positional argument's `NAME` in `NAME=value` (an environment-
// style assignment), so a URL or path argument is not read as one.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// credentialLooking reports whether a query-parameter or flag name has a
// credential-word segment (FR-423), or `sig`/`signature`.
func credentialLooking(name string) bool {
	for _, s := range segments(name) {
		if credentialSegment(s) || s == "sig" || s == "signature" {
			return true
		}
	}
	return false
}

// URLUserinfo is CredentialLooking's entry for credentials written into the
// URL itself (`https://user:token@host/…`).
const URLUserinfo = "URL user info"

// CredentialLooking returns the URL query-parameter names, URL user info, and
// the host-command `--flag` names and positional `NAME=value` names that look
// like credentials (FR-423, US2 #10). The form warns about them; they are never
// refused. Only names are returned, never values.
func CredentialLooking(rawURL string, args []string) []string {
	var out []string
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && rawURL != "" {
		if u.User != nil {
			out = append(out, URLUserinfo)
		}
		for name := range u.Query() {
			if credentialLooking(name) {
				out = append(out, name)
			}
		}
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			if name, _, ok := strings.Cut(a, "="); ok && envNameRe.MatchString(name) && credentialLooking(name) {
				out = append(out, name)
			}
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name != "" && credentialLooking(name) {
			out = append(out, "--"+name)
		}
	}
	sortStrings(out)
	return out
}
