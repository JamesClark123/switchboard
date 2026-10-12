package sbxtable

import (
	"strings"
	"testing"
)

func TestParseAlignedAndReflowedRows(t *testing.T) {
	out := "Some banner\n" +
		"SCOPE      TYPE      NAME       SECRET\n" +
		"(global)   service   github     gho_****\n" +
		"my-sb  service  anthropic  sk-****\n" + // narrower than the header: double-space split
		"\n"
	cols, rows := Parse(out, func(first string) bool { return first == "SCOPE" })
	if len(cols) != 4 || cols[0].Name != "SCOPE" || cols[3].Name != "SECRET" || cols[1].Start != 11 {
		t.Fatalf("cols = %+v", cols)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if Value(cols, rows[0], "NAME", "SERVICE") != "github" || Value(cols, rows[0], "SECRET") != "gho_****" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if Value(cols, rows[1], "SCOPE") != "my-sb" || Value(cols, rows[1], "NAME") != "anthropic" {
		t.Errorf("row 1 = %v", rows[1])
	}
	if Value(cols, rows[1], "MISSING") != "" || Value(cols, rows[1][:1], "NAME") != "" {
		t.Error("absent columns and short rows yield empty values")
	}
}

func TestParseKeepsSingleSpacesInsideCells(t *testing.T) {
	out := "NAME     TRANSPORT     STATUS       GATEWAY                 DETAIL\n" +
		"notion   remote http   needs auth   LOCAL, managed by you   sbx mcp auth notion\n"
	cols, rows := Parse(out, func(first string) bool { return first == "NAME" })
	if len(rows) != 1 || Value(cols, rows[0], "TRANSPORT") != "remote http" || Value(cols, rows[0], "GATEWAY") != "LOCAL, managed by you" || Value(cols, rows[0], "DETAIL") != "sbx mcp auth notion" {
		t.Errorf("rows = %v", rows)
	}
}

func TestParseWithoutHeader(t *testing.T) {
	cols, rows := Parse("No secrets stored.\n", func(string) bool { return false })
	if cols != nil || rows != nil {
		t.Errorf("no header: cols %v rows %v", cols, rows)
	}
	if got := HeaderColumns("   "); got != nil {
		t.Errorf("blank header = %v", got)
	}
	if got := SliceByOffsets("x", nil); got != nil {
		t.Errorf("no columns = %v", got)
	}
	if got := strings.Join(SplitColumns("  a  b   c "), "|"); got != "a|b|c" {
		t.Errorf("split = %q", got)
	}
	if slice("abc", 5, 9) != "" || slice("abc", -1, 2) != "ab" || slice("abc", 1, 0) != "bc" {
		t.Error("slice bounds")
	}
}
