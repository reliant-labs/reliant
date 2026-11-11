package db

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// WorktreeRunActivity is core.WorktreeRunActivity.
type WorktreeRunActivity = core.WorktreeRunActivity

// ArchivedBecauseLanded is core.ArchivedBecauseLanded.
const ArchivedBecauseLanded = core.ArchivedBecauseLanded
const ArchivedBecauseRecoverableIdle = core.ArchivedBecauseRecoverableIdle

func (r *Repo) ArchiveRecoverableIdleWorktree(ctx context.Context, id string, quietSince time.Time) (bool, error) {
	return store.ArchiveRecoverableIdleWorktree(ctx, r.ORM(), id, quietSince)
}

// WorktreeRunActivities reports, per worktree, whether a chat run is going in
// it and when anything last happened in it (store.WorktreeRunActivities).
func (r *Repo) WorktreeRunActivities(ctx context.Context, ids []string) (map[string]WorktreeRunActivity, error) {
	return store.WorktreeRunActivities(ctx, r.ORM(), ids)
}

// NoteWorktreeUse raises a live worktree's last_active to the last use its
// daemon saw (store.NoteWorktreeUse).
func (r *Repo) NoteWorktreeUse(ctx context.Context, id string, at time.Time) error {
	return store.NoteWorktreeUse(ctx, r.ORM(), id, at)
}

// MarkWorktreeForReclaim records a reclaim mark (store.MarkWorktreeForReclaim).
func (r *Repo) MarkWorktreeForReclaim(ctx context.Context, id, markID string, quietSince time.Time, use *time.Time, reason string) (bool, error) {
	return store.MarkWorktreeForReclaim(ctx, r.ORM(), id, markID, quietSince, use, reason)
}

// ClearWorktreeMark cancels a reclaim mark (store.ClearWorktreeMark).
func (r *Repo) ClearWorktreeMark(ctx context.Context, id, markID string) (bool, error) {
	return store.ClearWorktreeMark(ctx, r.ORM(), id, markID)
}

// ApproveWorktreeMark records "Reclaim now" for a mark
// (store.ApproveWorktreeMark).
func (r *Repo) ApproveWorktreeMark(ctx context.Context, id, markID string) (bool, error) {
	return store.ApproveWorktreeMark(ctx, r.ORM(), id, markID)
}

// ListMarkedWorktreesForUser lists the user's marked live worktrees
// (store.ListMarkedWorktreesForUser).
func (r *Repo) ListMarkedWorktreesForUser(ctx context.Context, userID string) ([]*core.HeldWorktree, error) {
	return store.ListMarkedWorktreesForUser(ctx, r.ORM(), userID)
}

// ArchiveMarkedWorktree reclaims a worktree under its reclaim mark
// (store.ArchiveMarkedWorktree).
func (r *Repo) ArchiveMarkedWorktree(ctx context.Context, id, markID string) (bool, error) {
	return store.ArchiveMarkedWorktree(ctx, r.ORM(), id, markID)
}

// ArchiveLandedWorktree archives a landed worktree that has been quiet since
// quietSince, leaving its chats live (store.ArchiveLandedWorktree).
func (r *Repo) ArchiveLandedWorktree(ctx context.Context, id string, quietSince time.Time) (bool, error) {
	return store.ArchiveLandedWorktree(ctx, r.ORM(), id, quietSince)
}

// ReviveArchivedWorktree unarchives a worktree a live chat is about to work
// in and returns the archive fence the daemon must retire
// (store.ReviveArchivedWorktree).
func (r *Repo) ReviveArchivedWorktree(ctx context.Context, id string) (string, error) {
	return store.ReviveArchivedWorktree(ctx, r.ORM(), id)
}
