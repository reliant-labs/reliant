// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// worktree.create runs on machines that are often saturated, and its caller
// can stop waiting, or lose the connection, while git is still checking out.
// What this file guards against, all seen on one cloud machine in prod
// (2026-11-03..06, worktree.create taking 20-135s against a 2m budget):
//
//   - A create whose context ends after `git worktree add` finished. That is
//     no proof its caller gave up (the caller's wait is not this context, and
//     the answer can still reach it), so a finished checkout is never taken
//     back: deleting one the server recorded as ACTIVE would lose the user's
//     workspace. It is finished as usual, locked for its worktree id, so a
//     re-sent request answers with it (finishedCreate).
//   - A `git worktree add` killed at the deadline. git cannot clean up after
//     SIGKILL, so it leaves a half-written checkout and a registration locked
//     "initializing" that holds the branch for good. The handler removes both
//     whenever the directory was its own (abandonCheckout).
//   - A connection that drops while the create runs. The daemon finishes it,
//     but the answer is lost. The router sends the request again after the
//     reconnect (reissuableDaemonCommands), and the second attempt waits for
//     the first (lockCreatePath) and answers with what it made
//     (finishedCreate), instead of failing on a branch it holds itself.
//   - A VM killed right after a create reported success. git does not fsync
//     the files that make a checkout a worktree, so the gitfile, HEAD or
//     branch ref can come back empty (llm bug reports, 2026-10-30). The
//     handler fsyncs them before it answers (syncNewCheckout).

// worktreeCreatePath is where worktree.create puts a checkout:
// <HOME>/.reliant/worktrees/<workspace_id>[/<sub_path>]. A sub_path of ""
// puts it at the workspace root itself (single-repo project).
//
// Both come from the request, and the path is locked, created and, on a
// failed create, removed whole (abandonCheckout), so it must stay inside the
// worktrees root: workspace_id is a relative path below the root
// (<project>/<name>-<id>, worktreepath.WorkspaceDirName), sub_path a relative
// path below the workspace, neither absolute nor climbing out with "..".
// Anything else is refused before the disk is touched.
func worktreeCreatePath(workspaceID, subPath string) (string, error) {
	if !filepath.IsLocal(workspaceID) || filepath.Clean(workspaceID) == "." {
		return "", fmt.Errorf("invalid workspace_id %q: must be a relative path below the worktrees directory", workspaceID)
	}
	if subPath != "" && !filepath.IsLocal(subPath) {
		return "", fmt.Errorf("invalid sub_path %q: must be a relative path inside the workspace", subPath)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	worktrees := filepath.Join(homeDir, ".reliant", "worktrees")
	root := filepath.Join(worktrees, workspaceID)
	p := root
	if subPath != "" {
		p = filepath.Join(root, subPath)
	}
	if rel, err := filepath.Rel(root, p); err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("invalid sub_path %q: leaves the workspace directory", subPath)
	}
	if rel, err := filepath.Rel(worktrees, root); err != nil || !filepath.IsLocal(rel) || rel == "." {
		return "", fmt.Errorf("invalid workspace_id %q: leaves the worktrees directory", workspaceID)
	}
	return p, nil
}

// worktreeFinishTimeout bounds finishing a checkout (head check, lock, fsync)
// whose create's context ended after `git worktree add` succeeded.
const worktreeFinishTimeout = 30 * time.Second

// createPathLocks serializes worktree.create per checkout path: one channel of
// capacity one per path this daemon process has created a checkout at. They
// are never removed; a daemon creates a bounded number of workspaces in its
// lifetime, and removing one safely would need a second lock.
var createPathLocks sync.Map // string -> chan struct{}

