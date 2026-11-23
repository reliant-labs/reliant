// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// GetConnection returns the user's own live connection: its metadata only, never its
// ciphertext (internal/credentials/connectionvault holds that). The trigger paths resolve an
// activation's owner through it.
func (r *Repo) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return store.GetUserConnection(ctx, r.ORM(), userID, id)
}

// DefaultConnection returns the user's default live connection to an integration: metadata only.
func (r *Repo) DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error) {
	return store.DefaultUserConnection(ctx, r.ORM(), userID, integrationID)
}
