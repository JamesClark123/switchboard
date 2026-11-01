package mcprules

import (
	"strings"
	"testing"
)

func TestValidClientID(t *testing.T) {
	for _, ok := range []string{"", "abc-123", "a.b@c"} {
		if err := ValidClientID(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"-x", "a b", "a\tb", strings.Repeat("a", 257), "\xff"} {
		if err := ValidClientID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAuthServer(t *testing.T) {
	for s, want := range map[string]bool{
		"/etc/meta.json": true, "https://idp.example": true, "http://localhost:8080/x": true,
		"http://127.0.0.1/x": true, "http://idp.example": false, "http://a.localhost": false,
		"relative/path": false, "https://": false,
	} {
		if got := AuthServerOK(s); got != want {
			t.Errorf("%q: %v", s, got)
		}
	}
	if CheckAuthServer("") != nil || CheckAuthServer("http://x.example") == nil || CheckAuthServer("https://a\x01") == nil {
		t.Error("CheckAuthServer")
	}
}

func TestCredentialLooking(t *testing.T) {
	got := CredentialLooking("https://x/mcp?api_key=1&org=2&sig=3", []string{"--token=abc", "--verbose", "plain", "--client-secret"})
	want := []string{"--client-secret", "--token", "api_key", "sig"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %q", got)
	}
	// Review: URL user info and positional NAME=value arguments.
	got = CredentialLooking("https://user:pw@x/mcp", []string{"GITHUB_TOKEN=ghp_x", "https://y?token=1", "PLAIN=1"})
	want = []string{"GITHUB_TOKEN", URLUserinfo}
	if strings.Join(got, ",") != strings.Join(want, ",") || strings.Contains(strings.Join(got, ","), "ghp_x") {
		t.Errorf("got %q", got)
	}
	if CredentialLooking("", nil) != nil {
		t.Error("empty")
	}
}
