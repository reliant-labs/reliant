// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"fmt"
	"strings"
)

// saveCommits keeps every commit only this worktree holds, so it can be
// removed without losing them: each checkout's HEAD when no remote branch has
// it, and the tips of the history only its own reflog, per-worktree refs and
// pseudo-refs reach whose change the base lacks (orphanTips, tipsNotOnBase).
// Each is pointed at by a ref under the checkout's snapshot directory in the
// shared repository,
//
//	refs/reliant/wip/<worktree-id>/<checkout>/orphans/head-<sha>
//	refs/reliant/wip/<worktree-id>/<checkout>/orphans/reflog-<sha>
//
// which outlives the worktree, is recorded on the row, and is never applied
// by a restore (SnapshotRefsOnRow). Refs only: the commits are already in the
// shared object store, so nothing else is written. Names carry the commit, so
// a second pass reuses the refs a first one made.
//
// It returns every ref that now holds this worktree's commits, including on
// error, so what was saved is always recorded.
func saveCommits(ctx context.Context, w Worktree, checkouts []string) ([]string, error) {
	var refs []string
	for _, c := range checkouts {
		dir := snapshotRefDir(w.ID, relTo(w.Path, c)) + "orphans/"
		head, err := git(ctx, c, nil, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return refs, err
		}
		onRemote, err := git(ctx, c, nil, "for-each-ref", "--contains", head, "--count=1", "--format=%(refname)", "refs/remotes")
		if err != nil {
			return refs, err
		}
		if onRemote == "" {
			ref, err := keepRef(ctx, c, dir+"head-"+head, head)
			if err != nil {
				return refs, err
			}
			refs = append(refs, ref)
		}
		tips, err := orphanTips(ctx, c, w.ID)
		if err == nil {
			tips, err = tipsNotOnBase(ctx, c, baseFor(w, c), tips)
		}
		if err != nil {
			return refs, err
		}
		for _, tip := range tips {
			ref, err := keepRef(ctx, c, dir+"reflog-"+tip, tip)
			if err != nil {
				return refs, err
			}
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// keepRef creates ref at commit, or accepts it when it already names commit.
// It never moves an existing ref.
func keepRef(ctx context.Context, checkout, ref, commit string) (string, error) {
	if _, err := git(ctx, checkout, nil, "update-ref", ref, commit, ""); err == nil {
		return ref, nil
	}
	if cur, err := git(ctx, checkout, nil, "rev-parse", "--verify", "--quiet", ref); err == nil && strings.TrimSpace(cur) == commit {
		return ref, nil
	}
	return "", fmt.Errorf("could not save commit %s as %s", short(commit), ref)
}
