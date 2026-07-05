// Package duplicate performs verbatim duplication of selected source directories
// into the daemon-controlled workspace folder (FR-010/010a). Every file is copied
// exactly — including dependency folders, build artifacts, and untracked files —
// so the seeded tree is byte-identical to the source (SC-002). Sources are read
// only; nothing is ever written outside the destination root.
package duplicate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Progress reports duplication progress for FR-028 (so a launch is never
// indistinguishable from a hang).
type Progress struct {
	BytesCopied uint64
	BytesTotal  uint64
	CurrentPath string
}

// ProgressFunc receives Progress updates as the copy streams. It MAY be nil.
type ProgressFunc func(Progress)

// Size walks the given sources read-only and returns the total byte count of
// regular files. Used both to pre-size the copy (CheckResources, FR-012f) and to
// drive the progress denominator.
func Size(sources []string) (uint64, error) {
	var total uint64
	for _, src := range sources {
		err := filepath.Walk(src, func(_ string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				total += uint64(info.Size())
			}
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("sizing %s: %w", src, err)
		}
	}
	return total, nil
}

// CopyAll duplicates each source into destRoot/<basename(source)>. It returns the
// number of bytes copied. The destination root is created if absent; sources are
// opened read-only. A streaming walk reports progress via onProgress.
func CopyAll(sources []string, destRoot string, onProgress ProgressFunc) (uint64, error) {
	if len(sources) == 0 {
		return 0, errors.New("no sources selected")
	}
	total, err := Size(sources)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return 0, fmt.Errorf("create dest root: %w", err)
	}

	var copied uint64
	report := func(path string) {
		if onProgress != nil {
			onProgress(Progress{BytesCopied: copied, BytesTotal: total, CurrentPath: path})
		}
	}

	seen := map[string]bool{}
	for _, src := range sources {
		base := filepath.Base(filepath.Clean(src))
		if base == "." || base == string(os.PathSeparator) || base == "" {
			return copied, fmt.Errorf("invalid source path %q", src)
		}
		if seen[base] {
			return copied, fmt.Errorf("duplicate source basename %q; rename or select distinct directories", base)
		}
		seen[base] = true

		dest := filepath.Join(destRoot, base)
		n, err := copyTree(src, dest, &copied, report)
		_ = n
		if err != nil {
			return copied, err
		}
	}
	report("")
	return copied, nil
}

// copyTree recursively copies src to dest, preserving mode bits and copying
// symlinks as-is (research R5 defaults). It updates *copied and reports progress.
func copyTree(src, dest string, copied *uint64, report func(string)) (uint64, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return 0, err
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return 0, err
		}
		return 0, os.Symlink(target, dest)

	case info.IsDir():
		if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
			return 0, err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return 0, err
		}
		for _, e := range entries {
			if _, err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dest, e.Name()), copied, report); err != nil {
				return 0, err
			}
		}
		return 0, nil

	case info.Mode().IsRegular():
		return copyFile(src, dest, info, copied, report)

	default:
		// Skip sockets/devices/fifos — they cannot be meaningfully duplicated.
		return 0, nil
	}
}

func copyFile(src, dest string, info os.FileInfo, copied *uint64, report func(string)) (uint64, error) {
	report(src)
	in, err := os.Open(src) // read-only — originals are never modified (SC-002)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return 0, err
	}

	buf := make([]byte, 1<<20)
	var n uint64
	for {
		r, rerr := in.Read(buf)
		if r > 0 {
			if _, werr := out.Write(buf[:r]); werr != nil {
				_ = out.Close()
				return n, werr
			}
			n += uint64(r)
			*copied += uint64(r)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = out.Close()
			return n, rerr
		}
	}
	if err := out.Close(); err != nil {
		return n, err
	}
	return n, nil
}
