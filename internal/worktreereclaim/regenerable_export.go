// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"path"
	"strings"
	"time"
)

// The regenerable-output inspection, for a deleter outside this package
// (internal/idlereclaim): the scratch-directory lifecycle and the
// node_modules strip of idle kept worktrees. They decide when; this package
// keeps deciding what a regenerable directory is and what holds one.

// InspectRegenerable inventories the regenerable directory dir (a
// node_modules, a build output, a tool cache) with the shedding inventory's
// holds: a protected descendant (.env, data, secrets, *.sqlite) a package
// manager's install record does not account for, a nested repository, a
// mount, a special file. It returns the bytes the directory holds and the
// newest modification time in it. Any error is a hold: the directory is not
// to be removed. Linux only; elsewhere every directory holds.
func InspectRegenerable(ctx context.Context, dir string) (bytes int64, newest time.Time, err error) {
	tree, err := inspectRegenerableTree(ctx, dir)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer tree.close()
	return tree.bytes, tree.newest, nil
}

// NodeModulesTargets lists the node_modules directories of checkout, relative
// to it with forward slashes, outermost first, that git ignores (so none is
// tracked) and the regenerable allow-list accepts. A node_modules inside
// another regenerable directory is not listed on its own: the outer one is.
func NodeModulesTargets(ctx context.Context, checkout string) []string {
	var out []string
	for _, rel := range regenerableTargets(ctx, checkout) {
		rel = strings.TrimSuffix(rel, "/")
		if path.Base(rel) == "node_modules" {
			out = append(out, rel)
		}
	}
	return out
}
