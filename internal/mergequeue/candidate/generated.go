// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"context"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// NotGenerated returns the paths that match none of globs (pipeline.yaml's
// `generated:`, doublestar, repository-relative), in the order given. An
// empty result means every path is generated.
func NotGenerated(paths, globs []string) []string {
	var out []string
	for _, p := range paths {
		if !isGenerated(p, globs) {
			out = append(out, p)
		}
	}
	return out
}

func isGenerated(path string, globs []string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, path); err == nil && ok {
			return true
		}
	}
	return false
}

// GeneratedOnly is the structural rule that lets a regenerated head count as
// the reviewed change with no second review: candidateTree (the regenerated
// result) differs from mPlusHeadTree ("M + the reviewed head") only in paths
// matching globs. offending lists the changed paths that do not, sorted; ok
// is true when there are none. Both trees must be in git's repository.
func GeneratedOnly(ctx context.Context, git gitport.Port, candidateTree, mPlusHeadTree string, globs []string) (ok bool, offending []string, err error) {
	changed, err := localGit{dir: git.Dir(), id: gitport.DefaultIdentity()}.changedPaths(ctx, mPlusHeadTree, candidateTree)
	if err != nil {
		return false, nil, err
	}
	offending = NotGenerated(changed, globs)
	return len(offending) == 0, offending, nil
}
