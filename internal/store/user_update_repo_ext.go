// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for UserUpdate: the per-user ledger the global
// WebSocket replays from. Every function takes and returns internal/db/core
// types.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// AppendUserUpdate allocates the user's next update sequence and writes
// update under it, in the transaction ctx carries (it refuses without one: see
// AllocateUpdateSequence). It sets update.SequenceNumber, and update.ID to
// "<user>-<seq>" unless the caller chose one, and stamps CreatedAt.
func AppendUserUpdate(ctx context.Context, db orm.Context, update *core.UserUpdate, callerChoseID bool) error {
	seq, err := AllocateUpdateSequence(ctx, db, UpdateStreamUser, update.UserID)
	if err != nil {
		return fmt.Errorf("failed to get next user sequence number: %w", err)
	}
	return insertUserUpdateAt(ctx, db, update, seq, callerChoseID)
}

// AppendUserUpdateBuiltUnderLock allocates userID's next update sequence
// FIRST, then asks build for the update and writes it under that number, in
// the transaction ctx carries. Allocating takes the user's counter row lock
// until commit, so whatever build reads it reads after every earlier holder
// of the lock committed: a higher sequence number never carries an older
// reading. Use it for an update that reports current state (activity), where
// AppendUserUpdate's read-then-allocate order could let a stale reading land
// at the newer number. A build error is returned as is, and the caller's
// transaction must roll back with it: committing the allocation alone would
// leave a gap in the user's sequence.
func AppendUserUpdateBuiltUnderLock(ctx context.Context, db orm.Context, userID string, build func(ctx context.Context) (*core.UserUpdate, error)) (*core.UserUpdate, error) {
	seq, err := AllocateUpdateSequence(ctx, db, UpdateStreamUser, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next user sequence number: %w", err)
	}
	update, err := build(ctx)
	if err != nil {
		return nil, err
	}
	if update == nil {
		return nil, fmt.Errorf("user update for %s: build returned none", userID)
	}
	update.UserID = userID
	if err := insertUserUpdateAt(ctx, db, update, seq, false); err != nil {
		return nil, err
	}
	return update, nil
}

// insertUserUpdateAt writes update as the user's update number seq.
func insertUserUpdateAt(ctx context.Context, db orm.Context, update *core.UserUpdate, seq int64, callerChoseID bool) error {
	update.SequenceNumber = seq
	if !callerChoseID {
		update.ID = fmt.Sprintf("%s-%d", update.UserID, seq)
	}
	update.CreatedAt = time.Now().UTC()
	if err := CreateUserUpdate(ctx, db, &UserUpdate{
		ID:             update.ID,
		UserID:         update.UserID,
		SequenceNumber: update.SequenceNumber,
		ProjectID:      update.ProjectID,
		WorktreeID:     update.WorktreeID,
		ChatID:         update.ChatID,
		UpdateType:     update.UpdateType,
		EntityType:     update.EntityType,
		EntityID:       update.EntityID,
		Data:           string(update.Data),
		CreatedAt:      update.CreatedAt,
	}); err != nil {
		return fmt.Errorf("failed to insert user update: %w", err)
	}
	return nil
}

// LatestUserUpdateSequence is the highest sequence number a user's ledger
// holds, 0 for a user with none.
func LatestUserUpdateSequence(ctx context.Context, db orm.Context, userID string) (int64, error) {
	var seq int64
	err := db.Bun().NewSelect().Model((*UserUpdate)(nil)).
		ColumnExpr("COALESCE(MAX(sequence_number), 0)").
		Where("user_id = ?", userID).
		Scan(ctx, &seq)
	return seq, err
}

// UserUpdatesSince is a user's updates after sinceSeq, ascending, at most
// limit of them.
func UserUpdatesSince(ctx context.Context, db orm.Context, userID string, sinceSeq int64, limit int) ([]core.UserUpdate, error) {
	rows, err := ListUserUpdate(ctx, db,
		orm.WhereEq(UserUpdateFieldUserID, userID),
		orm.WhereGt(UserUpdateFieldSequenceNumber, sinceSeq),
		orm.WithOrderBy(UserUpdateFieldSequenceNumber, orm.Asc),
		orm.WithLimit(limit))
	if err != nil {
		return nil, err
	}
	updates := make([]core.UserUpdate, len(rows))
	for i, row := range rows {
		updates[i] = core.UserUpdate{
			ID:             row.ID,
			UserID:         row.UserID,
			SequenceNumber: row.SequenceNumber,
			ProjectID:      row.ProjectID,
			WorktreeID:     row.WorktreeID,
			ChatID:         row.ChatID,
			UpdateType:     row.UpdateType,
			EntityType:     row.EntityType,
			EntityID:       row.EntityID,
			Data:           json.RawMessage(row.Data),
			CreatedAt:      row.CreatedAt,
		}
	}
	return updates, nil
}

// UserUpdateExtra carries fields on UserUpdate that are NOT columns — computed
// values, transient state, anything derived rather than stored. UserUpdate is
// embedded on UserUpdateExtra (see user_update_orm.go), so a field added here is a field on
// every UserUpdate value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type UserUpdateExtra struct {
}
