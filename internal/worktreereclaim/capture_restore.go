// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RestoreOptions names a capture and where to put it back.
type RestoreOptions struct {
	// Repo is the capture's repository: its main checkout or its git dir.
	Repo string
	// Ref is the capture ref (refs/reliant/reclaimed/...).
	Ref string
	// Dest is the directory to restore into. It must not exist. Empty means
	// the path the capture was taken of.
	Dest string
	// Branch is the branch to put the restored checkout on. Empty means the
	// branch it was on; see RestoreResult.Branch for what happened.
	Branch string
}

// RestoreResult is what RestoreCapture did.
type RestoreResult struct {
	Path string `json:"path"`
	Head string `json:"head"`
	// Branch is the branch the checkout is on, or "" when it was left on a
	// detached HEAD; BranchNote says why.
	Branch     string   `json:"branch,omitempty"`
	BranchNote string   `json:"branch_note,omitempty"`
	Orphans    []string `json:"orphans,omitempty"`
}

// RestoreCapture recreates a reclaimed checkout from its capture as a new
// linked worktree: HEAD at the captured commit, the files on disk as they
// were (untracked files included, and untracked again), and the index as it
// was staged. Commits only the old checkout's reflog reached stay reachable
// from the capture ref, which is kept; Orphans names them.
//
// The branch is reattached only when that changes no file and loses nothing:
// it exists at the captured commit and no other checkout has it, or it no
// longer exists and is created there. Otherwise the checkout stays on a
// detached HEAD and BranchNote says why.
func RestoreCapture(ctx context.Context, opts RestoreOptions) (RestoreResult, error) {
	info, err := ReadCapture(ctx, opts.Repo, opts.Ref)
	if err != nil {
		return RestoreResult{}, err
	}
	dest := opts.Dest
	if dest == "" {
		dest = info.Meta.Path
	}
	if dest == "" {
		return RestoreResult{}, errors.New("the capture records no path; name one with --to")
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return RestoreResult{}, err
	}
	if _, err := os.Lstat(dest); err == nil {
		return RestoreResult{}, fmt.Errorf("%s already exists; restore somewhere else with --to", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return RestoreResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return RestoreResult{}, err
	}
	dir, pre := gitDirArgs(opts.Repo)
	if _, err := git(ctx, dir, nil, append(pre, "worktree", "add", "--detach", "--no-checkout", dest, info.Head)...); err != nil {
		return RestoreResult{}, fmt.Errorf("could not create the checkout: %w", err)
	}
	res := RestoreResult{Path: dest, Head: info.Head, Orphans: info.Orphans}
	if err := applyCaptureTrees(ctx, dest, info); err != nil {
		return res, err
	}
	res.Branch, res.BranchNote = reattachBranch(ctx, dest, firstNonEmpty(opts.Branch, info.Meta.Branch), info.Head)
	return res, nil
}

// ApplyCapture puts a capture back into checkout, a fresh clean checkout of
// the captured commit (worktree.ensure recreates a reclaimed Reliant
// workspace from its branch, then calls this). It returns ErrBranchMoved,
// touching nothing, when HEAD is not the captured commit. On success the
// capture is moved to refs/reliant/restored/, never deleted.
func ApplyCapture(ctx context.Context, checkout string, info CaptureInfo) error {
	if out, err := git(ctx, checkout, nil, "status", "--porcelain", "--untracked-files=all"); err != nil || out != "" {
		return errors.New("checkout is not clean; not applying a capture over it")
	}
	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if head != info.Head {
		return fmt.Errorf("%w: it was captured on %s and HEAD is now %s; the work is kept at %s", ErrBranchMoved, short(info.Head), short(head), info.Ref)
	}
	if err := applyCaptureTrees(ctx, checkout, info); err != nil {
		// Back to HEAD's clean state, so a retry is possible.
		_, _ = git(ctx, checkout, nil, "read-tree", "--reset", "-u", "HEAD")
		_, _ = git(ctx, checkout, nil, "clean", "-fdq")
		return err
	}
	return retireCapture(ctx, checkout, info.Ref)
}

// applyCaptureTrees writes the capture's files into checkout (whose HEAD is
// the captured commit) and then its staged index, and proves both.
func applyCaptureTrees(ctx context.Context, checkout string, info CaptureInfo) error {
	// Files: the index and working tree both become the disk tree...
	if _, err := git(ctx, checkout, nil, "read-tree", "--reset", "-u", info.DiskTree); err != nil {
		return fmt.Errorf("writing the captured files: %w", err)
	}
	tmp, err := os.MkdirTemp("", "reliant-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if now, err := workTree(ctx, checkout, tmp, nil); err != nil || now != info.DiskTree {
		return fmt.Errorf("restored files do not match the capture (tree %s, want %s): %v", short(now), short(info.DiskTree), err)
	}
	// ...then the index alone becomes the staged tree: what was unstaged is
	// unstaged again, and what was untracked is untracked again.
	if _, err := git(ctx, checkout, nil, "read-tree", info.IndexTree); err != nil {
		return fmt.Errorf("restoring the staged state: %w", err)
	}
	if staged, err := git(ctx, checkout, nil, "write-tree"); err != nil || staged != info.IndexTree {
		return fmt.Errorf("restored index does not match the capture: %v", err)
	}
	// read-tree without -u drops the stat cache; refresh it so `git status`
	// is not slow the first time. Content is not touched.
	_, _ = git(ctx, checkout, nil, "update-index", "-q", "--refresh")
	return nil
}

// reattachBranch puts checkout (on a detached HEAD at head) onto branch when
// that loses nothing; see RestoreCapture.
func reattachBranch(ctx context.Context, checkout, branch, head string) (string, string) {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if branch == "" {
		return "", "the capture was taken on a detached HEAD"
	}
	ref := "refs/heads/" + branch
	cur, err := git(ctx, checkout, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	switch {
	case err != nil || cur == "":
		if _, err := git(ctx, checkout, nil, "update-ref", ref, head, ""); err != nil {
			return "", fmt.Sprintf("could not recreate branch %s: %v", branch, err)
		}
	case cur != head:
		return "", fmt.Sprintf("branch %s has moved to %s since the capture; left on a detached HEAD (git switch -c %s-restored)", branch, short(cur), branch)
	default:
		if other := checkedOutElsewhere(ctx, checkout, ref); other != "" {
			return "", fmt.Sprintf("branch %s is checked out at %s; left on a detached HEAD", branch, other)
		}
	}
	// symbolic-ref changes which ref HEAD names and nothing else: the branch
	// points at the commit HEAD already is, so no file or index entry moves.
	if _, err := git(ctx, checkout, nil, "symbolic-ref", "HEAD", ref); err != nil {
		return "", fmt.Sprintf("could not attach branch %s: %v", branch, err)
	}
	return branch, ""
}

// checkedOutElsewhere names another checkout that has ref checked out.
func checkedOutElsewhere(ctx context.Context, checkout, ref string) string {
	out, err := git(ctx, checkout, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	for _, e := range parseWorktreeList(out) {
		if e.Branch == ref && !samePath(e.Path, checkout) {
			return e.Path
		}
	}
	return ""
}

// retireCapture moves a restored capture out of the reclaimed namespace.
func retireCapture(ctx context.Context, repo, ref string) error {
	sha, err := git(ctx, repo, nil, "rev-parse", "--verify", ref)
	if err != nil {
		return err
	}
	dest := RestoredCaptureRefRoot + strings.TrimPrefix(ref, CaptureRefRoot)
	if _, err := git(ctx, repo, nil, "update-ref", dest, sha, ""); err != nil {
		return err
	}
	_, err = git(ctx, repo, nil, "update-ref", "-d", ref, sha)
	return err
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
