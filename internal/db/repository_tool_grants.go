// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ToolGrant is what one recorded tool result granted its thread
// (core.ToolGrant).
type ToolGrant = core.ToolGrant

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
	return store.ListToolGrants(ctx, r.ORM(), chatID)
}
