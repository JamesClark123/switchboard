package mcprules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RedactMarker replaces a credential literal wherever one would be shown.
const RedactMarker = "••••"

// credentialWords are the name segments that make a header a credential
// header (FR-423); names are split on `-` and `_` and compared case-insensitively.
var credentialWords = map[string]bool{
	"key": true, "apikey": true, "token": true, "secret": true, "password": true,
	"auth": true, "credential": true, "credentials": true,
}

// credentialNames are whole header names that are always credential headers.
var credentialNames = map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true}

// schemes is the fixed list of auth-scheme tokens a credential header may lead with.
var schemes = map[string]bool{
	"bearer": true, "basic": true, "token": true, "bot": true, "apikey": true, "key": true,
	"ssws": true, "dpop": true, "splunk": true, "geniekey": true,
}

// cookieNameRe is an RFC 6265 cookie name (a token) followed by `=`.
var cookieNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+=`)

// schemeLikeRe is a word that reads as an auth scheme.
var schemeLikeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

func segments(name string) []string {
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '-' || r == '_' })
}

// IsCredentialHeader reports whether a header name is a credential header
// (FR-423). Name segments, not substrings, match: `Keycloak-Realm` is not one,
// `X-Api-Key` is.
func IsCredentialHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if credentialNames[n] {
		return true
	}
	for _, s := range segments(n) {
		if credentialWords[s] {
			return true
		}
	}
	return false
}

// splitScheme returns the leading scheme token (when the value starts with a
// word followed by whitespace) and the rest.
func splitScheme(value string) (scheme, rest string) {
	i := strings.IndexAny(value, " \t")
	if i <= 0 {
		return "", value
	}
	return value[:i], strings.TrimLeft(value[i:], " \t")
}

// CheckCredentialValue applies the credential-header rule (FR-423) to one
// header: a credential header's value holds only `${placeholder}`s, optionally
// led by a listed scheme, joined by space, `:`, `;` or `,`; in a Cookie header a
// placeholder may be led by `<cookie-name>=`. The message names the header and
// the position of the literal text, never the value. Non-credential headers
// always pass.
func CheckCredentialValue(name, value string) error {
	if !IsCredentialHeader(name) {
		return nil
	}
	value = strings.TrimSpace(value)
	rest := value
	where := "the value"
	if s, r := splitScheme(value); s != "" && !strings.HasPrefix(s, "${") {
		switch {
		case schemes[strings.ToLower(s)]:
			rest, where = r, "the text after the scheme"
		case schemeLikeRe.MatchString(s) && strings.HasPrefix(r, "${"):
			return invalid("header %q: %q is not a recognised auth scheme, so it is literal text; put the scheme inside the secret (a credential header holds only ${placeholder}s)", name, s)
		}
	}
	cookie := strings.EqualFold(strings.TrimSpace(name), "cookie")
	found := false
	pos := where
	for rest != "" {
		switch c := rest[0]; {
		case c == ' ' || c == '\t' || c == ':' || c == ';' || c == ',':
			rest = rest[1:]
			continue
		case strings.HasPrefix(rest, "${"):
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				return invalid("header %q: %s has an unterminated ${…}", name, pos)
			}
			found = true
			rest = rest[end+1:]
			pos = "the text after ${…}"
			continue
		case cookie:
			if loc := cookieNameRe.FindStringIndex(rest); loc != nil && strings.HasPrefix(rest[loc[1]:], "${") {
				rest = rest[loc[1]:]
				continue
			}
		}
		if !found {
			if strings.Contains(rest, "${") {
				pos = "the text before ${…}"
			}
		}
		return invalid("header %q is a credential header and %s is literal text; it must hold only ${placeholder}s (convert the literal to a secret)", name, pos)
	}
	if !found {
		return invalid("header %q is a credential header and has no ${placeholder}; its value must be held as a secret (convert it)", name)
	}
	return nil
}

// CheckCredentialHeaders applies CheckCredentialValue to every header, except
// the names in skip (lower-cased).
func CheckCredentialHeaders(headers []string, skip map[string]bool) error {
	for _, h := range headers {
		n, v := SplitHeader(h)
		if skip[strings.ToLower(n)] {
			continue
		}
		if err := CheckCredentialValue(n, v); err != nil {
			return err
		}
	}
	return nil
}

// Convert turns a credential header's literal value into a placeholder (FR-423):
// newValue keeps a listed scheme (or a single cookie's name) and holds
// `${placeholder}`; secret is the literal that moves into the placeholder's
// secret. The placeholder is derived from the header name (lower-cased, `_` →
// `-`) and gets `-2`, `-3`, … when it is invalid, reserved or in used.
func Convert(name, value string, used map[string]bool) (newValue, placeholder, secret string) {
	value = strings.TrimSpace(value)
	prefix := ""
	secret = value
	if s, r := splitScheme(value); s != "" && schemes[strings.ToLower(s)] && r != "" {
		prefix, secret = s+" ", r
	} else if strings.EqualFold(strings.TrimSpace(name), "cookie") {
		if loc := cookieNameRe.FindStringIndex(value); loc != nil && !strings.ContainsAny(value, ";,") {
			prefix, secret = value[:loc[1]], value[loc[1]:]
		}
	}
	placeholder = derivePlaceholder(name, used)
	return prefix + "${" + placeholder + "}", placeholder, secret
}

func derivePlaceholder(name string, used map[string]bool) string {
	base := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
	base = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '-'
	}, base)
	base = strings.TrimLeft(base, ".-")
	if len(base) > MaxPlaceholder-4 {
		base = base[:MaxPlaceholder-4]
	}
	if base == "" {
		base = "secret"
	}
	cand := base
	for i := 2; used[cand] || ValidPlaceholder(cand) != nil; i++ {
		cand = base + "-" + strconv.Itoa(i)
	}
	return cand
}

// Redact keeps a leading listed scheme and replaces the rest with RedactMarker.
func Redact(value string) string {
	if s, r := splitScheme(strings.TrimSpace(value)); s != "" && schemes[strings.ToLower(s)] && r != "" {
		return s + " " + RedactMarker
	}
	return RedactMarker
}

// RedactHeader returns the header with its value redacted when it is a
// credential header that breaks the rule; ok reports whether it was redacted.
func RedactHeader(h string) (out string, redacted bool) {
	n, v := SplitHeader(h)
	if CheckCredentialValue(n, v) == nil {
		return h, false
	}
	return fmt.Sprintf("%s: %s", n, Redact(v)), true
}
