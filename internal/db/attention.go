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
