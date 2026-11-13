package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ApplyAttentionUpdate derives incident scope from the stored launch chat. The
// locked incident row (including an empty tombstone) serializes all its actors;
// READ COMMITTED ensures waiters observe the previous holder's committed fence.
func (r *Repo) ApplyAttentionUpdate(ctx context.Context, update core.AttentionUpdate) (core.AttentionResult, error) {
	var result core.AttentionResult
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		result, err = store.ApplyAttentionUpdate(ctx, r.ORM(), update)
		return err
	})
	if err != nil {
		return core.AttentionResult{}, err
	}
	return result, nil
}

// ListAttentionIncidents lists open episodes with read-time recovery expiry.
func (r *Repo) ListAttentionIncidents(ctx context.Context, userID string) ([]*core.AttentionIncident, error) {
	return store.ListAttentionIncidents(ctx, r.ORM(), userID)
}

// AttentionManagedChats returns durable managed registrations, including stale
// terminal actors. Unfinished observers without a durable incident fail open to
// legacy notification handling when their terminal write is lost.
func (r *Repo) AttentionManagedChats(ctx context.Context, userID string, chatIDs []string) (map[string]bool, error) {
	return store.AttentionManagedChats(ctx, r.ORM(), userID, chatIDs)
}
