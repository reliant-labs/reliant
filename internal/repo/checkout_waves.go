// Copyright (c) 2025 Reliant Labs
package repo

import (
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// CheckoutWaves groups a workspace's repos (by index) into the order their
// checkouts must run in. Every repo in a wave may be checked out
// concurrently; a wave starts only after the one before it finishes.
//
// A repo goes in the wave after the deepest registered repo that contains it.
// That only matters for a root repo ("") registered beside nested ones, or a
// repo registered under another: its checkout lands INSIDE the container's,
// and `git worktree add` will not populate the container into a directory a
// nested checkout already created. Sibling repos — control-plane/, forge/,
// reliant/ — contain none of each other and all land in wave 0.
func CheckoutWaves(repos []*core.Repo) [][]int {
	depth := make([]int, len(repos))
	var depthOf func(i int, seen int) int
	depthOf = func(i int, seen int) int {
		// Containment is a strict order on distinct paths, so a chain is at
		// most len(repos) long. The bound guards duplicate rows, which the
		// unique (project_id, relative_path) constraint should already make
		// impossible.
		if seen > len(repos) {
			return 0
		}
		best := -1
		for j := range repos {
			if j != i && pathContains(repos[j].RelativePath, repos[i].RelativePath) {
				best = max(best, depthOf(j, seen+1))
			}
		}
		return best + 1
	}
	maxDepth := 0
	for i := range repos {
		depth[i] = depthOf(i, 0)
		maxDepth = max(maxDepth, depth[i])
	}
	waves := make([][]int, maxDepth+1)
	for i, d := range depth {
		waves[d] = append(waves[d], i)
	}
	return waves
}

// pathContains reports whether the repo at relative path inner sits strictly
// inside the repo at relative path outer. "" and "." both mean the project
// root, which contains every other repo.
func pathContains(outer, inner string) bool {
	outer, inner = filepath.Clean(outer), filepath.Clean(inner)
	if outer == inner {
		return false
	}
	if outer == "." {
		return true
	}
	return strings.HasPrefix(inner, outer+string(filepath.Separator))
}
