package cigate

import (
	"context"
	"fmt"
	"strings"
)

// BranchRange is the commits a checkout adds over the branch it merges into,
// as a `git log` revision range, with a sentence saying how it was chosen. It
// takes its base the way affected-package mode does (ComputeAffected): baseRef
// (CI_BASE / --base) when set, else origin/<default branch>.
//
//   - on a branch, or any checkout with an explicit base (a pull request's or
//     merge group's merge commit): `<merge-base>..HEAD`, the merge-base of HEAD
//     and the base;
//   - on the default branch with no explicit base, or a checkout that adds no
//     commit over its base: `HEAD^..HEAD`, the commit just made — every
//     earlier one was scanned on the branch it merged from;
//   - a clone with no origin/<default branch> and no explicit base: "" (every
//     commit) — nothing was ever pushed, so every commit is one it adds;
//   - a first commit: "" (the whole history, which is that commit).
//
// It fails closed: a base that does not resolve, or has no merge-base with
// HEAD (a shallow clone, an unrelated ref), is an error, never a silent
// narrowing to the last commit, which would leave a branch's earlier commits
// unscanned.
//
// `forge ci secret-scan --branch` scans this range, so the gate's secret-scan
// row judges only what the change adds: a project that pins the forge that
// added the row is never failed by it over a credential committed years ago.
func BranchRange(ctx context.Context, root, baseRef string) (rangeSpec, why string, err error) {
	return branchRange(ctx, runGit, root, baseRef)
}

func branchRange(ctx context.Context, git func(ctx context.Context, dir string, args ...string) (string, error), root, baseRef string) (rangeSpec, why string, err error) {
	if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD^"); err != nil {
		return "", "the first commit: the whole history", nil
	}
	defaultBranch := ""
	if baseRef == "" {
		baseRef = defaultBaseRef(ctx, git, root)
		if baseRef == "" {
			return "", "every commit (no origin/<default branch> and no " + BaseEnv + ": nothing was pushed, so every commit is new)", nil
		}
		defaultBranch = strings.TrimPrefix(baseRef, "origin/")
	}
	if branch, err := git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && defaultBranch != "" && branch == defaultBranch {
		return "HEAD^..HEAD", "the last commit (on the default branch " + branch + ")", nil
	}
	base, err := git(ctx, root, "merge-base", "HEAD", baseRef)
	if err != nil || base == "" {
		return "", "", fmt.Errorf("cannot find the merge-base of HEAD and %s, so the commits this change adds are unknown "+
			"(a shallow clone, or a base that does not exist here): fetch the full history (actions/checkout fetch-depth: 0) "+
			"and the base, or set %s to the ref this change merges into", baseRef, BaseEnv)
	}
	head, err := git(ctx, root, "rev-parse", "HEAD")
	if err == nil && head == base {
		return "HEAD^..HEAD", "the last commit (HEAD adds no commit over " + baseRef + ")", nil
	}
	short := base
	if len(short) > 12 {
		short = short[:12]
	}
	return base + "..HEAD", "the commits since " + baseRef + " (" + short + ")", nil
}
