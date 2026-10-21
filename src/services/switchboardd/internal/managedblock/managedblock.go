// Package managedblock writes, replaces and strips a marker-delimited block
// inside a text file (typically a workspace CLAUDE.md), preserving everything
// outside the markers. It is the mechanism feature 005's escape-hatch rule
// block proved out, extracted (feature 009, research R12) so the worker
// briefing block can share it without the worker package importing escapehatch.
// Different marker pairs coexist in one file; each pair is managed independently.
package managedblock

import (
	"os"
	"strings"
)

// Replace swaps the block delimited by begin/end in content for block (which
// must itself carry the markers), leaving the surrounding content untouched.
// Content without both markers is returned unchanged.
func Replace(content, begin, end, block string) string {
	b := strings.Index(content, begin)
	e := strings.Index(content, end)
	if b < 0 || e < 0 {
		return content
	}
	e += len(end)
	return content[:b] + block + content[e:]
}

// Remove returns content with the marker-delimited block (and its surrounding
// blank lines) removed. Content without the block is returned unchanged.
func Remove(content, begin, end string) string {
	b := strings.Index(content, begin)
	if b < 0 {
		return content
	}
	e := strings.Index(content, end)
	if e < 0 {
		return content
	}
	e += len(end)
	before := strings.TrimRight(content[:b], "\n")
	after := strings.Trim(content[e:], "\n")
	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after + "\n"
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after + "\n"
	}
}

// Write creates the file when absent, replaces the delimited block when
// present, or appends it after the existing content. body is the block's
// content between the markers (the markers are added here).
func Write(path, begin, end, body string) error {
	return WriteBlock(path, begin, end, begin+"\n"+body+"\n"+end)
}

// WriteBlock is Write for a block that already carries its markers.
func WriteBlock(path, begin, end, block string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(path, []byte(block+"\n"), 0o644)
		}
		return err
	}
	content := string(existing)
	var updated string
	if strings.Contains(content, begin) && strings.Contains(content, end) {
		updated = Replace(content, begin, end, block)
	} else {
		updated = strings.TrimRight(content, "\n") + "\n\n" + block + "\n"
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

// Strip removes the delimited block from the file, leaving user content; a file
// that held only the block is deleted. A missing file is not an error.
func Strip(path, begin, end string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	stripped := Remove(string(existing), begin, end)
	if strings.TrimSpace(stripped) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(stripped), 0o644)
}
