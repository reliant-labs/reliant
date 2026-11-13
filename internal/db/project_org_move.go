// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"

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
		return err
	})
	return result, err
}

// ProjectOrgMoveEffects reads what moving the project to toOrgID would do,
// as reliant can tell it: the preview's effects (store.ProjectOrgMoveEffects).
func (r *Repo) ProjectOrgMoveEffects(ctx context.Context, projectID, toOrgID string) (*core.ProjectOrgMoveEffects, error) {
	return store.ProjectOrgMoveEffects(ctx, r.ORM(), projectID, toOrgID)
}