// lockCreatePath waits until no other worktree.create for path is running, or
// ctx ends. The returned func releases it.
func lockCreatePath(ctx context.Context, path string) (func(), error) {
	v, _ := createPathLocks.LoadOrStore(path, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// lockNewCheckout locks a checkout worktree.create made for its worktree id.
// A variable so a test can make it fail the way an expired context does.
var lockNewCheckout = worktreereclaim.LockCheckout

// finishedCreate answers a create that an earlier attempt of the same request
// already made: checkout is a registered worktree of the project on the
// requested branch, either locked for this request's worktree id (the lock is
// the last thing a successful create writes, so a checkout locked for this id
// was made by this request) or not locked at all. An unlocked one is what an
// attempt leaves when `git worktree add` finished but the lock could not be
// written (its finish context ran out): the path is this request's alone
// (worktreeCreatePath), so it is adopted and locked now; a failed lock is
// answered as an error, never as success. A checkout locked for anything
// else, "initializing" included, is not this request's. ok is false when no
// checkout is adopted, which the handler creates as usual.
func finishedCreate(ctx context.Context, req worktreeCreateRequest, checkout string) (worktreeCreateResponse, bool) {
	if req.WorktreeID == "" {
		return worktreeCreateResponse{}, false
	}
	if _, err := os.Lstat(filepath.Join(checkout, ".git")); err != nil {
		return worktreeCreateResponse{}, false
	}
	list := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	list.Dir = req.ProjectPath
	out, err := list.Output()
	if err != nil {
		return worktreeCreateResponse{}, false
	}
	for _, wt := range parseWorktreePorcelain(string(out)) {
		if !sameCheckoutPath(wt.Path, checkout) {
			continue
		}
		if wt.Prunable || wt.Branch != req.Branch {
			return worktreeCreateResponse{}, false
		}
		if wt.Locked && wt.LockReason != worktreereclaim.LockReason(req.WorktreeID) {
			return worktreeCreateResponse{}, false
		}
		if req.ExpectedHead != "" && wt.Head != req.ExpectedHead {
			return worktreeCreateResponse{}, false
		}
		if !wt.Locked {
			if err := lockNewCheckout(ctx, checkout, req.WorktreeID); err != nil {
				logging.WarnContext(ctx, "worktree.create found an earlier attempt's unlocked checkout but could not lock it",
					"worktree_path", checkout, "worktree_id", req.WorktreeID, "error", err)
				return worktreeCreateResponse{
					Error:        fmt.Sprintf("an earlier attempt made this checkout but it could not be locked for its worktree; retry to finish it: %v", err),
					WorktreePath: checkout,
				}, true
			}
			syncNewCheckout(ctx, req.ProjectPath, checkout, req.Branch)
		}
		base := req.BaseBranch
		if base == "" {
			base = getRepositoryDefaultBranch(ctx, req.ProjectPath)
		}
		logging.InfoContext(ctx, "worktree.create found its checkout already made by an earlier attempt",
			"worktree_path", checkout, "branch", req.Branch, "worktree_id", req.WorktreeID)
		return worktreeCreateResponse{Success: true, WorktreePath: checkout, BaseBranch: base}, true
	}
	return worktreeCreateResponse{}, false
}

// sameCheckoutPath compares a path git reports with one this daemon built,
// through symlinks when either side has one ($HOME may be a link).
func sameCheckoutPath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// localBranchAbsent reports whether repoPath has no local branch named branch,
// answering true only when git said so: `rev-parse --verify --quiet` exits 1
// for a missing ref. Any other outcome (the ref exists, git could not run, the
// context ended) is false, so a branch whose existence is unknown is never
// treated as one this create made.
func localBranchAbsent(ctx context.Context, repoPath, branch string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repoPath
	var exitErr *exec.ExitError
	return errors.As(cmd.Run(), &exitErr) && exitErr.ExitCode() == 1
}

// worktreeAbandonTimeout bounds taking back one checkout. It runs on a
// context of its own: the create's has usually ended by then.
const worktreeAbandonTimeout = time.Minute

// abandonCheckout removes a checkout this create made and is not handing to
// its caller: the directory, its registration in the project's repository
// (even one git left locked "initializing"), and branch when the create made
// it and nothing has moved it since startPoint. The caller guarantees the
// directory did not exist before this create, so everything in it is ours.
// Best effort: what it cannot remove, the startup repair prunes later.
func abandonCheckout(ctx context.Context, repoPath, checkout, branch, startPoint string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeAbandonTimeout)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repoPath
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}

	admin := registrationOf(ctx, repoPath, checkout)
	_, _ = git("worktree", "unlock", checkout)
	_, _ = git("worktree", "remove", "--force", checkout)
	removeErr := os.RemoveAll(checkout)
	if admin != "" {
		if err := os.RemoveAll(admin); err != nil && removeErr == nil {
			removeErr = err
		}
	}

	branchRemoved := false
	if branch != "" && startPoint != "" {
		head, errHead := git("rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
		start, errStart := git("rev-parse", "--verify", "--quiet", startPoint+"^{commit}")
		if errHead == nil && errStart == nil && head == start {
			// git refuses to delete a branch checked out anywhere, so a
			// branch someone else picked up in the meantime stays.
			_, err := git("branch", "-D", branch)
			branchRemoved = err == nil
		}
	}
	if removeErr != nil {
		logging.WarnContext(ctx, "worktree.create could not take back its unfinished checkout; the startup repair prunes what is left",
			"worktree_path", checkout, "error", removeErr)
		return
	}
	logging.InfoContext(ctx, "worktree.create took back a checkout it did not hand over",
		"worktree_path", checkout, "branch", branch, "branch_removed", branchRemoved)
}

// registrationOf is the admin directory (<common>/worktrees/<name>) that
// registers checkout in repoPath's repository, or "" when there is none. It is
// read from the checkout's gitfile when git got that far, else found by the
// gitdir file that points back at the checkout.
func registrationOf(ctx context.Context, repoPath, checkout string) string {
	if b, err := os.ReadFile(filepath.Join(checkout, ".git")); err == nil {
		if dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:"); ok {
			dir = strings.TrimSpace(dir)
			if !filepath.IsAbs(dir) { // worktree.useRelativePaths
				dir = filepath.Join(checkout, dir)
			}
			if filepath.Base(filepath.Dir(dir)) == "worktrees" {
				return filepath.Clean(dir)
			}
		}
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoPath, common)
	}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return ""
	}
	want := filepath.Join(checkout, ".git")
	for _, e := range entries {
		admin := filepath.Join(common, "worktrees", e.Name())
		b, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err == nil && sameCheckoutPath(strings.TrimSpace(string(b)), want) {
			return admin
		}
	}
	return ""
}

