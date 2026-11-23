package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

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
