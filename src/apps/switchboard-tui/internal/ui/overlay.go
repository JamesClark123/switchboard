package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// overlayCenter composites the foreground (a modal box) centered over the
// background (the full-screen list page), so the launch form floats as a layer
// with the list still visible around it. Both are multi-line, possibly-styled
// strings; compositing is ANSI-aware so background styling to the sides of the
// modal is preserved.
func overlayCenter(bg, fg string, screenW, screenH int) string {
	fgW := lipgloss.Width(fg)
	fgH := lipgloss.Height(fg)
	dx := (screenW - fgW) / 2
	if dx < 0 {
		dx = 0
	}
	dy := (screenH - fgH) / 2
	if dy < 0 {
		dy = 0
	}
	return overlay(bg, fg, dx, dy, screenH)
}

// overlay splices fg onto bg with its top-left at (dx, dy), padding bg to at
// least minLines rows so a modal taller than the current content isn't clipped.
func overlay(bg, fg string, dx, dy, minLines int) string {
	bgLines := strings.Split(bg, "\n")
	for len(bgLines) < minLines {
		bgLines = append(bgLines, "")
	}
	for i, fgLine := range strings.Split(fg, "\n") {
		y := dy + i
		if y < 0 || y >= len(bgLines) {
			continue
		}
		bgLines[y] = spliceLine(bgLines[y], fgLine, dx)
	}
	return strings.Join(bgLines, "\n")
}

// spliceLine replaces the cells [dx, dx+width(fg)) of bg with fg, keeping the
// background content to the left and right intact.
func spliceLine(bg, fg string, dx int) string {
	left := ansi.Truncate(bg, dx, "")
	if w := ansi.StringWidth(left); w < dx {
		left += strings.Repeat(" ", dx-w)
	}
	right := ""
	if ansi.StringWidth(bg) > dx+ansi.StringWidth(fg) {
		right = ansi.TruncateLeft(bg, dx+ansi.StringWidth(fg), "")
	}
	return left + "\x1b[0m" + fg + "\x1b[0m" + right
}
