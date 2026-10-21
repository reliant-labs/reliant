// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/store"
)

// GetToolResultBlock retrieves the tool_result block for a given tool_call_id,
// or (nil, nil) when there is none. Joins the transaction ctx carries, if any.
func (r *Repo) GetToolResultBlock(ctx context.Context, toolCallID string) (*MessageContentBlock, error) {
	return store.FindToolBlock(ctx, r.ORM(), toolCallID, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT)
}
