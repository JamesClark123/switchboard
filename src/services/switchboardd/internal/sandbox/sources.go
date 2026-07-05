// Package sandbox owns sandbox lifecycle on the host: source discovery, verbatim
// duplication orchestration, and launch/stop/restart/destroy via the host sbx CLI.
package sandbox

import (
	"os"
	"path/filepath"
	"sort"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// ListSourceCandidates enumerates immediate child directories of root as launch
// candidates (FR-007). When reposOnly is set, only git repositories are returned.
// is_repo is true when the directory contains a .git entry.
func ListSourceCandidates(root string, reposOnly bool) ([]*pb.SourceRef, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []*pb.SourceRef
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		abs := filepath.Join(root, e.Name())
		isRepo := isGitRepo(abs)
		if reposOnly && !isRepo {
			continue
		}
		out = append(out, &pb.SourceRef{Path: abs, IsRepo: isRepo})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetPath() < out[j].GetPath() })
	return out, nil
}

func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}
