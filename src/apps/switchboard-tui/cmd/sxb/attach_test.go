package main

import "testing"

func TestParseAttachArgs(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		wantID  string
		wantH   string
		wantErr bool
	}{
		{"flags", []string{"--sandbox", "sb1", "--host", "h2"}, "sb1", "h2", false},
		{"short flags", []string{"-s", "sb2", "-H", "h3"}, "sb2", "h3", false},
		{"positional id", []string{"sb9"}, "sb9", "", false},
		{"missing id", []string{"--host", "h1"}, "", "h1", true},
		{"empty", nil, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAttachArgs(tc.argv)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got.sandboxID != tc.wantID || got.host != tc.wantH {
				t.Fatalf("got (%q,%q), want (%q,%q)", got.sandboxID, got.host, tc.wantID, tc.wantH)
			}
		})
	}
}
