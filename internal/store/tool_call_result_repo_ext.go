// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ToolCallResult: a call's result, keyed by
// the call it answers (tool_call_id is the primary key AND the foreign key
// into tool_calls), and the grants a load_tool result recorded. Every function
// takes and returns internal/db/core types.
package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// upsertToolCallResultSQL writes a result for chat $8's call, replacing any
// existing result for the same call.
//
// A result belongs to its call (the foreign key), and so to the call's chat.
// The EXISTS is what makes the writer prove it is that chat: a result written
// for chat B under an id chat A's call holds would otherwise satisfy the
// foreign key against A's call and replace A's result. Writes nothing (0 rows)
// when the call is not this chat's or does not exist; UpsertToolCallResult
// reports both as core.ErrToolCallIDInAnotherChat.
//
// granted_tools travels with content: a rewrite of the result (an error
// replacing it, a repair) replaces what the call granted, so the grants always
// describe the result the model reads.
//
// Bound parameters through the raw passthrough: content is the tool's whole
// output.
const upsertToolCallResultSQL = `
INSERT INTO tool_call_results (
    tool_call_id, message_id, content, is_error, created_at, updated_at, granted_tools
)
SELECT
    $1::text, $2::text, $3::text,
    $4::boolean, $5::timestamptz, $6::timestamptz,
    COALESCE($7::text[], '{}')
WHERE EXISTS (
    SELECT 1 FROM tool_calls
    WHERE id = $1::text AND chat_id = $8::text
)
ON CONFLICT (tool_call_id) DO UPDATE SET
    message_id = EXCLUDED.message_id,
    content = EXCLUDED.content,
    is_error = EXCLUDED.is_error,
    updated_at = EXCLUDED.updated_at,
    granted_tools = EXCLUDED.granted_tools
`

// UpsertToolCallResult writes chatID's call's result. It fails with
// core.ErrToolCallIDInAnotherChat unless the call exists and is chatID's.
func UpsertToolCallResult(ctx context.Context, db orm.Context, chatID string, result *core.ToolCallResult) error {
	var granted any
	if result.GrantedTools != nil {
		granted = result.GrantedTools
	}
	res, err := db.Exec(ctx, upsertToolCallResultSQL,
		result.ToolCallID, result.MessageID, result.Content,
		result.IsError, result.CreatedAt, result.UpdatedAt,
		granted, chatID,
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		// No call with this id in chatID: either another chat's call holds
		// the id, or no call was recorded at all.
		return fmt.Errorf("%w: no call %q recorded for chat %q", core.ErrToolCallIDInAnotherChat, result.ToolCallID, chatID)
	}
	return nil
}

// FindToolCallResult reads the result recorded for one call, or nil if none
// was ever written.
func FindToolCallResult(ctx context.Context, db orm.Context, toolCallID string) (*core.ToolCallResult, error) {
	row, err := GetToolCallResultByID(ctx, db, toolCallID)
	if err != nil {
		if isMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	return toolCallResultFromRow(row), nil
}

// FindToolCallResultForMessage reads the result of the call that assistant
// message messageID carries, or nil. History recovery reads this rather than
// FindToolCallResult: an id alone can name another chat's call, but a call's
// message is its own, and message ids are ours. Branched chats included — an
// inherited message keeps its id, and so does the call record pointing at it.
func FindToolCallResultForMessage(ctx context.Context, db orm.Context, toolCallID, messageID string) (*core.ToolCallResult, error) {
	row := new(ToolCallResult)
	err := db.Bun().NewSelect().Model(row).
		Join("JOIN tool_calls AS tc ON tc.id = tool_call_results.tool_call_id").
		Where("tool_call_results.tool_call_id = ?", toolCallID).
		Where("tc.message_id = ?", messageID).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if isMissing(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get tool call result for message: %w", err)
	}
	return toolCallResultFromRow(row), nil
}

// ListToolCallResultsByMessageIDs is every result whose message_id is in
// messageIDs, ordered by message then creation time.
func ListToolCallResultsByMessageIDs(ctx context.Context, db orm.Context, messageIDs []string) ([]*core.ToolCallResult, error) {
	if len(messageIDs) == 0 {
		return []*core.ToolCallResult{}, nil
	}
	rows, err := ListToolCallResult(ctx, db,
		orm.WhereIn(ToolCallResultFieldMessageID, messageIDs),
		orm.WithOrderBy(ToolCallResultFieldMessageID, orm.Asc),
		orm.WithOrderBy(ToolCallResultFieldCreatedAt, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list tool call results for messages: %w", err)
	}
	results := make([]*core.ToolCallResult, len(rows))
	for i, row := range rows {
		results[i] = toolCallResultFromRow(row)
	}
	return results, nil
}

// listToolGrantsForChatSQL: every grant a tool call recorded in one chat, per
// thread — what a load_tool result granted, written with that result
// (UpsertToolCallResult). The coarse fresh restart rebuilds each thread's
// grants from these rows, because the dead execution's in-memory record of
// them died with it. A chat is one root execution and its sub-agents, each on
// its own thread, so the thread keys the grants exactly as the workflow keys
// them. Ordered so the merge is deterministic.
const listToolGrantsForChatSQL = `
SELECT tc.thread_id AS thread_id, r.granted_tools
FROM tool_call_results r
JOIN tool_calls tc ON tc.id = r.tool_call_id
WHERE tc.chat_id = ?0
  AND tc.thread_id IS NOT NULL AND tc.thread_id <> ''
  AND cardinality(r.granted_tools) > 0
ORDER BY tc.thread_id ASC, tc.id ASC
`

// ListToolGrants is every grant a tool result in chatID recorded, one entry
// per result, ordered by thread (listToolGrantsForChatSQL).
func ListToolGrants(ctx context.Context, db orm.Context, chatID string) ([]*core.ToolGrant, error) {
	var rows []struct {
		ThreadID     string   `bun:"thread_id"`
		GrantedTools []string `bun:"granted_tools,array"`
	}
	if err := db.Bun().NewRaw(listToolGrantsForChatSQL, chatID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list tool grants: %w", err)
	}
	grants := make([]*core.ToolGrant, 0, len(rows))
	for _, row := range rows {
		grants = append(grants, &core.ToolGrant{ThreadID: row.ThreadID, Tools: row.GrantedTools})
	}
	return grants, nil
}

func toolCallResultFromRow(row *ToolCallResult) *core.ToolCallResult {
	result := &core.ToolCallResult{
		ToolCallID: row.ToolCallID,
		MessageID:  row.MessageID,
		Content:    row.Content,
		IsError:    row.IsError,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
	if len(row.GrantedTools) > 0 {
		result.GrantedTools = row.GrantedTools
	}
	return result
}

// ToolCallResultExtra carries fields on ToolCallResult that are NOT columns — computed
// values, transient state, anything derived rather than stored. ToolCallResult is
// embedded on ToolCallResultExtra (see tool_call_result_orm.go), so a field added here is a field on
// every ToolCallResult value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ToolCallResultExtra struct {
}
