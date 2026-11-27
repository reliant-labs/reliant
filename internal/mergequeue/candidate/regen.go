// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// detailMax bounds the regen output kept in an ejection's Detail.
const detailMax = 4 << 10

// regenerate settles e's conflict with M, all in generated paths, by running
// regen over M merged with the head, then squashes the result onto tip.
func (b *builder) regenerate(ctx context.Context, tip string, e Entry, conflicts []string) (step, error) {
	// "M + the reviewed head": every non-conflicting change merged, and the
	// conflicting (generated) paths taken from the head, for regen to
	// rewrite.
	x, left, err := b.local.mergeTheirs(ctx, b.m, e.Head)
	if err != nil {
		return step{}, err
	}
	if len(left) > 0 {
		return eject(e, ReasonConflict, left, "regen not attempted: a conflict remains with the head's side taken (a delete, or a file against a directory)"), nil
	}
	r, err := b.runRegen(ctx, x, e.Head)
	var cmdErr *CommandError
	if errors.As(err, &cmdErr) {
		return eject(e, ReasonRegenFailed, conflicts, tail(cmdErr.Output)), nil
	}
	if err != nil {
		return step{}, err
	}
	ok, offending, err := GeneratedOnly(ctx, b.p.Git, r, x, b.cfg.Generated)
	if err != nil {
		return step{}, err
	}
	if !ok {
		return eject(e, ReasonNotGeneratedOnly, offending, "regen changed paths outside `generated:`"), nil
	}

	pr, err := b.p.GitHub.GetPullRequest(ctx, e.Repo, e.Number)
	if err != nil {
		return step{}, err
	}
	if pr.State != ghport.PRStateOpen || pr.Merged {
		return eject(e, ReasonHeadMoved, nil, "the pull request is closed"), nil
	}
	var newHead string
	pushed := false
	if pr.HeadSHA == e.Head {
		msg, err := gitport.FormatMessage(fmt.Sprintf("Merge %s and regenerate generated files", pr.BaseRef), "", b.trailers(e, e.Head))
		if err != nil {
			return step{}, err
		}
		if newHead, err = b.local.commitTree(ctx, r, []string{e.Head, b.m}, msg); err != nil {
			return step{}, err
		}
	} else {
		// A retried build finds the regen commit it pushed before. Its
		// commit time differs from one we would write now, so it is
		// recognised by its shape: the same tree on the same parents.
		same, err := b.isRegenOf(ctx, pr.HeadSHA, r, e.Head)
		if err != nil {
			return step{}, err
		}
		if !same {
			return eject(e, ReasonHeadMoved, nil, fmt.Sprintf("the PR's head is %s, not the queued %s", pr.HeadSHA, e.Head)), nil
		}
		if err := b.ensure(ctx, pr.HeadSHA); err != nil {
			return step{}, err
		}
		newHead, pushed = pr.HeadSHA, true
	}

	res, err := b.squash(ctx, tip, e, newHead)
	var ce *gitport.ConflictError
	switch {
	case errors.As(err, &ce):
		return eject(e, ReasonBatchConflict, ce.Paths, "regenerated against main, but conflicts with an earlier entry of this batch"), nil
	case err != nil:
		return step{}, err
	}
	if !pushed {
		if st, done, err := b.pushRegen(ctx, e, pr.HeadRef, newHead); done || err != nil {
			return st, err
		}
	}
	kept := e
	kept.Head = newHead
	return step{
		entry: kept, commit: res.Commit, tree: res.Tree,
		regen: &Regenerated{Entry: e, NewHead: newHead, Base: b.m, Conflicts: conflicts},
	}, nil
}

// runRegen checks x out (as a commit on M and the head), runs regen in it and
// returns the tree it leaves.
func (b *builder) runRegen(ctx context.Context, x, head string) (string, error) {
	commit, err := b.local.commitTree(ctx, x, []string{b.m, head}, "regen checkout\n")
	if err != nil {
		return "", err
	}
	co, err := b.local.newCheckout(ctx, commit)
	if err != nil {
		return "", err
	}
	defer co.remove(ctx)
	if _, err := b.runner.Run(ctx, co.dir, b.cfg.Regen); err != nil {
		return "", err
	}
	return co.tree(ctx)
}

// isRegenOf reports whether sha is a regen commit of head on M with tree.
func (b *builder) isRegenOf(ctx context.Context, sha, tree, head string) (bool, error) {
	c, err := b.p.GitHub.GetCommit(ctx, b.repo, sha)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", sha, err)
	}
	return c.TreeSHA == tree && slices.Equal(c.Parents, []string{head, b.m}), nil
}

// pushRegen pushes the regen commit to the PR's branch, leased on the queued
// head. done means the entry was ejected instead (its head moved first).
func (b *builder) pushRegen(ctx context.Context, e Entry, branch, newHead string) (st step, done bool, err error) {
	perr := b.p.Git.Push(ctx, b.p.Remote, gitport.PushInput{SHA: newHead, Ref: ghport.BranchRef(branch), ExpectedOld: e.Head})
	if perr == nil {
		return step{}, false, nil
	}
	pr, err := b.p.GitHub.GetPullRequest(ctx, e.Repo, e.Number)
	switch {
	case err != nil:
		return step{}, false, fmt.Errorf("push regen to %s: %w (and reading the PR back: %w)", branch, perr, err)
	case pr.HeadSHA == newHead:
		return step{}, false, nil
	case pr.HeadSHA != e.Head:
		return eject(e, ReasonHeadMoved, nil, fmt.Sprintf("the PR's head moved to %s before the regen commit was pushed", pr.HeadSHA)), true, nil
	}
	return step{}, false, fmt.Errorf("push regen to %s: %w", branch, perr)
}

func tail(s string) string {
	if len(s) <= detailMax {
		return s
	}
	return "…" + s[len(s)-detailMax:]
}
