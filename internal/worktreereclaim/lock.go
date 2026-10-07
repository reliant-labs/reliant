// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// lockInfo is one checkout's registration as `git worktree list --porcelain`
// reports it.
type lockInfo struct {
	Registered bool
	Locked     bool
	Reason     string
	CommonDir  string
}

func inspectLock(ctx context.Context, checkout string) (lockInfo, error) {
	common, err := commonDir(ctx, checkout)
	if err != nil {
		return lockInfo{}, err
	}
	out, err := git(ctx, "", nil, "--git-dir="+common, "worktree", "list", "--porcelain")
	if err != nil {
		return lockInfo{}, err
	}
	info := lockInfo{CommonDir: common}
	for _, block := range strings.Split(out, "\n\n") {
		var path string
		var locked bool
		var reason string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				path = strings.TrimPrefix(line, "worktree ")
			case line == "locked":
				locked = true
			case strings.HasPrefix(line, "locked "):
				locked = true
				reason = strings.TrimPrefix(line, "locked ")
			}
		}
		if path != "" && samePath(path, checkout) {
			info.Registered = true
			info.Locked = locked
			info.Reason = reason
			return info, nil
		}
	}
	return info, nil
}

// isLinkedWorktree reports whether checkout's .git is the file of a linked
// worktree (gitdir: <repo>/.git/worktrees/<name>). A main checkout has a .git
// directory, and a submodule's .git file points into <repo>/.git/modules; both
// are repositories in their own right and never removable here.
func isLinkedWorktree(checkout string) bool {
	fi, err := os.Lstat(filepath.Join(checkout, ".git"))
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		return false
	}
	dir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	return filepath.Base(filepath.Dir(filepath.ToSlash(dir))) == "worktrees" ||
		filepath.Base(filepath.Dir(dir)) == "worktrees"
}

func setLock(ctx context.Context, info lockInfo, checkout, reason string) error {
	if info.Locked {
		if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "unlock", checkout); err != nil {
			return err
		}
	}
	_, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "lock", "--reason", reason, checkout)
	return err
}

// LockCheckout locks the checkout as the ACTIVE worktree `worktreeID`. A
// checkout locked by someone else, or for another worktree id, is left alone
// and reported: replacing its reason would erase their claim.
//
// A checkout that was archived under a fence is re-locked as "restored
// <fence>", never as a plain active lock, so the highest fence retired for the
// row survives and no claim built before the restore can use it again.
func LockCheckout(ctx context.Context, checkout, worktreeID string) error {
	return lockActiveAs(ctx, checkout, worktreeID, "")
}

// RestoreCheckout is LockCheckout for an unarchive: retiredFence is the archive
// being undone. It always rewrites the lock, whatever its current state.
func RestoreCheckout(ctx context.Context, checkout, worktreeID, retiredFence string) error {
	return lockActiveAs(ctx, checkout, worktreeID, retiredFence)
}

func lockActiveAs(ctx context.Context, checkout, worktreeID, retiredFence string) error {
	info, err := inspectLock(ctx, checkout)
	if err != nil {
		return err
	}
	if !info.Registered {
		return fmt.Errorf("%s is not a registered git worktree", checkout)
	}
	cur, mine := parseLock(info.Reason)
	mine = mine && cur.ID == worktreeID
	if info.Locked && !mine {
		return fmt.Errorf("%s is locked for something else (%q)", checkout, info.Reason)
	}

	// The highest fence retired so far: from the current lock, and from the one
	// being retired now.
	retired := ""
	if mine && (cur.Archived || cur.Restored) {
		retired = cur.Fence
	}
	if fenceNewer(retiredFence, retired) || (retired == "" && retiredFence != "") {
		retired = retiredFence
	}
	want := LockReason(worktreeID)
	if retired != "" {
		want = RestoredLockReason(worktreeID, retired)
	}
	if info.Locked && info.Reason == want {
		return nil
	}
	return setLock(ctx, info, checkout, want)
}

// claimArchived makes the archive fence the lock reason of a checkout, which
// is what authorizes its removal. It refuses a lock that is not this row's, a
// fence that is not newer than one already retired, and an already newer claim.
func claimArchived(ctx context.Context, info lockInfo, checkout, worktreeID, fence string) (HeldReason, string) {
	want := ArchivedLockReason(worktreeID, fence)
	if info.Locked && info.Reason == want {
		return "", ""
	}
	if info.Locked {
		p, ok := parseLock(info.Reason)
		switch {
		case !ok || p.ID != worktreeID:
			return ReasonUnmanaged, fmt.Sprintf("%s is locked for something else (%q)", checkout, info.Reason)
		case p.Restored && !fenceNewer(fence, p.Fence):
			return ReasonStale, "this archive was already restored; a newer archive is needed"
		case p.Archived && !fenceNewer(fence, p.Fence):
			return ReasonStale, "a newer archive of this workspace holds the lock"
		}
	}
	if err := setLock(ctx, info, checkout, want); err != nil {
		return ReasonUnverified, err.Error()
	}
	return "", ""
}

// UnlockForRemoval clears the reliant lock on a checkout that is about to be
// removed deliberately by the legacy single-directory commands. Best effort.
func UnlockForRemoval(ctx context.Context, checkout string) {
	info, err := inspectLock(ctx, checkout)
	if err != nil || !info.Locked || !ReliantLock(info.Reason) {
		return
	}
	_, _ = git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "unlock", checkout)
}
