package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// The claudeui fixtures (SWI-17, research R1/R2) live in testdata/claudeui as
// two files per screen:
//   - <name>.txt: the screen's rows as plain text, one per line, trailing blanks
//     trimmed (termview.Line.Text);
//   - <name>.faint: the faint-attribute sidecar, one line per row, with `~`
//     under each faint rune and a space under each other rune, trailing spaces
//     trimmed (a row with no faint text is an empty line).
const fixtureDir = "testdata/claudeui"

// The texts the capture helper left in the box, unsubmitted.
const (
	fixtureTextLeft = "please summarise the open pull requests"
	// fixtureFramedPrompt is a framed action prompt as delivered: a multi-line
	// paste, which the box shows as a placeholder.
	fixtureFramedPrompt = "[switchboard orchestrator \"pdev-orch\" · run run-1a2b3c4d-7 · action \"review-pr\"]\n\n" +
		"Review pull request 412 in acme/web.\n\nfocus: tests\n\n" +
		"When you are done, call orchestrator_report_completion with a short summary."
	// fixtureLongPrompt is a one-line custom prompt that wraps over three rows.
	fixtureLongPrompt = "Before you start, read CONTRIBUTING.md and the package README in full, then list every " +
		"exported function in the package that has no test, and for each one write a table-driven test " +
		"that covers its error paths as well as its happy path, keeping the existing naming conventions."
)

// encodeFixture renders a screen snapshot as the two fixture files' contents.
func encodeFixture(lines []termview.Line) (text, faint string) {
	var t, f strings.Builder
	for _, l := range lines {
		t.WriteString(l.Text)
		t.WriteByte('\n')
		var row strings.Builder
		for _, on := range l.Faint {
			if on {
				row.WriteByte('~')
			} else {
				row.WriteByte(' ')
			}
		}
		f.WriteString(strings.TrimRight(row.String(), " "))
		f.WriteByte('\n')
	}
	return t.String(), f.String()
}

// decodeFixture is encodeFixture's inverse.
func decodeFixture(text, faint string) []termview.Line {
	rows := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	marks := strings.Split(strings.TrimSuffix(faint, "\n"), "\n")
	out := make([]termview.Line, len(rows))
	for i, r := range rows {
		n := len([]rune(r))
		mask := make([]bool, n)
		if i < len(marks) {
			for j, c := range []rune(marks[i]) {
				if j < n && c == '~' {
					mask[j] = true
				}
			}
		}
		out[i] = termview.Line{Text: r, Faint: mask}
	}
	return out
}

// loadFixture reads one fixture screen.
func loadFixture(t *testing.T, name string) []termview.Line {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(fixtureDir, name+".txt"))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	faint, err := os.ReadFile(filepath.Join(fixtureDir, name+".faint"))
	if err != nil {
		t.Fatalf("fixture %s sidecar: %v", name, err)
	}
	return decodeFixture(string(text), string(faint))
}

// saveFixture writes one fixture screen (the capture helper).
func saveFixture(t *testing.T, name string, lines []termview.Line) {
	t.Helper()
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	text, faint := encodeFixture(lines)
	if err := os.WriteFile(filepath.Join(fixtureDir, name+".txt"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, name+".faint"), []byte(faint), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFixtureEncodingRoundTrips(t *testing.T) {
	s := termview.New(20, 3, termview.WithoutScrollback())
	_, _ = s.Write([]byte("❯ \x1b[2mTry it\x1b[0m\r\n\r\nplain"))
	want := s.Lines()
	got := decodeFixture(encodeFixture(want))
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Text != want[i].Text || len(got[i].Faint) != len(want[i].Faint) {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
		for j := range want[i].Faint {
			if got[i].Faint[j] != want[i].Faint[j] {
				t.Fatalf("row %d rune %d faint = %v, want %v", i, j, got[i].Faint[j], want[i].Faint[j])
			}
		}
	}
}
