package sbxkit

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"sbx version 0.46.0", "0.46.0", true},
		{"sbx 0.36.0 (abc123)\n", "0.36.0", true},
		{"sbx-e2e 0.0", "", false},
		{"", "", false},
		{"garbage", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseVersion(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseVersion(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		have, min string
		want      bool
	}{
		{"sbx version 0.46.0", MinSbxVersion, true},
		{"0.36.0", MinSbxVersion, true},
		{"0.35.9", MinSbxVersion, false},
		{"1.0.0", MinSbxVersion, true},
		{"0.36.1", "0.36.10", false},
		{"sbx-e2e 0.0", MinSbxVersion, false},
		{"0.46.0", "", false},
	}
	for _, tc := range cases {
		if got := AtLeast(tc.have, tc.min); got != tc.want {
			t.Errorf("AtLeast(%q, %q) = %v, want %v", tc.have, tc.min, got, tc.want)
		}
	}
}
