package mcprules

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeHeaders(t *testing.T) {
	got, err := NormalizeHeaders([]string{" X-Org :  acme ", "", "Authorization: Bearer ${k}"})
	if err != nil || !reflect.DeepEqual(got, []string{"X-Org: acme", "Authorization: Bearer ${k}"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, tc := range []struct{ in, want string }{
		{"novalue:", "must be \"Name: value\""},
		{"bad name: v", "not a valid HTTP field name"},
		{"X-A: one\nline", "single line"},
		{"X-A: a\x00b", "control character"},
		{"X-B: \x1b[31m", "control character"},
	} {
		if _, err := NormalizeHeaders([]string{tc.in}); err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err %v, want %q", tc.in, err, tc.want)
		}
	}
	if _, err := NormalizeHeaders([]string{"A: 1", "a: 2"}); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("duplicate: %v", err)
	}
	if got, err := NormalizeHeaders([]string{"X-A: a\tb"}); err != nil || got[0] != "X-A: a\tb" {
		t.Errorf("tab: %q %v", got, err)
	}
	// Review: an empty name never quotes the value.
	if _, err := NormalizeHeaders([]string{": Bearer sk-live-SECRET"}); err == nil || strings.Contains(err.Error(), "sk-live") {
		t.Errorf("empty name: %v", err)
	}
	// SC-007: a malformed credential header never quotes its literal.
	if _, err := NormalizeHeaders([]string{"Authorization:"}); err == nil || strings.Contains(err.Error(), "sk-") {
		t.Errorf("credential: %v", err)
	}
}

func TestPlaceholders(t *testing.T) {
	got := Placeholders([]string{"Authorization: Bearer ${api-key}", "X-T: ${a}:${b}", "X-U: ${a}"})
	if !reflect.DeepEqual(got, []string{"api-key", "a", "b"}) {
		t.Fatalf("got %q", got)
	}
}

func TestValidPlaceholder(t *testing.T) {
	for _, ok := range []string{"api-key", "A1", "k.v_x-2"} {
		if err := ValidPlaceholder(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"client_secret", "collide with the OAuth client secret"},
		{"client_secret:identity", "no \":\""},
		{"-x", "start with a letter"},
		{"", "start with a letter"},
		{strings.Repeat("a", 129), "longer than 128"},
	} {
		err := ValidPlaceholder(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.in, err, tc.want)
		}
	}
	if err := CheckPlaceholders([]string{"A: ${ok}", "B: ${client_secret}"}); err == nil {
		t.Error("CheckPlaceholders accepted client_secret")
	}
}

func TestSecretNames(t *testing.T) {
	if SecretName("s", "p") != "mcp:s:p" || ClientSecretName("s") != "mcp:s:client_secret" ||
		IdentityName("s") != "mcp:s:client_secret:identity" || SecretPrefix("s") != "mcp:s:" {
		t.Fatal("secret names")
	}
}

func TestWellFormedHeaderAndValidURL(t *testing.T) {
	for h, want := range map[string]bool{"X-A: b": true, "X-A:": true, "Authorization=Bearer x": false, "Bad Name: x": false, ": x": false} {
		if WellFormedHeader(h) != want {
			t.Errorf("WellFormedHeader(%q) != %v", h, want)
		}
	}
	for _, ok := range []string{"https://x.example/mcp", "HTTP://localhost:8080", "https://u:p@x/y?q=1"} {
		if err := ValidURL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "x.example/mcp", "ftp://x", "https://", "https://x/a b", "https://x/\x00", "-https://x", "%zz"} {
		if err := ValidURL(bad); err == nil || !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", bad)
		}
	}
}
