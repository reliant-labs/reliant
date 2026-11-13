// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Moving a project to another org (control-plane
// docs/design/org-project-move.md). The caller decides who may move and to
// where (internal/authz, internal/orgs); these record and execute it.

// InsertProjectOrgMove records a requested move in state preparing, or
// returns the move the same (actor, request key) made before, with created
// false. A second open move of the project is
// core.ErrProjectOrgMoveInProgress.
func (r *Repo) InsertProjectOrgMove(ctx context.Context, m *core.ProjectOrgMove) (*core.ProjectOrgMove, bool, error) {
	return store.InsertProjectOrgMove(ctx, r.ORM(), m)
}

// GetProjectOrgMove reads one move, or core.ErrProjectOrgMoveNotFound.
func (r *Repo) GetProjectOrgMove(ctx context.Context, id string) (*core.ProjectOrgMove, error) {
	return store.GetProjectOrgMove(ctx, r.ORM(), id)
}

// TransitionProjectOrgMove advances a move from one of from to to
// (store.TransitionProjectOrgMove); any other state is
// core.ErrProjectOrgMoveState.
func (r *Repo) TransitionProjectOrgMove(ctx context.Context, id string, from []core.ProjectOrgMoveState, to core.ProjectOrgMoveState, detail string, counts *core.ProjectOrgMoveCounts) (*core.ProjectOrgMove, error) {
	return store.TransitionProjectOrgMove(ctx, r.ORM(), id, from, to, detail, counts)
}

// ProjectOrgMoveBlockers reads what refuses moving the project now: the
// preview's read, which the commit repeats under its locks.
func (r *Repo) ProjectOrgMoveBlockers(ctx context.Context, projectID string) (*core.ProjectOrgMoveBlockers, error) {
	return store.ProjectOrgMoveBlockers(ctx, r.ORM(), projectID)
}

// MoveProjectOrg commits a prepared move: the project, its chats, their runs
// and its triggers move to the target org in one ReadCommitted transaction
// (store.MoveProjectOrg). The project owner's org lock is taken first, so no
// backfill or org-sensitive write of the owner interleaves; then the move,
// project and chat rows are locked and the quiescence predicate re-read.
//
// Work running in the project is core.ErrProjectNotQuiescent with the
// result's Blockers set, and nothing is written. A project that left the
// move's source org (or never had an org and DefaultOrgID is not the source)
// is store.ErrOrgChanged. A move that already committed returns
// AlreadyMoved: the step is idempotent.
func (r *Repo) MoveProjectOrg(ctx context.Context, c core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error) {
	// The owner is fixed on the move row, so it is read before the lock;
	// the transaction re-reads the row under it.
	move, err := store.GetProjectOrgMove(ctx, r.ORM(), c.MoveID)
	if err != nil {
		return nil, err
	}
	var result *core.ProjectOrgMoveResult
	err = r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := store.LockUserOrgs(ctx, r.ORM(), move.OwnerUserID); err != nil {
			return err
		}
		var err error
		result, err = store.MoveProjectOrg(ctx, r.ORM(), c)
		if err != nil || result.AlreadyMoved {
			return err
		}
		// The owner's clients learn the project moved in the commit's own
		// transaction: stamped with the target org (user_updates_stamp_org
		// reads the project as this transaction left it), it reaches them in
		// the org it is now in. The log's older rows keep their orgs.
		projectID := result.Move.ProjectID
		return r.EmitUserRefetch(ctx, result.Move.OwnerUserID, RefetchProjectOrgMoved, RefetchOpts{ProjectID: &projectID})
	})
	return result, err
}

// ListProjectOrgMoves lists moves newest first (store.ListProjectOrgMoves):
// an org's ledger, or a person's own moves.
func (r *Repo) ListProjectOrgMoves(ctx context.Context, f core.ProjectOrgMoveFilters) ([]*core.ProjectOrgMove, error) {
	return store.ListProjectOrgMoves(ctx, r.ORM(), f)
}

// ListProjectTriggersPausedBy lists the project's triggers the system paused
// for reason.
func (r *Repo) ListProjectTriggersPausedBy(ctx context.Context, projectID, reason string) ([]*core.Trigger, error) {
	return store.ListProjectTriggersPausedBy(ctx, r.ORM(), projectID, reason)
}

// ListProjectTriggersResumedSince lists the project's triggers a move turned
// back on at or after since (store.ListProjectTriggersResumedSince).
func (r *Repo) ListProjectTriggersResumedSince(ctx context.Context, projectID string, since time.Time) ([]*core.Trigger, error) {
	return store.ListProjectTriggersResumedSince(ctx, r.ORM(), projectID, since)
}

// ResumeTriggerPausedBy turns a trigger back on only while it is still
// paused for reason and is the version the caller checked (its updated_at,
// seen), recording it in the trigger's change history; it reports whether it
// did. A person's pause or resume since wins (false). A change since that left
// the pause standing, such as an edit that kept it disabled, is
// core.ErrTriggerChangedSinceRead: the caller reads it again and checks the
// definition it would turn on.
func (r *Repo) ResumeTriggerPausedBy(ctx context.Context, id, reason string, seen time.Time) (bool, error) {
	var resumed bool
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := store.LockTrigger(ctx, r.ORM(), id); err != nil {
			return err
		}
		before, err := store.GetTrigger(ctx, r.ORM(), id)
		if err != nil {
			return err
		}
		if before.Enabled || before.PausedReason != reason {
			return nil
		}
		if !before.UpdatedAt.Equal(seen) {
			return core.ErrTriggerChangedSinceRead
		}
		if resumed, err = store.ResumeTriggerPausedBy(ctx, r.ORM(), id, reason, seen); err != nil || !resumed {
			return err
		}
		after, err := store.GetTrigger(ctx, r.ORM(), id)
		if err != nil {
			return err
		}
		return store.RecordTriggerChange(ctx, r.ORM(), core.TriggerChangeOrgMoveResumed, before, after)
	})
	return resumed, err
}

// ProjectOrgMoveEffects reads what moving the project to toOrgID would do,
// as reliant can tell it: the preview's effects (store.ProjectOrgMoveEffects).
func (r *Repo) ProjectOrgMoveEffects(ctx context.Context, projectID, toOrgID string) (*core.ProjectOrgMoveEffects, error) {
	return store.ProjectOrgMoveEffects(ctx, r.ORM(), projectID, toOrgID)
}

// FindProjectOrgMoveByRequest is the move an actor's request key made, or
// core.ErrProjectOrgMoveNotFound.
func (r *Repo) FindProjectOrgMoveByRequest(ctx context.Context, actorUserID, requestKey string) (*core.ProjectOrgMove, error) {
	return store.FindProjectOrgMoveByRequest(ctx, r.ORM(), actorUserID, requestKey)
}
