package dial_test

import (
	"strings"
	"testing"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
)

func TestClassifyDialStderr(t *testing.T) {
	cases := map[string]dial.DialErrorKind{
		"me@box: Permission denied (publickey,password).":                dial.DialAuthFailed,
		"ssh: connect to host box port 22: Connection refused":           dial.DialUnreachable,
		"ssh: Could not resolve hostname box: Name or service not known": dial.DialUnreachable,
		"bash: sxbd: command not found":                                  dial.DialNoDaemon,
		"something odd happened":                                         dial.DialOther,
	}
	for in, want := range cases {
		if got := dial.ClassifyDialStderr(in); got != want {
			t.Errorf("ClassifyDialStderr(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDialErrorText(t *testing.T) {
	e := &dial.DialError{Kind: dial.DialAuthFailed, Stderr: "noise\nme@box: Permission denied (publickey).\n"}
	if !strings.Contains(e.Error(), "authentication failed") || !strings.Contains(e.Error(), "Permission denied") {
		t.Errorf("Error() = %q", e.Error())
	}
	// No stderr => the kind alone.
	for k, want := range map[dial.DialErrorKind]string{
		dial.DialUnreachable: "host unreachable",
		dial.DialNoDaemon:    "sxbd not found on the host",
		dial.DialOther:       "connection failed",
	} {
		if got := (&dial.DialError{Kind: k}).Error(); got != want {
			t.Errorf("kind %v Error() = %q, want %q", k, got, want)
		}
	}
}
