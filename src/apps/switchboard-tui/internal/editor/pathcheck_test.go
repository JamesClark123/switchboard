package editor

import "testing"

func TestCheckPath(t *testing.T) {
	cases := []struct {
		path       string
		limited    bool
		wantRule   string
		wantDetail string
	}{
		{"/home/josé", true, "", ""},
		{"/a b/c", true, "", ""},
		{"/x:y,z", true, "", ""},
		{"/x+=@_-.9", true, "", ""},
		{"/home/日本語/ü", true, "", ""},
		{"/home/é", true, "", ""}, // a combining mark
		{"/~x", true, RulePathLimited, "'~'"},
		{"/a(b", true, RulePathLimited, "'('"},
		{"/a#b", true, RulePathLimited, "'#'"},
		{"/a$b", true, RulePathLimited, "'$'"},
		{"/a`b", true, RulePathLimited, "'`'"},
		{"/a\u200bb", true, RulePathLimited, "U+200B"},
		{"/a\xffb", true, RulePathLimited, "invalid UTF-8"},
		{"/a\nb", true, RulePathUnsafe, ""},
		{"/a\nb", false, RulePathUnsafe, ""},
		{"/a\tb", false, RulePathUnsafe, ""},
		{"relative/x", false, RulePathUnsafe, ""},
		{"", false, RulePathUnsafe, ""},
		{"/a(b", false, "", ""}, // the limit is off
		{"/a$b", false, "", ""},
	}
	for _, c := range cases {
		ref := checkPath(c.path, c.limited)
		gotRule, gotDetail := "", ""
		if ref != nil {
			gotRule, gotDetail = ref.Rule, ref.Detail
		}
		if gotRule != c.wantRule || gotDetail != c.wantDetail {
			t.Errorf("checkPath(%q, %v) = %q/%q, want %q/%q", c.path, c.limited, gotRule, gotDetail, c.wantRule, c.wantDetail)
		}
	}
}
