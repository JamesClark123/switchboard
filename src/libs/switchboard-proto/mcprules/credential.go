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
// header (FR-423); names are split on `-`, `_` and `.` and compared
// case-insensitively.
var credentialWords = map[string]bool{
	"key": true, "apikey": true, "token": true, "secret": true, "password": true,
	"auth": true, "credential": true, "credentials": true, "authorization": true, "authentication": true,
}

// credentialSuffixes make a run-together segment a credential one when it ends
// with them (`AccessToken`, `ClientSecret`, `DbPassword`).
var credentialSuffixes = []string{"token", "secret", "password"}

// keyPrefixes make a run-together `<prefix>key` segment a credential one
// (`AccessKey`, `PrivateKey`), while `Monkey` and `Hotkey` are not.
var keyPrefixes = map[string]bool{
	"api": true, "access": true, "secret": true, "private": true, "auth": true, "client": true,
	"session": true, "signing": true, "subscription": true,
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
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '-' || r == '_' || r == '.' })
}

// credentialSegment reports whether one lower-cased name segment is a
// credential word (FR-423): a listed word, a run-together word ending in
// `token`/`secret`/`password`, or a listed `<prefix>key`.
func credentialSegment(s string) bool {
	if credentialWords[s] {
		return true
	}
	for _, suf := range credentialSuffixes {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	p, ok := strings.CutSuffix(s, "key")
	return ok && keyPrefixes[p]
}

// IsCredentialHeader reports whether a header name is a credential header
// (FR-423). Name segments, not substrings, match: `Keycloak-Realm` is not one,
// `X-Api-Key`, `X-Authorization` and `X-AccessToken` are.
func IsCredentialHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if credentialNames[n] {
		return true
	}
	for _, s := range segments(n) {
		if credentialSegment(s) {
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
			// Placeholders() matches only ${…} without braces inside, so
			// anything else would pass here and be sent as literal text.
			if strings.ContainsAny(rest[2:end], "{$") {
				return invalid("header %q: %s has a ${…} with a brace or $ inside", name, pos)
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

// Convert turns a credential header's literal text into a placeholder (FR-423):
// newValue keeps a listed scheme (or, in a Cookie header, every cookie name) and
// the value's existing `${…}`s, and its first run of literal text becomes `${placeholder}`;
// secret is that literal text, so a secret never holds `${…}` itself. A value
// with more than one literal run (in a Cookie header, each cookie's value is
// one) is converted one run per call. The placeholder
// is derived from the header name (lower-cased, `_` → `-`) and gets `-2`, `-3`,
// … when it is invalid, reserved or in use. A value with no literal text comes
// back unchanged with an empty secret.
func Convert(name, value string, used map[string]bool) (newValue, placeholder, secret string) {
	value = strings.TrimSpace(value)
	head, rest := "", value
	if s, r := splitScheme(value); s != "" && schemes[strings.ToLower(s)] && r != "" {
		head, rest = s+" ", r
	}
	placeholder = derivePlaceholder(name, used)
	start, end := literalRun(rest, strings.EqualFold(strings.TrimSpace(name), "cookie"))
	if start < 0 {
		return value, placeholder, ""
	}
	return head + rest[:start] + "${" + placeholder + "}" + rest[end:], placeholder, rest[start:end]
}

// literalRun returns the bounds of the first run of literal text in v: text
// that is neither a separator (space, `:`, `;`, `,`) nor a well-formed `${…}`,
// up to the next well-formed `${…}`, trailing separators trimmed. In a cookie
// header a leading `<cookie-name>=` is not literal and a `;` ends the run, so
// each cookie's value converts on its own and later cookie names stay visible.
// start is -1 when there is none.
func literalRun(v string, cookie bool) (start, end int) {
	placeholderAt := func(i int) int {
		if !strings.HasPrefix(v[i:], "${") {
			return -1
		}
		e := strings.IndexByte(v[i:], '}')
		if e < 0 || strings.ContainsAny(v[i+2:i+e], "{$") {
			return -1
		}
		return i + e + 1
	}
	start = -1
	for i := 0; i < len(v); {
		if start < 0 {
			if strings.IndexByte(" \t:;,", v[i]) >= 0 {
				i++
				continue
			}
			if e := placeholderAt(i); e >= 0 {
				i = e
				continue
			}
			if loc := cookieNameRe.FindStringIndex(v[i:]); cookie && loc != nil {
				i += loc[1]
				if e := placeholderAt(i); e >= 0 {
					i = e
					continue
				}
				if i == len(v) {
					break
				}
			}
			start = i
		}
		if placeholderAt(i) >= 0 || cookie && v[i] == ';' {
			break
		}
		i++
		end = i
	}
	if start < 0 {
		return -1, -1
	}
	for end > start && strings.IndexByte(" \t:;,", v[end-1]) >= 0 {
		end--
	}
	return start, end
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

// minRedactLen is the shortest value RedactValue replaces in place.
const minRedactLen = 4

// RedactValue returns msg with every occurrence of value replaced by
// RedactMarker. A shorter value would match inside unrelated words (a path, an
// argv, the CLI's own text) and garble the message, so a msg holding one is
// replaced whole by withheld instead.
func RedactValue(msg, value, withheld string) string {
	switch {
	case value == "" || !strings.Contains(msg, value):
		return msg
	case len(value) < minRedactLen:
		return withheld
	}
	return strings.ReplaceAll(msg, value, RedactMarker)
}

// RedactHeader returns the header with its value redacted when it is a
// credential header that breaks the rule; ok reports whether it was redacted.
// An entry that is not a field name and a colon is redacted whole: its "name"
// may hold the credential.
func RedactHeader(h string) (out string, redacted bool) {
	if !WellFormedHeader(h) {
		return RedactMarker, true
	}
	n, v := SplitHeader(h)
	if CheckCredentialValue(n, v) == nil {
		return h, false
	}
	return fmt.Sprintf("%s: %s", n, Redact(v)), true
}
