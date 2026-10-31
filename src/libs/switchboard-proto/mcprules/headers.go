package mcprules

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// ErrInvalid marks every rule violation; the daemon maps it to INVALID_ARGUMENT.
var ErrInvalid = errors.New("invalid argument")

// MaxPlaceholder bounds a placeholder name (FR-408).
const MaxPlaceholder = 128

// ClientSecret is the reserved secret name of a confidential OAuth client.
const ClientSecret = "client_secret"

// headerNameRe is an HTTP field name (RFC 9110 token).
var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

// placeholderRe finds `${name}` in a header value.
var placeholderRe = regexp.MustCompile(`\$\{([^{}]*)\}`)

// placeholderNameRe is the placeholder name rule (FR-408): no `:`, so a
// placeholder never addresses the runtime's `:identity` / `:endpoint` records.
var placeholderNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// quoteHeader is the safe way to quote a whole header in a message: a
// credential header's value is redacted.
func quoteHeader(name, value string) string {
	if value == "" {
		// Nothing to redact: a marker would suggest a value was typed.
		return fmt.Sprintf("%q", name)
	}
	if IsCredentialHeader(name) {
		value = Redact(value)
	}
	return fmt.Sprintf("%q", name+": "+value)
}

// NormalizeHeaders validates "Name: value" entries for `--header` (FR-103): a
// token name, a colon, a non-empty value without control characters (RFC 9110
// field-value: a tab is allowed). Blank entries are dropped, and a name given
// twice is refused (the runtime takes each name once).
func NormalizeHeaders(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, h := range in {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		name, value, ok := strings.Cut(h, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		switch {
		case !ok:
			// Without a colon the name cannot be told from a credential value,
			// so nothing of the line is quoted.
			return nil, invalid("a header line has no colon; each header must be \"Name: value\"")
		case name == "":
			// The text after the colon may be a credential: quote nothing.
			return nil, invalid("a header line has no name; each header must be \"Name: value\"")
		case value == "":
			return nil, invalid("header %s must be \"Name: value\"", quoteHeader(name, value))
		case !headerNameRe.MatchString(name):
			return nil, invalid("header name %q is not a valid HTTP field name", name)
		case strings.ContainsAny(value, "\r\n"):
			return nil, invalid("header %q must be a single line", name)
		case strings.IndexFunc(value, func(r rune) bool { return r != '\t' && unicode.IsControl(r) }) >= 0:
			return nil, invalid("header %q holds a control character", name)
		case seen[strings.ToLower(name)]:
			return nil, invalid("header %q is given more than once", name)
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name+": "+value)
	}
	return out, nil
}

// WellFormedHeader reports whether h is an HTTP field name, a colon and a value:
// the shape every header switchboard records has. A runtime-reported entry that
// is not (no colon, or a "name" that is not a token) may carry a credential in
// any part, so it is never shown.
func WellFormedHeader(h string) bool {
	name, _, ok := strings.Cut(h, ":")
	return ok && headerNameRe.MatchString(strings.TrimSpace(name))
}

// ValidURL checks a remote server's URL (FR-103): an absolute http or https URL
// with a host, without spaces or control characters, so a URL the host CLI
// would refuse never reaches a save that has already removed the server.
func ValidURL(raw string) error {
	u, err := url.Parse(raw)
	if raw == "" || err != nil || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
		!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return invalid("the URL must be an http:// or https:// URL with a host")
	}
	return nil
}

// SplitHeader returns a header's trimmed name and value.
func SplitHeader(h string) (name, value string) {
	name, value, _ = strings.Cut(h, ":")
	return strings.TrimSpace(name), strings.TrimSpace(value)
}

// Placeholders returns the distinct placeholder names used in headers, in
// order of first use. Names are returned as written (valid or not).
func Placeholders(headers []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range headers {
		_, value := SplitHeader(h)
		for _, m := range placeholderRe.FindAllStringSubmatch(value, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// ValidPlaceholder applies the placeholder name rule (FR-408).
func ValidPlaceholder(name string) error {
	switch {
	case name == ClientSecret:
		return invalid("placeholder ${%s} is reserved: it would collide with the OAuth client secret mcp:<server>:%s", name, ClientSecret)
	case len(name) > MaxPlaceholder:
		return invalid("placeholder ${%s} is longer than %d characters", name, MaxPlaceholder)
	case !placeholderNameRe.MatchString(name):
		return invalid("placeholder ${%s} must start with a letter or digit and contain only letters, digits, dots, hyphens and underscores (no \":\")", name)
	}
	return nil
}

// CheckPlaceholders applies ValidPlaceholder to every placeholder in headers.
func CheckPlaceholders(headers []string) error {
	for _, p := range Placeholders(headers) {
		if err := ValidPlaceholder(p); err != nil {
			return err
		}
	}
	return nil
}

// SecretName is the host secret a placeholder of a server resolves to.
func SecretName(server, placeholder string) string { return "mcp:" + server + ":" + placeholder }

// ClientSecretName is the confidential client's secret of a server.
func ClientSecretName(server string) string { return SecretName(server, ClientSecret) }

// IdentityName is the runtime-managed identity binding of a server's client.
func IdentityName(server string) string { return ClientSecretName(server) + ":identity" }

// SecretPrefix is the prefix every secret name of a server shares.
func SecretPrefix(server string) string { return "mcp:" + server + ":" }
