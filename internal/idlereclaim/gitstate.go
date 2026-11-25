// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// gitWall bounds one git command.
const gitWall = 30 * time.Second

// git runs one git command in dir and returns its trimmed stdout. None of
// these commands reaches a remote, and each output read is git's porcelain or
// an object id, so the inherited environment is used as it is.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitWall)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// adminDir is a linked worktree's git admin directory (<common>/worktrees/<name>),
// from its .git file, or "".
func adminDir(checkout string) string {
	b, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	dir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if dir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(checkout, dir)
	}
	return filepath.Clean(dir)
}

// commonDir is the git common dir of a linked worktree (its repository's
// .git), or "".
func commonDir(checkout string) string {
	admin := adminDir(checkout)
	if admin == "" {
		return ""
	}
	common := filepath.Dir(filepath.Dir(admin))
	if c, err := os.ReadFile(filepath.Join(admin, "commondir")); err == nil {
		common = strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(admin, common)
		}
	}
	return filepath.Clean(common)
}

// dirty reports whether a working tree has a change git would show: a
// modified, staged, deleted or untracked file, a submodule change. An error
// is reported as dirty.
func dirty(ctx context.Context, root string) (bool, error) {
	out, err := git(ctx, root, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return true, err
	}
	return out != "", nil
}

// locked reports whether a linked worktree is locked (`git worktree lock`):
// someone, Reliant's own sweep included, asked for it to be kept.
func locked(checkout string) bool {
	admin := adminDir(checkout)
	if admin == "" {
		return true // cannot tell: treat as kept
	}
	_, err := os.Lstat(filepath.Join(admin, "locked"))
	return err == nil
}

// revListAny reports whether `git rev-list -n1 <args>` names any commit. An
// error is reported as yes.
func revListAny(ctx context.Context, root string, args ...string) (bool, error) {
	out, err := git(ctx, root, append([]string{"rev-list", "-n1"}, args...)...)
	if err != nil {
		return true, err
	}
	return out != "", nil
}

