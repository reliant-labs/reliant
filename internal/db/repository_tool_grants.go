// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

// ToolGrant is what one recorded tool result granted its thread. See
// ListToolGrants.
type ToolGrant struct {
	ThreadID string
	Tools    []string
}

// ListToolGrants returns every grant a tool result in chatID recorded, one
// entry per result, ordered by thread. A load_tool result records its grants
// in the same write as its content (tool_call_results.granted_tools), so this
// is the durable record of what each thread was granted — the one a coarse
// fresh restart rebuilds the threads' grants from after the execution that
// held them in memory died.
func (r *Repo) ListToolGrants(ctx context.Context, chatID string) ([]*ToolGrant, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if r.DB == nil {
		return nil, fmt.Errorf("repository has no database connection")
	}
	rows, err := pgdb.New(r.DB.DB(ctx)).ListToolGrantsForChat(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list tool grants: %w", err)
	}
	grants := make([]*ToolGrant, 0, len(rows))
	for _, row := range rows {
		grants = append(grants, &ToolGrant{ThreadID: row.ThreadID, Tools: row.GrantedTools})
	}
	return grants, nil
}
