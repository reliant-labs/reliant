// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// LastThreadActivityByChat returns, for every thread of a chat, the timestamp of
// the newest message in that thread.
//
// It is the per-thread progress marker `reliant-dev workflow ps` uses to tell a
// thread that is still working from one that has gone quiet. Messages are the
// only durable per-thread progress evidence the schema has: step executions and
// position checkpoints are written for the ROOT run only, so a spawned agent
// thread has neither, while every turn it takes lands a message. A workflow
// row's `thread` column IS the message's `thread_id`, so these map keys join
// straight onto workflow rows.
//
// Read-only: one grouped SELECT per chat, no mutation.
func (r *Repo) LastThreadActivityByChat(ctx context.Context, chatID string) (map[string]time.Time, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.LastThreadActivityByChat(ctx, r.ORM(), chatID)
}
