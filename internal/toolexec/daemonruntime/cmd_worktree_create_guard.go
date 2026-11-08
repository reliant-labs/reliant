// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
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
//     workspace. It is finished as usual, locked for its worktree id.
//   - A `git worktree add` killed at the deadline. git cannot clean up after
//     SIGKILL, so it leaves a half-written checkout and a registration locked
//     "initializing" that holds the branch for good. The handler removes both
//     whenever the directory was its own and the create was not forced
//     (abandonCheckout); the startup repair prunes what is left.
//   - A VM killed right after a create reported success. git does not fsync
//     the files that make a checkout a worktree, so the gitfile, HEAD or
//     branch ref can come back empty (llm bug reports, 2026-10-30). The
//     handler fsyncs them before it answers (syncNewCheckout).
//
// A connection that drops while the create runs still loses the answer: the
// create is not re-sent, and the checkout it finished is left for the server,
// which knows the workspace failed, to clean up (owner decision 17 (a),
// 2026-11-08: adopting a re-sent request's checkout from git state returns as
// its own design).

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

// beforeBranchCreate, when set (tests only), runs right before a create makes
// its new branch: where another git process may make the same branch first.
var beforeBranchCreate func(repoPath, branch string)

// ownedBranch is a branch one create made: its name, the commit createBranch
// set it to, and the token createBranch wrote into its one reflog entry. The
// token is what proves ownership later: a tip still at the start point does
// not (anyone may set a branch to that commit), but a reflog holding only the
// entry this create wrote does, because git appends to a ref's reflog on every
// update and drops it with the ref.
type ownedBranch struct {
	name  string
	oid   string
	token string
}

// createBranch makes refs/heads/<branch> at startPoint's commit, only if no
// such ref exists: `git update-ref <ref> <new> ""` is a compare-and-swap whose
// expected old value is "absent", applied in git's own ref transaction. So it
// succeeds only for the call that made the branch, whatever other git process
// runs beside it. It records the reflog entry `git branch` would, tagged with
// a token unique to this call, which deleteOwnedBranch checks before it takes
// the branch back. On failure it returns git's output and a nil branch.
func createBranch(ctx context.Context, repoPath, branch, startPoint string) (*ownedBranch, []byte, error) {
	rev := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", startPoint+"^{commit}")
	rev.Dir = repoPath
	out, err := rev.Output()
	if err != nil {
		return nil, []byte(fmt.Sprintf("fatal: invalid start point '%s'\n", startPoint)), err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, []byte("fatal: could not generate a branch ownership token\n"), err
	}
	owned := &ownedBranch{name: branch, oid: strings.TrimSpace(string(out)), token: hex.EncodeToString(nonce[:])}
	cmd := exec.CommandContext(ctx, "git", "update-ref", "--create-reflog", "-m", owned.reflogMessage(startPoint),
		"refs/heads/"+branch, owned.oid, "")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, out, err
	}
	return owned, nil, nil
}

// reflogTokenPrefix marks the token in the reflog entry createBranch writes.
const reflogTokenPrefix = "reliant-worktree-create:"

func (b *ownedBranch) reflogMessage(startPoint string) string {
	return "branch: Created from " + startPoint + " (" + reflogTokenPrefix + b.token + ")"
}

// beforeBranchDelete, when set (tests only), runs while deleteOwnedBranch
// holds the branch's ref lock, right before it checks ownership.
var beforeBranchDelete func(repoPath, branch string)

// deleteOwnedBranch deletes b only while it is provably still the branch this
// create made, and returns whether it did. It deletes in one git ref
// transaction: `prepare` takes the ref's lock and verifies its tip is still
// b.oid, so no other git process can update the ref until the transaction
// commits or aborts. With the lock held it checks that the reflog is exactly
// the one entry createBranch wrote (a branch deleted and remade, or moved and
// moved back, has another reflog) and that no worktree has the branch checked
// out (what `git branch -D` refuses), and only then commits the deletion.
// Anything else aborts and leaves the branch.
func deleteOwnedBranch(ctx context.Context, repoPath string, b *ownedBranch) bool {
	ref := "refs/heads/" + b.name
	cmd := exec.CommandContext(ctx, "git", "update-ref", "--stdin")
	cmd.Dir = repoPath
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	if _, err := fmt.Fprintf(stdin, "start\ndelete %s %s\nprepare\n", ref, b.oid); err != nil {
		return false
	}
	replies := bufio.NewReader(stdout)
	for _, want := range []string{"start: ok", "prepare: ok"} {
		line, err := replies.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != want {
			return false // the tip moved, or git refused: the transaction is gone
		}
	}
	if beforeBranchDelete != nil {
		beforeBranchDelete(repoPath, b.name)
	}
	if !branchStillOwned(ctx, repoPath, b) {
		_, _ = io.WriteString(stdin, "abort\n")
		return false
	}
	if _, err := io.WriteString(stdin, "commit\n"); err != nil {
		return false
	}
	line, err := replies.ReadString('\n')
	return err == nil && strings.TrimSpace(line) == "commit: ok"
}

// branchStillOwned reports whether b's reflog is exactly the entry
// createBranch wrote and no worktree has b checked out. The caller holds the
// ref's lock, so neither the ref nor its reflog can change under it.
func branchStillOwned(ctx context.Context, repoPath string, b *ownedBranch) bool {
	ref := "refs/heads/" + b.name
	logCmd := exec.CommandContext(ctx, "git", "reflog", "show", "--format=%gs", ref, "--")
	logCmd.Dir = repoPath
	out, err := logCmd.Output()
	if err != nil {
		return false
	}
	entries := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(entries) != 1 || !strings.HasSuffix(entries[0], "("+reflogTokenPrefix+b.token+")") {
		return false
	}
	wtCmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	wtCmd.Dir = repoPath
	out, err = wtCmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "branch "+ref {
			return false
		}
	}
	return true
}

// worktreeAbandonTimeout bounds taking back one checkout. It runs on a
// context of its own: the create's has usually ended by then.
const worktreeAbandonTimeout = time.Minute

// abandonCheckout removes a checkout this create made and is not handing to
// its caller: the directory, its registration in the project's repository
// (even one git left locked "initializing"), and the branch the create made
// (nil when it made none) while it is provably still that branch
// (deleteOwnedBranch). The caller guarantees the
// directory did not exist before this create, so everything in it is ours.
// Best effort: what it cannot remove, the startup repair prunes later.
func abandonCheckout(ctx context.Context, repoPath, checkout string, created *ownedBranch) {
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

	branch, branchRemoved := "", false
	if created != nil {
		branch = created.name
		branchRemoved = deleteOwnedBranch(ctx, repoPath, created)
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
// It returns an error naming every path that could not be synced: the create
// must not report success then.
func syncNewCheckout(ctx context.Context, repoPath, checkout, branch string) error {
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
		logging.WarnContext(ctx, "worktree checked out but not all of its git metadata could be synced to disk",
			"worktree_path", checkout, "failures", strings.Join(failed, "; "))
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// fsyncPath is a variable so a test can make a sync fail.
var fsyncPath = fsyncFile

// fsyncFile flushes one file or directory to disk. A path that does not exist
// (a packed ref, a branch kept only in packed-refs) is not a failure.
func fsyncFile(p string) error {
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