// syncNewCheckout makes the files that make checkout a worktree durable
// before the create reports success: its gitfile, its registration (HEAD,
// gitdir, commondir, index, lock) and the branch ref, with the directories
// that name them. git fsyncs none of them by default, and a VM killed in the
// seconds after a create left them empty. The working tree's files are not
// synced: they are git's objects checked out, which `git checkout` restores.
func syncNewCheckout(ctx context.Context, repoPath, checkout, branch string) {
	paths := []string{filepath.Join(checkout, ".git"), checkout, filepath.Dir(checkout)}
	admin := registrationOf(ctx, repoPath, checkout)
	if admin != "" {
		for _, name := range []string{"HEAD", "gitdir", "commondir", "index", "locked"} {
			paths = append(paths, filepath.Join(admin, name))
		}
		paths = append(paths, admin, filepath.Dir(admin))
		if b, err := os.ReadFile(filepath.Join(admin, "commondir")); err == nil && branch != "" {
			common := strings.TrimSpace(string(b))
			if !filepath.IsAbs(common) {
				common = filepath.Join(admin, common)
			}
			heads := filepath.Join(common, "refs", "heads")
			ref := filepath.Join(heads, filepath.FromSlash(branch))
			paths = append(paths, ref, filepath.Join(common, "packed-refs"))
			// A branch named fix/x lives in refs/heads/fix/, created for it.
			for dir := filepath.Dir(ref); strings.HasPrefix(dir, heads); dir = filepath.Dir(dir) {
				paths = append(paths, dir)
			}
		}
	}
	var failed []string
	for _, p := range paths {
		if err := fsyncPath(p); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if len(failed) > 0 {
		logging.WarnContext(ctx, "worktree created but not all of its git metadata could be synced to disk",
			"worktree_path", checkout, "failures", strings.Join(failed, "; "))
	}
}

// fsyncPath flushes one file or directory to disk. A path that does not exist
// (a packed ref, a branch kept only in packed-refs) is not a failure.
func fsyncPath(p string) error {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
