// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/store"
)

// SeedSeqCounterForFork raises chatID's seq allocator to cover everything
// reachable through forkAtContextWindowID's chain (store.SeedSeqCounterForFork).
//
// Called when a thread is forked. Safe to call repeatedly: the GREATEST keeps
// it monotonic, so a retry or a duplicate fork cannot move the counter
// backwards.
func (r *Repo) SeedSeqCounterForFork(ctx context.Context, chatID, forkAtContextWindowID string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID is required")
	}
	if forkAtContextWindowID == "" {
		return fmt.Errorf("fork context window ID is required")
	}
	return store.SeedSeqCounterForFork(ctx, r.ORM(), chatID, forkAtContextWindowID)
}
