package agent

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	swb "github.com/jamesclark123/switchboard/libs/switchboard-proto"
)

// TestCaptureModelIDFixtures captures the screens of `/model <id>` (SWI-34),
// apart from TestCaptureFixtures so these don't rewrite the others:
//
//	SXB_CAPTURE_CLAUDE=1 go test -run TestCaptureModelIDFixtures -v -timeout 5m ./internal/agent/
//
// model-set-id-* are specific models set by their ID (claude-fable-5, …) and
// their read-back lines; model-not-found-id is an ID Claude Code does not
// know; model-picker-tail is the bare /model picker scrolled to its end, so
// every model it offers is on one screen or the other.
func TestCaptureModelIDFixtures(t *testing.T) {
	if os.Getenv("SXB_CAPTURE_CLAUDE") != "1" {
		t.Skip("set SXB_CAPTURE_CLAUDE=1 to capture the claudeui fixtures from a local claude")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("no claude on PATH: %v", err)
	}
	restore := backupUserSettings(t)
	defer restore()

	c := startCapture(t, bin)
	defer c.stop()
	c.waitScreen("the input box", 60*time.Second, hasBox)
	c.waitHook("SessionStart", 60*time.Second)
	time.Sleep(3 * time.Second) // start-up notices, and a one-off dialog, finish drawing
	if s := c.text(); !hasBox(s) {
		// A one-off start-up dialog ("Make auto mode your default …?"): Esc
		// cancels it and changes nothing.
		t.Logf("start-up dialog dismissed:\n%s", s)
		c.keys("\x1b")
		c.waitScreen("the input box after Esc", 30*time.Second, hasBox)
		time.Sleep(time.Second)
	}

	for _, m := range []struct{ name, cmd, want string }{
		{"model-set-id-fable-5", "/model claude-fable-5", "claude-fable-5\n"},
		{"model-set-id-fable-5-1", "/model claude-fable-5-1", "claude-fable-5-1\n"},
		{"model-set-id-fable-5-2", "/model claude-fable-5-2", "claude-fable-5-2\n"},
		{"model-set-id-haiku-4-5", "/model claude-haiku-4-5", "claude-haiku-4-5\n"},
		{"model-not-found-id", "/model claude-bogus-1", "claude-bogus-1\n"},
	} {
		c.submit(m.cmd)
		// The echo lands on its own row; wait for the result row under it.
		c.waitScreen(m.want, 10*time.Second, func(s string) bool {
			i := strings.LastIndex(s, m.want)
			return i >= 0 && strings.Contains(s[i:], "⎿")
		})
		c.stable()
		c.save(m.name)
		t.Logf("%s:\n%s", m.cmd, c.text())
	}

	// The bare /model picker, with its "More models" row expanded (2.1.326
	// folds the older models under it), then Esc.
	c.submit("/model")
	c.waitScreen("the model picker", 10*time.Second, func(s string) bool { return strings.Contains(s, "Select model") })
	c.settle()
	c.save("model-picker-folded")
	t.Logf("picker:\n%s", c.text())
	for i := 0; i < 12 && !cursorOn(c.text(), "More models"); i++ {
		c.keys("\x1b[B")
		time.Sleep(60 * time.Millisecond)
	}
	if !cursorOn(c.text(), "More models") {
		t.Fatalf("no More models row to expand; screen:\n%s", c.text())
	}
	c.keys("\r")
	c.waitScreen("the expanded picker", 10*time.Second, func(s string) bool { return strings.Contains(s, "Enter to collapse") })
	c.stable()
	c.save("model-picker-more")
	t.Logf("picker, more models:\n%s", c.text())
	rows := pickerRows(c.text())
	more := regexp.MustCompile(`↓ \d+ more`)
	for i := 0; i < 12 && more.MatchString(c.text()); i++ {
		c.keys("\x1b[B")
		time.Sleep(100 * time.Millisecond)
	}
	c.stable()
	c.save("model-picker-more-tail")
	t.Logf("picker, more models, scrolled:\n%s", c.text())
	for _, r := range pickerRows(c.text()) {
		if !slices.Contains(rows, r) {
			rows = append(rows, r)
		}
	}
	t.Logf("picker rows: %q", rows)
	// The catalog is the picker: the same names, in whatever order (the
	// current model's row moves).
	var catalog []string
	for _, m := range swb.Models {
		catalog = append(catalog, m.Display)
	}
	slices.Sort(catalog)
	picker := slices.Clone(rows)
	slices.Sort(picker)
	if !slices.Equal(picker, catalog) {
		t.Errorf("the picker offers %q but the catalog has %q: update switchboard-proto/models.go", picker, catalog)
	}
	c.keys("\x1b")
	c.waitScreen("the input box after Esc", 10*time.Second, hasBox)

	// Every model the picker offers, set by the ID the catalog's rule derives
	// from its display name, reads back as that display name.
	for _, display := range rows {
		id := swb.ModelIDFromDisplay(display)
		c.settle() // a paste right after the last result can be lost
		c.submit("/model " + id)
		var line string
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if line = resultAfter(c.text(), "❯ /model "+id); line != "" {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if !strings.HasPrefix(line, "Set model to "+display+" ") {
			t.Errorf("/model %s (%s) read back %q; screen:\n%s", id, display, line, c.text())
		}
		t.Logf("/model %s → %q", id, line)
	}
}

// stable waits until two screen reads 300 ms apart agree (a repaint finished).
func (c *capture) stable() {
	last := c.text()
	for range 10 {
		time.Sleep(300 * time.Millisecond)
		if s := c.text(); s == last {
			return
		} else {
			last = s
		}
	}
}

// pickerRows lists the "<Family> <version>" names on a picker's numbered rows.
func pickerRows(screen string) []string {
	re := regexp.MustCompile(`^\s*(?:❯\s*)?\d+\.\s+(Opus|Fable|Sonnet|Haiku) (\d+(?:\.\d+)?)\s`)
	var out []string
	for _, row := range strings.Split(screen, "\n") {
		if m := re.FindStringSubmatch(row); m != nil && !slices.Contains(out, m[1]+" "+m[2]) {
			out = append(out, m[1]+" "+m[2])
		}
	}
	return out
}

// resultAfter is the `⎿` row right under the last row equal to echo, "" if none.
func resultAfter(screen, echo string) string {
	rows := strings.Split(screen, "\n")
	for i := len(rows) - 2; i >= 0; i-- {
		if strings.TrimRight(rows[i], " ") == echo {
			if r := strings.TrimSpace(rows[i+1]); strings.HasPrefix(r, "⎿") {
				return strings.TrimSpace(strings.TrimPrefix(r, "⎿"))
			}
			return ""
		}
	}
	return ""
}

// cursorOn reports whether the picker row holding text carries the cursor.
func cursorOn(screen, text string) bool {
	for _, row := range strings.Split(screen, "\n") {
		if strings.Contains(row, text) && strings.HasPrefix(strings.TrimSpace(row), "❯") {
			return true
		}
	}
	return false
}
