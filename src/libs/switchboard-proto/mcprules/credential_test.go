package mcprules

import (
	"strings"
	"testing"
)

func TestIsCredentialHeader(t *testing.T) {
	for name, want := range map[string]bool{
		"Authorization": true, "proxy-authorization": true, "Cookie": true, "X-Api-Key": true,
		"X_Auth_Token": true, "X-Client-Secret": true, "x-credentials": true, "ApiKey": true,
		"Keycloak-Realm": false, "X-Org": false, "X-Auth-Mode": true, "Monkey": false,
		// Review: run-together and dotted names.
		"X-Authorization": true, "Authentication": true, "X-AccessToken": true, "AuthToken": true,
		"X-Api.Key": true, "X-AccessKey": true, "X-ClientSecret": true, "X-Hotkey": false, "X-Tokenizer": false,
	} {
		if got := IsCredentialHeader(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

func TestCheckCredentialValue(t *testing.T) {
	accept := [][2]string{
		{"Authorization", "Bearer ${k}"},
		{"Authorization", "ApiKey ${k}"},
		{"Authorization", "basic ${user}:${pass}"},
		{"Cookie", "session=${session}"},
		{"Cookie", "a=${a}; b=${b}"},
		{"Cookie", "theme=; session=${s}"},
		{"Cookie", "theme= ;a=${a}"},
		{"X-Api-Key", "${key}"},
		{"X-Org", "acme"},
	}
	for _, c := range accept {
		if err := CheckCredentialValue(c[0], c[1]); err != nil {
			t.Errorf("%s: %s: %v", c[0], c[1], err)
		}
	}
	refuse := []struct{ name, value, want string }{
		{"Authorization", "Bearer sk-live-abc ${x}", "text before ${…}"},
		{"Cookie", "session=abc; csrf=${t}", "literal text"},
		{"X-Auth-Mode", "oauth", "the value is literal"},
		{"Authorization", "Bearer sk-live-abc", "text after the scheme"},
		{"Authorization", "Custom ${k}", "put the scheme inside the secret"},
		{"Authorization", "${k} trailing", "text after ${…}"},
		{"Authorization", "Bearer ••••", "literal"},
		{"Authorization", "Bearer ${k", "unterminated"},
		{"X-Api-Key", "abc", "the value is literal"}, {"X-Api-Key", " ; ", "no ${placeholder}"},
	}
	for _, c := range refuse {
		err := CheckCredentialValue(c.name, c.value)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %s: %v, want %q", c.name, c.value, err, c.want)
			continue
		}
		for _, lit := range []string{"sk-live", "abc", "oauth", "trailing"} {
			if strings.Contains(c.value, lit) && strings.Contains(err.Error(), lit) {
				t.Errorf("%s: message quotes the literal: %v", c.value, err)
			}
		}
	}
	if err := CheckCredentialHeaders([]string{"Authorization: Bearer ••••", "X-Org: a"}, map[string]bool{"authorization": true}); err != nil {
		t.Errorf("skip: %v", err)
	}
	if err := CheckCredentialHeaders([]string{"X-Api-Key: abc"}, nil); err == nil {
		t.Error("CheckCredentialHeaders accepted a literal")
	}
}

func TestConvert(t *testing.T) {
	for _, tc := range []struct {
		name, value         string
		used                map[string]bool
		wantValue, wantP, s string
	}{
		{"Authorization", "Bearer sk-live-abc", nil, "Bearer ${authorization}", "authorization", "sk-live-abc"},
		{"X_Api_Key", "abc", nil, "${x-api-key}", "x-api-key", "abc"},
		{"X-Auth-Mode", "oauth", map[string]bool{"x-auth-mode": true}, "${x-auth-mode-2}", "x-auth-mode-2", "oauth"},
		{"X-Auth-Mode", "oauth", map[string]bool{"x-auth-mode": true, "x-auth-mode-2": true}, "${x-auth-mode-3}", "x-auth-mode-3", "oauth"},
		{"Cookie", "session=abc", nil, "session=${cookie-session}", "cookie-session", "abc"},
		{"Cookie", "abc", nil, "${cookie}", "cookie", "abc"},
		{"Client_Secret", "v", nil, "${client-secret}", "client-secret", "v"},
		{"Authorization", "Custom abc", nil, "${authorization}", "authorization", "Custom abc"},
		// Review: existing placeholders stay in the value, never in the secret.
		{"Authorization", "Bearer ${x} tail", nil, "Bearer ${x} ${authorization}", "authorization", "tail"},
		{"Authorization", "Custom ${k}", nil, "${authorization} ${k}", "authorization", "Custom"},
		{"Cookie", "a=${a}; b=lit", nil, "a=${a}; b=${cookie-b}", "cookie-b", "lit"},
		{"Cookie", "session=${cookie-csrf}; csrf=def", map[string]bool{"cookie-csrf": true}, "session=${cookie-csrf}; csrf=${cookie-csrf-2}", "cookie-csrf-2", "def"},
		{"Cookie", "My_Pref=dark", nil, "My_Pref=${cookie-my-pref}", "cookie-my-pref", "dark"},
		{"Authorization", "Bearer abc ${x", nil, "Bearer ${authorization}", "authorization", "abc ${x"},
		// Review round 4: an empty cookie value is not literal text (it used
		// to panic with start > end), and placeholders collapse runs of `-`.
		{"Cookie", "a=; b=x", nil, "a=; b=${cookie-b}", "cookie-b", "x"},
		{"Cookie", "a= ; b=x", nil, "a= ; b=${cookie-b}", "cookie-b", "x"},
		{"Cookie", "a=;", nil, "a=;", "cookie", ""},
		{"Cookie", "theme=; session=${s}", nil, "theme=; session=${s}", "cookie", ""},
		{"Cookie", "__Host-sid=abc", nil, "__Host-sid=${cookie-host-sid}", "cookie-host-sid", "abc"},
		{"Cookie", "a._b=abc", nil, "a._b=${cookie-a.-b}", "cookie-a.-b", "abc"},
		{"X--Key_", "abc", nil, "${x-key}", "x-key", "abc"},
		// Review round 5: a cookie value of blanks then `${…}`, or of
		// separators only (literal text to CheckCredentialValue), does not hide a later
		// literal cookie or leave a refused value Convert cannot change.
		{"Cookie", "a= ${x}; b=lit", nil, "a= ${x}; b=${cookie-b}", "cookie-b", "lit"},
		{"Cookie", "a=\t${x}; session=abc", nil, "a=\t${x}; session=${cookie-session}", "cookie-session", "abc"},
		{"Cookie", "a= ,; b=${b}", nil, "a= ${cookie-a}; b=${b}", "cookie-a", ","},
	} {
		v, p, s := Convert(tc.name, tc.value, tc.used)
		if v != tc.wantValue || p != tc.wantP || s != tc.s {
			t.Errorf("%s %q: got %q %q %q", tc.name, tc.value, v, p, s)
		}
		if err := CheckCredentialValue(tc.name, v); err != nil {
			t.Errorf("converted %q refused: %v", v, err)
		}
	}
	// Review: each cookie's value is its own run; later cookie names stay out
	// of the secret.
	if v, _, sec := Convert("Cookie", "session=abc; csrf=def", nil); v != "session=${cookie-session}; csrf=def" || sec != "abc" {
		t.Errorf("multi-cookie: %q %q", v, sec)
	}
	// Review round 3: LiteralCookie names the cookie the next Convert moves.
	for _, c := range []struct{ name, value, want string }{
		{"Cookie", "session=${s}; csrf=def", "csrf"},
		{"Cookie", "session=abc; csrf=def", "session"},
		{"Cookie", "abc", ""},
		{"Cookie", "a=${a}", ""},
		{"Cookie", "theme=; session=abc", "session"},
		{"Cookie", "theme=", ""},
		{"Authorization", "session=abc", ""},
	} {
		if got := LiteralCookie(c.name, c.value); got != c.want {
			t.Errorf("LiteralCookie(%q, %q) = %q, want %q", c.name, c.value, got, c.want)
		}
	}
	// Two literal runs convert one per call; no secret holds ${…}.
	v, _, sec := Convert("Authorization", "lit1 ${x} lit2", nil)
	if v != "${authorization} ${x} lit2" || sec != "lit1" {
		t.Errorf("first run: %q %q", v, sec)
	}
	v, _, sec = Convert("Authorization", v, map[string]bool{"authorization": true, "x": true})
	if v != "${authorization} ${x} ${authorization-2}" || sec != "lit2" || CheckCredentialValue("Authorization", v) != nil {
		t.Errorf("second run: %q %q", v, sec)
	}
	if v, _, sec := Convert("Authorization", "Bearer ${k}", nil); v != "Bearer ${k}" || sec != "" {
		t.Errorf("no literal: %q %q", v, sec)
	}
	if _, p, _ := Convert("!!", "x", nil); p != "secret" {
		t.Errorf("fallback placeholder %q", p)
	}
}

func TestRedactValue(t *testing.T) {
	const w = "withheld"
	for _, c := range []struct{ msg, value, want string }{
		{"sbx secret set k: refused: sk-live-1", "sk-live-1", "sbx secret set k: refused: " + RedactMarker},
		{"sbx secret set k: refused", "sk-live-1", "sbx secret set k: refused"},
		{"sbx secret set k: refused: x", "x", w}, // short: never garbled into sb•••• …
		{"sbx secret set k: refused", "abc", "sbx secret set k: refused"},
		{"anything", "", "anything"},
	} {
		if got := RedactValue(c.msg, c.value, w); got != c.want {
			t.Errorf("RedactValue(%q, %q) = %q, want %q", c.msg, c.value, got, c.want)
		}
	}
}

func TestRedact(t *testing.T) {
	if Redact("Bearer sk-1") != "Bearer ••••" || Redact("sk-1") != RedactMarker || Redact("Bearer") != RedactMarker {
		t.Fatal("redact")
	}
	if h, ok := RedactHeader("X-Api-Key: abc"); !ok || h != "X-Api-Key: ••••" {
		t.Errorf("RedactHeader: %q %v", h, ok)
	}
	if h, ok := RedactHeader("X-Org: abc"); ok || h != "X-Org: abc" {
		t.Errorf("RedactHeader non-credential: %q %v", h, ok)
	}
	// Review: an entry that is not "token-name: value" is redacted whole.
	for _, h := range []string{"Authorization=Bearer sk-live-SECRET", "Bad Name: sk-live-SECRET"} {
		if out, ok := RedactHeader(h); !ok || out != RedactMarker {
			t.Errorf("RedactHeader(%q) = %q %v", h, out, ok)
		}
	}
}

// Review round 5: every value CheckCredentialValue refuses for literal text
// reaches a passing value by repeated Convert, and no step returns an empty
// secret (a mismatch would loop the TUI's conversion offer).
func TestConvertReachesCheck(t *testing.T) {
	for _, c := range [][2]string{
		{"Authorization", "Bearer sk-live-abc ${x}"},
		{"Cookie", "session=abc; csrf=${t}"},
		{"X-Auth-Mode", "oauth"},
		{"Authorization", "Bearer sk-live-abc"},
		{"Authorization", "Custom ${k}"},
		{"Authorization", "${k} trailing"},
		{"Authorization", "Bearer ••••"},
		{"X-Api-Key", "abc"},
		{"Cookie", "session=abc; csrf=def"},
		{"Cookie", "a= ${x}; b=lit"},
		{"Cookie", "a=\t${x}; b=lit"},
		{"Cookie", "a=,; b=lit"},
		{"Cookie", "theme= ; a=${a}; b=lit; c=; d=lit2"},
		{"Authorization", "lit1 ${x} lit2"},
	} {
		v, used := c[1], map[string]bool{}
		for step := 0; CheckCredentialValue(c[0], v) != nil; step++ {
			if step == 8 {
				t.Errorf("%s %q: still refused after %d converts: %q", c[0], c[1], step, v)
				break
			}
			nv, p, sec := Convert(c[0], v, used)
			if sec == "" || nv == v {
				t.Errorf("%s %q: Convert of refused %q returned no secret", c[0], c[1], v)
				break
			}
			v, used[p] = nv, true
		}
	}
}

func TestReviewRegressions(t *testing.T) {
	if _, err := NormalizeHeaders([]string{"Authorization Bearer sk_live_abc123"}); err == nil || strings.Contains(err.Error(), "sk_live") {
		t.Errorf("no-colon header = %v", err)
	}
	if err := CheckCredentialValue("Authorization", "Bearer ${sk_live{abc}"); err == nil || strings.Contains(err.Error(), "sk_live") {
		t.Errorf("brace inside a placeholder = %v", err)
	}
	if err := CheckCredentialValue("Authorization", "Bearer ${k}"); err != nil {
		t.Errorf("plain placeholder = %v", err)
	}
}