// hasHEAD reports whether the working tree has a commit checked out.
func hasHEAD(ctx context.Context, root string) bool {
	_, err := git(ctx, root, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	return err == nil
}

// headOnRemote reports whether a working tree's HEAD is reachable from a
// remote-tracking ref: everything committed there is pushed.
func headOnRemote(ctx context.Context, root string) bool {
	if !hasHEAD(ctx, root) {
		return false
	}
	unpushed, err := revListAny(ctx, root, "HEAD", "--not", "--remotes")
	return err == nil && !unpushed
}

// ignoredEntries lists what git ignores in a working tree, relative to it
// (a wholly ignored directory as one entry). An error is returned as such.
func ignoredEntries(ctx context.Context, root string) ([]string, error) {
	out, err := git(ctx, root, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// untrackedOutput rejects even force-added descendants of ignored directories.
func untrackedOutput(ctx context.Context, root, target string) bool {
	if !gitIgnored(ctx, root, target) {
		return false
	}
	out, err := git(ctx, root, "ls-files", "--cached", "-z", "--", ":(literal)"+relOf(root, target))
	return err == nil && out == ""
}

// gitIgnored reports whether git ignores p inside the working tree root.
func gitIgnored(ctx context.Context, root, p string) bool {
	_, err := git(ctx, root, "check-ignore", "-q", "--", relOf(root, p))
	return err == nil
}

// worktreePaths lists the working trees of the repository whose git dir is
// at gitDir (the main one first).
func worktreePaths(ctx context.Context, gitDir string) ([]string, error) {
	out, err := git(ctx, gitDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, filepath.Clean(p))
		}
	}
	return paths, nil
}

// uniqueWork is why removing the git working tree root, found inside dir,
// would lose work that exists nowhere else, or "" when it would not. Every
// error is a reason: what cannot be checked is kept.
//
//   - Any change git would show (dirty).
//   - A linked worktree that is locked, or whose HEAD has commits no branch,
//     tag or remote-tracking ref reaches. Its branches' commits live in its
//     repository, which is not removed with it unless it is inside dir too,
//     and then it is checked as a repository.
//   - A repository with a commit (any ref: branches, tags, stash, notes,
//     Reliant's reclaim captures, HEAD) no remote-tracking ref reaches, or
//     with a working tree outside dir that removing it would orphan.
func uniqueWork(ctx context.Context, dir string, r gitRoot) string {
	rel := relOf(dir, r.Path)
	if d, err := dirty(ctx, r.Path); err != nil {
		return fmt.Sprintf("cannot read the git state of %s", rel)
	} else if d {
		return fmt.Sprintf("%s has uncommitted changes", rel)
	}
	if r.Linked {
		if locked(r.Path) {
			return fmt.Sprintf("%s is a locked git worktree", rel)
		}
		if hasHEAD(ctx, r.Path) {
			if detached, err := revListAny(ctx, r.Path, "HEAD", "--not", "--branches", "--tags", "--remotes"); err != nil || detached {
				return fmt.Sprintf("%s has commits on no branch, tag or remote", rel)
			}
		}
		return ""
	}
	if local, err := revListAny(ctx, r.Path, "--all", "--not", "--remotes"); err != nil {
		return fmt.Sprintf("cannot read the history of %s", rel)
	} else if local {
		return fmt.Sprintf("%s has commits that are on no remote", rel)
	}
	paths, err := worktreePaths(ctx, filepath.Join(r.Path, ".git"))
	if err != nil {
		return fmt.Sprintf("cannot list the worktrees of %s", rel)
	}
	for _, p := range paths {
		if !within(dir, p) {
			return fmt.Sprintf("%s has a worktree outside this directory, at %s", rel, p)
		}
	}
	return ""
}

// removableWorktree is why the linked worktree wt is not one the scratch
// strip may remove, or "": it must be unlocked, clean, with its HEAD on a
// remote and nothing git ignores in it but the regenerable output the strip
// took (stripped, absolute paths): anything else ignored, a .env, is kept.
func removableWorktree(ctx context.Context, wt string, stripped map[string]bool) string {
	if commonDir(wt) == "" {
		return "its repository cannot be found"
	}
	if locked(wt) {
		return "it is locked"
	}
	if d, err := dirty(ctx, wt); err != nil || d {
		return "it has uncommitted changes"
	}
	if !headOnRemote(ctx, wt) {
		return "its HEAD is not on a remote"
	}
	ignored, err := ignoredEntries(ctx, wt)
	if err != nil {
		return "its ignored files cannot be listed"
	}
	for _, rel := range ignored {
		if !stripped[filepath.Join(wt, filepath.FromSlash(strings.TrimSuffix(rel, "/")))] {
			return "it holds ignored files that are not build output (" + rel + ")"
		}
	}
	return ""
}

// removeWorktree removes the linked worktree wt the way the quarantine
// removes a directory: `git worktree move` parks it in trash (one rename,
// which git records), q.InUse checks both paths once nothing can reach it by
// its old name, and only then `git worktree remove` (never --force) deletes
// it. git refuses a worktree that is dirty or locked, so a change made since
// it was checked is not lost. A failed check or removal moves it back.
func removeWorktree(ctx context.Context, wt string, q worktreereclaim.Quarantine) error {
	if q.InUse == nil || q.Trash == "" {
		return errors.New("no quarantine for the removal")
	}
	common := commonDir(wt)
	if common == "" {
		return fmt.Errorf("the repository of %s cannot be found", wt)
	}
	if err := os.MkdirAll(q.Trash, 0o700); err != nil {
		return err
	}
	parked := filepath.Join(q.Trash, filepath.Base(wt)+"-worktree")
	if _, err := git(ctx, common, "worktree", "move", wt, parked); err != nil {
		return err
	}
	err := q.InUse(ctx, []string{wt, parked})
	if err == nil {
		if _, err = git(ctx, common, "worktree", "remove", parked); err == nil {
			return nil
		}
	}
	if _, merr := git(context.WithoutCancel(ctx), common, "worktree", "move", parked, wt); merr != nil {
		return fmt.Errorf("%w; it stays at %s: %v", err, parked, merr)
	}
	return err
}

// pruneWorktrees drops the registrations of the repository at common whose
// directories are gone. Best effort.
func pruneWorktrees(ctx context.Context, common string) {
	_, _ = git(ctx, common, "worktree", "prune")
}
