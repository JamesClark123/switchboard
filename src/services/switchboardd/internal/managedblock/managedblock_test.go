package managedblock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	bA, eA = "<!-- a:begin -->", "<!-- a:end -->"
	bB, eB = "<!-- b:begin -->", "<!-- b:end -->"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteCreatesReplacesAppends(t *testing.T) {
	p := filepath.Join(t.TempDir(), "CLAUDE.md")
	// Absent file: created with the block only.
	if err := Write(p, bA, eA, "one"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != bA+"\none\n"+eA+"\n" {
		t.Errorf("created = %q", got)
	}
	// Replace in place; content outside untouched.
	if err := os.WriteFile(p, []byte("# intro\n\n"+bA+"\nold\n"+eA+"\n\nuser outro\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, bA, eA, "new"); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if !strings.HasPrefix(got, "# intro\n\n") || !strings.HasSuffix(got, "\n\nuser outro\n") || !strings.Contains(got, "\nnew\n") || strings.Contains(got, "old") {
		t.Errorf("replaced = %q", got)
	}
	if strings.Count(got, bA) != 1 {
		t.Errorf("marker count = %d", strings.Count(got, bA))
	}
	// Append after existing user content when no block is present.
	if err := os.WriteFile(p, []byte("just notes\n\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, bA, eA, "x"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "just notes\n\n"+bA+"\nx\n"+eA+"\n" {
		t.Errorf("appended = %q", got)
	}
}

func TestStrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "CLAUDE.md")
	// Missing file is fine.
	if err := Strip(p, bA, eA); err != nil {
		t.Fatal(err)
	}
	// Only the block => file removed.
	if err := Write(p, bA, eA, "x"); err != nil {
		t.Fatal(err)
	}
	if err := Strip(p, bA, eA); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("file holding only the block should be removed")
	}
	// User content survives.
	if err := os.WriteFile(p, []byte("intro\n\n"+bA+"\nx\n"+eA+"\n\noutro\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Strip(p, bA, eA); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "intro\n\noutro\n" {
		t.Errorf("stripped = %q", got)
	}
	// A directory in the file's place is a real error on both paths.
	d := filepath.Join(dir, "dir")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Strip(d, bA, eA); err == nil {
		t.Error("Strip on a directory should fail")
	}
	if err := Write(d, bA, eA, "x"); err == nil {
		t.Error("Write on a directory should fail")
	}
}

func TestTwoMarkerPairsCoexist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := Write(p, bA, eA, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, bB, eB, "beta"); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
		t.Fatalf("both blocks expected: %q", got)
	}
	// Rewriting A leaves B intact; stripping B leaves A intact.
	if err := Write(p, bA, eA, "alpha2"); err != nil {
		t.Fatal(err)
	}
	if err := Strip(p, bB, eB); err != nil {
		t.Fatal(err)
	}
	got = read(t, p)
	if !strings.Contains(got, "alpha2") || strings.Contains(got, "beta") || strings.Contains(got, bB) {
		t.Errorf("after rewrite A + strip B: %q", got)
	}
}

func TestReplaceAndRemovePure(t *testing.T) {
	block := bA + "\nrule\n" + eA
	if got := Replace("plain", bA, eA, block); got != "plain" {
		t.Errorf("Replace without markers should be a no-op, got %q", got)
	}
	if got := Replace("a\n"+bA+"\nold\n"+eA+"\nb", bA, eA, block); got != "a\n"+block+"\nb" {
		t.Errorf("Replace = %q", got)
	}
	cases := map[string]string{
		block:                         "",
		"intro\n\n" + block:           "intro\n",
		block + "\n\noutro":           "outro\n",
		"intro\n\n" + block + "\n\no": "intro\n\no\n",
		"plain content":               "plain content",
		bA + "\nno end":               bA + "\nno end",
	}
	for in, want := range cases {
		if got := Remove(in, bA, eA); got != want {
			t.Errorf("Remove(%q) = %q, want %q", in, got, want)
		}
	}
}
