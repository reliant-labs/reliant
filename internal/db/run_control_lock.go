// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/store"
)

// LockChatRunControl takes the chat's run-control lock and returns the func
// that releases it. Release is idempotent. It is store.LockChatRunControl
// over the Repository's ORM client, which says why the lock exists and how
// it is held; inside a transaction it refuses with store.ErrRunControlInTx.
func (r *Repo) LockChatRunControl(ctx context.Context, chatID string) (func(), error) {
	if r.DB == nil {
		return nil, errors.New("database connection not initialized")
	}
	return store.LockChatRunControl(ctx, r.ORM(), chatID)
}
