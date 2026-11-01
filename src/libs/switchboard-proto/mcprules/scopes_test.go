package mcprules

import (
	"strings"
	"testing"
)

func TestValidScopes(t *testing.T) {
	if err := ValidScopes([]string{"read", "write:all", "https://x/y"}); err != nil {
		t.Fatal(err)
	}
	many := make([]string, MaxScopes+1)
	for i := range many {
		many[i] = "s" + strings.Repeat("x", i)
	}
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"a,b"}, "comma"},
		{[]string{"-x"}, "start with"},
		{[]string{"a\"b"}, "printable"},
		{[]string{"a\\b"}, "printable"},
		{[]string{"é"}, "printable"},
		{[]string{"a", "a"}, "more than once"},
		{[]string{""}, "empty"},
		{[]string{strings.Repeat("a", 257)}, "longer than"},
		{many, "at most 32"},
	} {
		if err := ValidScopes(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.in, err, tc.want)
		}
	}
	if got := ParseScopes("  read  write "); len(got) != 2 || got[1] != "write" {
		t.Errorf("ParseScopes %q", got)
	}
}
