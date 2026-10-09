// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
)

// effectiveWorktreeID is the workspace a thread's tools run in: the thread's
// own binding when it has one (a sub-agent spawned with a `worktree`), else the
// chat's. The bool reports whether the binding came from the thread, because a
// thread-bound workspace that has gone away must fail the call rather than
// quietly fall back to the project checkout the way a chat's does.
//
// A missing thread row is not an error: callers pass thread ids that may not
// have a row (a run that predates threads), and those have no binding.
func effectiveWorktreeID(ctx context.Context, repo db.Repository, chat *db.Chat, thread string) (*string, bool, error) {
	if thread != "" {
		t, err := repo.GetThread(ctx, thread)
		switch {
		case err == nil:
			if t != nil && t.WorktreeID != nil && *t.WorktreeID != "" {
				return t.WorktreeID, true, nil
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			return nil, false, fmt.Errorf("load thread %s: %w", thread, err)
		}
	}
	return chat.WorktreeID, false, nil
}
