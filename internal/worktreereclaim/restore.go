// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrBranchMoved means a snapshot was NOT applied because the checkout's HEAD is
// no longer the commit the snapshot was taken on. The work is intact in its ref;
// applying its whole tree over a newer HEAD would silently revert newer commits.
var ErrBranchMoved = errors.New("the branch moved since this work was saved")

// SnapshotRefsOnRow filters the refs recorded on a row down to the snapshots
// that may be applied. Orphan-tip refs (".../orphans/...") are kept for safety,
// never applied. Only refs RECORDED ON THE ROW are ever candidates: there is no
// scan of the repository for refs the row does not know about, because a stale
// ref from an earlier lifecycle must never be applied to a later one.
func SnapshotRefsOnRow(refs []string) []string {
	var out []string
	for _, r := range refs {
		if strings.HasPrefix(r, "refs/reliant/wip/") && !strings.Contains(r, "/orphans/") {
			out = append(out, r)
		}
	}
	return out
}

// LatestSnapshot returns the newest of the given refs that still resolves, or "".
// It looks only at the refs it is handed (those recorded on the row).
func LatestSnapshot(ctx context.Context, repoPath, worktreeID string, refs []string) string {
	snaps := SnapshotRefsOnRow(refs)
	sort.Slice(snaps, func(i, j int) bool { return refTime(snaps[i]) < refTime(snaps[j]) })
	for i := len(snaps) - 1; i >= 0; i-- {
		if _, err := git(ctx, repoPath, nil, "rev-parse", "--verify", "--quiet", snaps[i]+"^{commit}"); err == nil {
			return snaps[i]
		}
	}
	return ""
}

// refTime orders snapshot refs by the nanosecond stamp in their last segment.
func refTime(ref string) string {
	last := ref[strings.LastIndex(ref, "/")+1:]
	stamp, _, _ := strings.Cut(last, "-")
	return fmt.Sprintf("%030s", stamp)
}

// AppliedRef is where a snapshot's ref moves once it has been applied, so no
// later restore can pick it up again.
func AppliedRef(ref string) string {
	return strings.Replace(ref, "refs/reliant/wip/", "refs/reliant/applied/", 1)
}

// ApplySnapshot restores a saved snapshot onto a freshly created checkout, byte
// for byte, and only when that is provably the right thing to do:
//
//   - the checkout is clean, and its HEAD IS the commit the snapshot was taken
//     on (the snapshot commit's parent). Otherwise it returns ErrBranchMoved and
//     touches nothing. There is no three-way merge: the simple rule is the safe
//     one, and the work stays in its ref for a person to apply by hand.
//   - the snapshot's tree is written into the working tree through a temporary
//     index (the real index stays at HEAD), then the files on disk are re-hashed
//     and must equal the snapshot's tree.
//   - if ANY step fails, the checkout is put back to HEAD's clean state before
//     the error is returned, so a failed apply never leaves a half-written tree
//     and a retry is possible.
//
// On success the ref is moved to refs/reliant/applied/..., never deleted.
func ApplySnapshot(ctx context.Context, checkout, ref string) (err error) {
	if out, serr := git(ctx, checkout, nil, "status", "--porcelain", "--untracked-files=all"); serr != nil || out != "" {
		return fmt.Errorf("checkout is not clean; not applying a snapshot over it")
	}
	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	parent, err := git(ctx, checkout, nil, "rev-parse", "--verify", ref+"^")
	if err != nil {
		return fmt.Errorf("snapshot %s has no parent: %w", ref, err)
	}
	if head != parent {
		return fmt.Errorf("%w: it was taken on %s and HEAD is now %s; the work is kept at %s and was not applied", ErrBranchMoved, short(parent), short(head), ref)
	}

	tmp, err := os.MkdirTemp("", "reliant-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	var written []string
	defer func() {
		if err != nil {
			rollbackToHead(ctx, checkout, tmp, written)
		}
	}()

	tree, err := git(ctx, checkout, nil, "rev-parse", "--verify", ref+"^{tree}")
	if err != nil {
		return err
	}
	// Everything the snapshot differs from HEAD in: the only paths we may touch,
	// and so the only ones a rollback must put back.
	diff, err := gitRaw(ctx, checkout, nil, "diff-tree", "-r", "-z", "--no-renames", "--name-status", "HEAD^{tree}", tree)
	if err != nil {
		return err
	}
	fields := strings.Split(string(diff), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		written = append(written, fields[i+1])
	}

	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err = git(ctx, checkout, env, "read-tree", tree); err != nil {
		return err
	}
	if _, err = git(ctx, checkout, env, "checkout-index", "-a", "-f"); err != nil {
		return fmt.Errorf("writing the saved files: %w", err)
	}
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "D" { // in HEAD but not in the snapshot
			if rerr := os.Remove(filepath.Join(checkout, filepath.FromSlash(fields[i+1]))); rerr != nil && !os.IsNotExist(rerr) {
				return rerr
			}
		}
	}

	now, err := workTree(ctx, checkout, tmp, nil)
	if err != nil {
		return err
	}
	if now != tree {
		err = fmt.Errorf("restored files do not match the snapshot (tree %s, want %s)", now, tree)
		return err
	}
	return nil
}

// rollbackToHead puts every path a failed apply may have touched back to HEAD:
// tracked files are restored from HEAD, files that exist only in the snapshot
// are removed. The checkout was clean before the apply, so this is exact.
func rollbackToHead(ctx context.Context, checkout, tmp string, paths []string) {
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "rollback-index")}
	if _, err := git(ctx, checkout, env, "read-tree", "HEAD^{tree}"); err != nil {
		return
	}
	_, _ = git(ctx, checkout, env, "checkout-index", "-a", "-f")
	for _, rel := range paths {
		if _, err := git(ctx, checkout, nil, "cat-file", "-e", "HEAD:"+rel); err != nil {
			_ = os.Remove(filepath.Join(checkout, filepath.FromSlash(rel)))
		}
	}
	// Directories the snapshot created that are now empty.
	for _, rel := range paths {
		for d := filepath.Dir(filepath.Join(checkout, filepath.FromSlash(rel))); d != checkout && strings.HasPrefix(d, checkout); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
}

// RetireAppliedRef moves a successfully applied snapshot ref out of the wip
// namespace. It never deletes it.
func RetireAppliedRef(ctx context.Context, repoPath, ref string) error {
	sha, err := git(ctx, repoPath, nil, "rev-parse", "--verify", ref)
	if err != nil {
		return err
	}
	dest := AppliedRef(ref)
	if _, err := git(ctx, repoPath, nil, "update-ref", dest, sha, ""); err != nil {
		return err
	}
	_, err = git(ctx, repoPath, nil, "update-ref", "-d", ref, sha)
	return err
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
