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
		{"Cookie", "session=abc", nil, "session=${cookie}", "cookie", "abc"},
		{"Client_Secret", "v", nil, "${client-secret}", "client-secret", "v"},
		{"Authorization", "Custom abc", nil, "${authorization}", "authorization", "Custom abc"},
	} {
		v, p, s := Convert(tc.name, tc.value, tc.used)
		if v != tc.wantValue || p != tc.wantP || s != tc.s {
			t.Errorf("%s %q: got %q %q %q", tc.name, tc.value, v, p, s)
		}
		if err := CheckCredentialValue(tc.name, v); err != nil {
			t.Errorf("converted %q refused: %v", v, err)
		}
	}
	if _, p, _ := Convert("!!", "x", nil); p != "secret" {
		t.Errorf("fallback placeholder %q", p)
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
}
