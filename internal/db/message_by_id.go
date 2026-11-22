// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/store"
)

// FindMessage retrieves a message by id, returning (nil, nil) when no such row
// exists.
//
// GetMessage treats a miss as an error, which is right for callers that are
// dereferencing a known id. This variant exists for the idempotency checks in
// SaveMessage, which ask "is this id already taken?" and must be able to hear
// "no" without unwrapping an error string. It mirrors GetMessageByActivityID's
// contract deliberately, so both idempotency lookups read the same way.
func (r *Repo) FindMessage(ctx context.Context, id string) (*Message, error) {
	if id == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}
	msg, err := store.FindMessage(ctx, r.ORM(), id)
	if err != nil {
		return nil, fmt.Errorf("failed to find message by ID: %w", err)
	}
	return msg, nil
}
