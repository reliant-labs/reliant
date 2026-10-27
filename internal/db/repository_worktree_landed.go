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

// WorktreeRunActivities reports, per worktree, whether a chat run is going in
// it and when anything last happened in it (store.WorktreeRunActivities).
func (r *Repo) WorktreeRunActivities(ctx context.Context, ids []string) (map[string]WorktreeRunActivity, error) {
	return store.WorktreeRunActivities(ctx, r.ORM(), ids)
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
