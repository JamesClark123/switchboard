package mcprules

import (
	"net/url"
	"strings"
)

// credentialLooking reports whether a query-parameter or flag name has a
// credential-word segment (FR-423), or `sig`/`signature`.
func credentialLooking(name string) bool {
	for _, s := range segments(name) {
		if credentialWords[s] || s == "sig" || s == "signature" {
			return true
		}
	}
	return false
}

// CredentialLooking returns the URL query-parameter names and host-command
// `--flag` names that look like credentials (FR-423, US2 #10). The form warns
// about them; they are never refused.
func CredentialLooking(rawURL string, args []string) []string {
	var out []string
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && rawURL != "" {
		for name := range u.Query() {
			if credentialLooking(name) {
				out = append(out, name)
			}
		}
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
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
