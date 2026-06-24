// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// =============================================================================
// REPOSITORY HELPER FUNCTIONS
// =============================================================================
//
// These helpers provide clean, composable functions for database operations.
// They MUST be called within a RunTx transaction context.
// Database triggers automatically populate the chat_updates table (migration 028).

// GetToolResultBlock retrieves a tool_result block for a given tool_call_id.
// Can be called outside a transaction context.
func (r *Repo) GetToolResultBlock(ctx context.Context, toolCallID string) (*MessageContentBlock, error) {
	tx := r.DB.DB(ctx)
	query := `
		SELECT id, message_id, position, block_type, content,
		       tool_name, tool_input, tool_call_id, is_error,
		       version, node_id, node_path, created_at, updated_at
		FROM message_content_blocks
		WHERE tool_call_id = ? AND block_type = ?
		LIMIT 1
	`
	query = r.bindQuery(query)

	var block MessageContentBlock
	// Use sql.NullString for nullable columns
	var nodeID, nodePath sql.NullString
	err := tx.QueryRowContext(ctx, query, toolCallID, int32(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT)).Scan(
		&block.ID, &block.MessageID, &block.Position, &block.BlockType, &block.Content,
		&block.ToolName, &block.ToolInput, &block.ToolCallID, &block.IsError,
		&block.Version, &nodeID, &nodePath, &block.CreatedAt, &block.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	// Convert sql.NullString to string
	if nodeID.Valid {
		block.NodeID = nodeID.String
	}
	if nodePath.Valid {
		block.NodePath = nodePath.String
	}

	return &block, nil
}
