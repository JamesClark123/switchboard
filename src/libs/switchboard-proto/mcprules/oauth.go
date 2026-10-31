package mcprules

import (
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxClientID bounds a pre-registered OAuth client ID (FR-105).
const MaxClientID = 256

// ValidClientID checks a pre-registered OAuth client ID (FR-105): one token of
// at most MaxClientID characters, not flag-like. Empty is allowed.
func ValidClientID(id string) error {
	if id != "" && (utf8.RuneCountInString(id) > MaxClientID || !utf8.ValidString(id) || id[0] == '-' ||
		strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
		return invalid("client ID must be a single token of at most %d characters", MaxClientID)
	}
	return nil
}

// CheckAuthServer checks the authorization server setting. Empty is allowed.
func CheckAuthServer(s string) error {
	if s != "" && (!AuthServerOK(s) || strings.IndexFunc(s, unicode.IsControl) >= 0) {
		return invalid("authorization server must be an https URL (http only to localhost or a loopback IP) or an absolute path on the daemon's host")
	}
	return nil
}

// AuthServerOK accepts an absolute path, an https URL, or an http URL to a
// loopback IP or exactly localhost: the metadata names the endpoints the
// client secret is sent to, so it is never fetched in cleartext across a
// network. *.localhost is refused: `sbx` dials the daemon's host, whose
// resolver may ask the network's DNS for such a name.
func AuthServerOK(s string) bool {
	if filepath.IsAbs(s) {
		return true
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	return u.Scheme == "http" && (strings.EqualFold(h, "localhost") || ip != nil && ip.IsLoopback())
}
