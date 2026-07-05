// Package resources performs the pre-launch disk/resource check (FR-012f, SC-013):
// it estimates the bytes a verbatim copy will require and compares them against
// the free space on the controlled-workspace filesystem, returning warnings the
// client may override.
package resources

import (
	"fmt"
	"syscall"

	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
)

// Report is the result of a resource check.
type Report struct {
	OK             bool
	RequiredBytes  uint64
	AvailableBytes uint64
	Warnings       []string
}

// safetyMargin keeps a little headroom so a copy that exactly fits still leaves
// the filesystem usable.
const safetyMargin = 1.1

// AvailableBytesFunc returns free bytes for the filesystem backing path. It is a
// package var so tests can stub the syscall.
var AvailableBytesFunc = availableBytes

func availableBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	//nolint:unconvert // Bavail/Bsize types vary by platform.
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// Check estimates the copy size of sources and compares it to free space under
// workspaceRoot. A copy larger than available space (with margin) is flagged with
// a warning and OK=false; the client may still override (FR-012f).
func Check(sources []string, workspaceRoot string) (*Report, error) {
	required, err := duplicate.Size(sources)
	if err != nil {
		return nil, err
	}
	avail, err := AvailableBytesFunc(workspaceRoot)
	if err != nil {
		return nil, err
	}

	rep := &Report{RequiredBytes: required, AvailableBytes: avail, OK: true}
	needed := uint64(float64(required) * safetyMargin)
	if needed > avail {
		rep.OK = false
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"low disk: duplication needs ~%d bytes (with margin) but only %d are free on %s",
			needed, avail, workspaceRoot))
	}
	return rep, nil
}
