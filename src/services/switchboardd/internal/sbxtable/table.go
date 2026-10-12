// Package sbxtable parses the aligned, header-led tables the host sandbox CLI
// prints (`sbx mcp ls`, `sbx secret ls`, …): columns are found from the header
// line's offsets, rows are cut at those offsets when they are padded to them
// and split on runs of two spaces otherwise, so a value with single spaces
// ("remote http", "LOCAL, managed by you") survives intact.
package sbxtable

import "strings"

// Column is one header cell: its upper-cased name and start offset.
type Column struct {
	Name  string
	Start int
}

// HeaderColumns splits a header line on runs of two or more spaces, keeping each
// column's start offset. Single spaces inside a name ("URL/COMMAND") are kept.
func HeaderColumns(header string) []Column {
	var cols []Column
	i := 0
	for i < len(header) {
		for i < len(header) && header[i] == ' ' {
			i++
		}
		if i >= len(header) {
			break
		}
		start := i
		for i < len(header) && !columnBreak(header, i) {
			i++
		}
		cols = append(cols, Column{Name: strings.ToUpper(strings.TrimSpace(header[start:i])), Start: start})
	}
	return cols
}

// columnBreak reports whether position i of header starts a gap between column
// names: a space followed by another space, or a space at the very end.
func columnBreak(header string, i int) bool {
	if header[i] != ' ' {
		return false
	}
	return i+1 == len(header) || header[i+1] == ' '
}

// SliceByOffsets cuts a row at the header's column starts when the row is
// padded to them (a space right before each start); nil when it is not.
func SliceByOffsets(l string, cols []Column) []string {
	if len(cols) == 0 {
		return nil
	}
	for _, c := range cols[1:] {
		if c.Start > len(l) || (c.Start > 0 && l[c.Start-1] != ' ') {
			return nil
		}
	}
	vals := make([]string, len(cols))
	for i, c := range cols {
		end := len(l)
		if i+1 < len(cols) {
			end = cols[i+1].Start
		}
		vals[i] = strings.TrimSpace(slice(l, c.Start, end))
	}
	return vals
}

// SplitColumns splits a row on runs of two or more spaces.
func SplitColumns(l string) []string {
	var out []string
	for _, part := range strings.Split(strings.TrimSpace(l), "  ") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Parse finds the header (the first line whose first column isHeader accepts)
// and returns its columns with every following non-empty row's values, aligned
// to the columns (by offset when padded, by double-space split otherwise). A
// missing header yields nil columns and no rows.
func Parse(out string, isHeader func(firstColumn string) bool) (cols []Column, rows [][]string) {
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if cols == nil {
			if c := HeaderColumns(l); len(c) > 0 && isHeader(c[0].Name) {
				cols = c
			}
			continue
		}
		vals := SliceByOffsets(l, cols)
		if vals == nil {
			vals = SplitColumns(l)
		}
		if len(vals) == 0 || strings.TrimSpace(vals[0]) == "" {
			continue
		}
		rows = append(rows, vals)
	}
	return cols, rows
}

// Value returns the cell under the first column whose name is one of names.
func Value(cols []Column, vals []string, names ...string) string {
	for i, c := range cols {
		if i >= len(vals) {
			break
		}
		for _, n := range names {
			if c.Name == n {
				return strings.TrimSpace(vals[i])
			}
		}
	}
	return ""
}

func slice(s string, from, to int) string {
	if from < 0 {
		from = 0
	}
	if from > len(s) {
		return ""
	}
	if to > len(s) || to < from {
		to = len(s)
	}
	return s[from:to]
}
