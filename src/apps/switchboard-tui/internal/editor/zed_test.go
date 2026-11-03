package editor

import "testing"

// TestZedAddress pins contracts/editor-launch.md §1's Zed address (research F5).
func TestZedAddress(t *testing.T) {
	cases := []struct {
		target, path string
		want         string
		wantRule     string
	}{
		{"user@box", "/srv/ws/my proj", "ssh://user@box/srv/ws/my%20proj", ""},
		{"2001:db8::1", "/x", "ssh://[2001:db8::1]/x", ""},
		{"u@[2001:db8::1]:22", "/x", "ssh://u@[2001:db8::1]:22/x", ""},
		{"user@box", "/a/b#c", "ssh://user@box/a/b%23c", ""},
		{"user@box", "/ü", "ssh://user@box/%C3%BC", ""},
		{"user@box", "/a?b", "ssh://user@box/a%3Fb", ""},
		{"user@box", "/a%b", "ssh://user@box/a%25b", ""},
		{"box", "/x", "ssh://box/x", ""},
		{"ssh://user@box", "/x", "ssh://user@box/x", ""},
		{"box:2222", "/x", "ssh://box:2222/x", ""},
		{"a@b@box", "/x", "ssh://a%40b@box/x", ""},
		{"user@box/evil", "/x", "", RuleZedTarget},
		{"user@box?x", "/x", "", RuleZedTarget},
		{"box#x", "/x", "", RuleZedTarget},
		{"", "/x", "", RuleZedTarget},
		{"user@", "/x", "", RuleZedTarget},
		{"ssh://-oX", "/x", "", RuleTargetDash},
		{"-oProxyCommand=x@box", "/x", "", RuleTargetDash},
		{"user@-box", "/x", "", RuleTargetDash},
		{"alice:hunter2@box", "/x", "", RuleZedTarget},
	}
	for _, c := range cases {
		got, ref := ZedAddress(c.target, c.path)
		gotRule := ""
		if ref != nil {
			gotRule = ref.Rule
		}
		if got != c.want || gotRule != c.wantRule {
			t.Errorf("ZedAddress(%q, %q) = %q, %q; want %q, %q", c.target, c.path, got, gotRule, c.want, c.wantRule)
		}
	}
}
