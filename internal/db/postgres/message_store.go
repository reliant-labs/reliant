package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

type messageStore struct {
	q  pgdb.Querier
	db pgdb.DBTX
}

// NewMessageStore creates the Postgres message store implementation.
func NewMessageStore(q pgdb.Querier, db pgdb.DBTX) core.MessageStore {
	return &messageStore{q: q, db: db}
}

func (s *messageStore) CreateMessage(ctx context.Context, msg *core.Message) error {
	return s.q.CreateMessage(ctx, messageToCreateParams(msg))
}

func (s *messageStore) CreateMessageIfNotExists(ctx context.Context, msg *core.Message) error {
	return s.q.CreateMessageIfNotExists(ctx, messageToCreateIfNotExistsParams(msg))
}

func (s *messageStore) GetMessage(ctx context.Context, id string) (*core.Message, error) {
	sqlcMsg, err := s.q.GetMessage(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("message not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	return messageFromPG(sqlcMsg), nil
}

func (s *messageStore) GetNextOrdinal(ctx context.Context, threadID string) (int64, error) {
	next, err := s.q.GetNextOrdinalByThread(ctx, threadID)
	if err != nil {
		return 0, err
	}
	return int64(next), nil
}

func (s *messageStore) ListMessages(ctx context.Context, chatID string, opts core.MessageListOptions, listContextWindowIDsByThread func(context.Context, string) ([]string, error)) ([]*core.Message, error) {
	var sqlcMsgs []pgdb.Message
	var err error

	if opts.ContextWindowID != nil {
		sqlcMsgs, err = s.q.GetMessagesByContextWindow(ctx, *opts.ContextWindowID)
		if err != nil {
			return nil, fmt.Errorf("failed to get messages by context window: %w", err)
		}
	} else if opts.Thread != nil && opts.ContextSequence != nil {
		sqlcMsgs, err = s.q.GetMessagesByThreadAndSequence(ctx, pgdb.GetMessagesByThreadAndSequenceParams{
			ThreadID: *opts.Thread,
			Sequence: int64(*opts.ContextSequence),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get messages by thread and sequence: %w", err)
		}
	} else {
		sqlcMsgs, err = s.q.ListMessages(ctx, chatID)
		if err != nil {
			return nil, fmt.Errorf("failed to list messages: %w", err)
		}

		if opts.Thread != nil {
			var filtered []pgdb.Message
			cwIDs, cwErr := listContextWindowIDsByThread(ctx, *opts.Thread)
			if cwErr == nil {
				cwIDMap := make(map[string]bool)
				for _, id := range cwIDs {
					cwIDMap[id] = true
				}
				for _, msg := range sqlcMsgs {
					if cwIDMap[msg.ContextWindowID] {
						filtered = append(filtered, msg)
					}
				}
				sqlcMsgs = filtered
			}
		}
	}

	start := opts.Offset
	if start > len(sqlcMsgs) {
		start = len(sqlcMsgs)
	}
	end := start + opts.Limit
	if end > len(sqlcMsgs) || opts.Limit == 0 {
		end = len(sqlcMsgs)
	}
	return messagesFromPG(sqlcMsgs[start:end]), nil
}

func (s *messageStore) GetLatestMessageInThread(ctx context.Context, threadID string) (*core.Message, error) {
	msg, err := s.q.GetLatestMessageByThread(ctx, threadID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest message: %w", err)
	}
	return messageFromPG(msg), nil
}

func (s *messageStore) GetLatestContextSequenceByThread(ctx context.Context, threadID string) (int64, error) {
	result, err := s.q.GetLatestContextSequenceByThread(ctx, threadID)
	if err != nil {
		return 0, err
	}
	maxSeq, ok := result.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected type for max_context_sequence: %T", result)
	}
	return maxSeq, nil
}

func (s *messageStore) GetLatestMessageWithTokensInThread(ctx context.Context, threadID string, contextSequence int) (*core.Message, error) {
	msg, err := s.q.GetLatestMessageWithTokensByThread(ctx, pgdb.GetLatestMessageWithTokensByThreadParams{
		ThreadID: threadID,
		Sequence: int64(contextSequence),
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest message with tokens: %w", err)
	}
	return messageFromPG(msg), nil
}

func (s *messageStore) CountMessagesInThread(ctx context.Context, threadID string) (int, error) {
	count, err := s.q.CountMessagesByThread(ctx, threadID)
	if err != nil {
		return 0, fmt.Errorf("failed to count messages: %w", err)
	}
	return int(count), nil
}

func (s *messageStore) CreateContentBlock(ctx context.Context, block *core.MessageContentBlock) error {
	return s.q.CreateContentBlock(ctx, contentBlockToCreateParams(block))
}

func (s *messageStore) CreateContentBlockIfNotExists(ctx context.Context, block *core.MessageContentBlock) error {
	return s.q.CreateContentBlockIfNotExists(ctx, contentBlockToCreateIfNotExistsParams(block))
}

func (s *messageStore) GetContentBlock(ctx context.Context, id string) (*core.MessageContentBlock, error) {
	sqlcBlock, err := s.q.GetContentBlock(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("content block not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get content block: %w", err)
	}
	return contentBlockFromPG(sqlcBlock), nil
}

func (s *messageStore) ListContentBlocks(ctx context.Context, messageID string) ([]*core.MessageContentBlock, error) {
	sqlcBlocks, err := s.q.ListContentBlocks(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to list content blocks: %w", err)
	}
	return contentBlocksFromPG(sqlcBlocks), nil
}

func (s *messageStore) ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*core.MessageContentBlock, error) {
	if len(messageIDs) == 0 {
		return []*core.MessageContentBlock{}, nil
	}

	// Build the IN clause with Postgres positional parameters ($1, $2, $3, ...)
	// The sqlc-generated code doesn't properly handle sqlc.slice() for Postgres
	// with database/sql - it generates IN ($1) which only matches the first ID.
	placeholders := make([]string, len(messageIDs))
	args := make([]interface{}, len(messageIDs))
	for i, id := range messageIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT id, message_id, position, block_type, content, tool_name, tool_input, tool_call_id, is_error, version, node_id, node_path, activity_id, workflow_run_id, attempt_number, thought_signature, created_at, updated_at
		FROM message_content_blocks
		WHERE message_id IN (%s)
		ORDER BY message_id, position ASC`,
		strings.Join(placeholders, ", "),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list content blocks for messages: %w", err)
	}
	defer rows.Close()

	var blocks []pgdb.MessageContentBlock
	for rows.Next() {
		var b pgdb.MessageContentBlock
		if err := rows.Scan(
			&b.ID, &b.MessageID, &b.Position, &b.BlockType,
			&b.Content, &b.ToolName, &b.ToolInput, &b.ToolCallID,
			&b.IsError, &b.Version, &b.NodeID, &b.NodePath,
			&b.ActivityID, &b.WorkflowRunID, &b.AttemptNumber,
			&b.ThoughtSignature, &b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan content block: %w", err)
		}
		blocks = append(blocks, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate content blocks: %w", err)
	}

	return contentBlocksFromPG(blocks), nil
}

func (s *messageStore) UpdateContentBlock(ctx context.Context, block *core.MessageContentBlock) error {
	return s.q.UpdateContentBlock(ctx, pgdb.UpdateContentBlockParams{
		ID:        block.ID,
		Content:   msgPtrToNullString(block.Content),
		ToolInput: msgPtrToNullString(block.ToolInput),
		Version:   msgIntPtrToNullInt64(block.Version),
	})
}

func (s *messageStore) AppendToContentBlock(ctx context.Context, blockID string, delta string) error {
	return s.q.AppendToContentBlock(ctx, pgdb.AppendToContentBlockParams{
		ID:      blockID,
		Content: sql.NullString{String: delta, Valid: true},
	})
}

func (s *messageStore) UpdateMessage(ctx context.Context, msg *core.Message) error {
	return s.q.UpdateMessage(ctx, pgdb.UpdateMessageParams{
		ID:         msg.ID,
		TokenCount: msgIntPtrToNullInt64(msg.TokenCount),
		Cost:       msgCostMicrosPtrToNullFloat64(msg.CostMicros),
	})
}

func messageFromPG(sm pgdb.Message) *core.Message {
	return &core.Message{
		ID:              sm.ID,
		ChatID:          sm.ChatID,
		Ordinal:         sm.Ordinal,
		ThreadID:        sm.ThreadID,
		ContextWindowID: sm.ContextWindowID,
		Role:            reliantv1.MessageRole(sm.Role),
		DisplayStyle:    msgNullInt32ToDisplayStylePtr(sm.DisplayStyle),
		Model:           msgNullStringToPtr(sm.Model),
		Agent:           msgNullStringToPtr(sm.Agent),
		TokenCount:      msgNullInt64ToIntPtr(sm.TokenCount),
		CostMicros:      msgNullFloat64ToCostMicrosPtr(sm.Cost),
		WorkflowID:      msgNullStringToPtr(sm.WorkflowID),
		RunID:           msgNullStringToPtr(sm.RunID),
		NodeID:          msgNullStringToPtr(sm.NodeID),
		NodePath:        msgNullStringToPtr(sm.NodePath),
		ActivityID:      msgNullStringToPtr(sm.ActivityID),
		IsStreaming:     false,
		CreatedAt:       sm.CreatedAt,
		UpdatedAt:       sm.UpdatedAt,
	}
}

func messagesFromPG(rows []pgdb.Message) []*core.Message {
	items := make([]*core.Message, len(rows))
	for i, row := range rows {
		items[i] = messageFromPG(row)
	}
	return items
}

func messageToCreateParams(msg *core.Message) pgdb.CreateMessageParams {
	return pgdb.CreateMessageParams{
		ID:              msg.ID,
		ChatID:          msg.ChatID,
		Ordinal:         msg.Ordinal,
		ThreadID:        msg.ThreadID,
		ContextWindowID: msg.ContextWindowID,
		NodeID:          msgPtrToNullString(msg.NodeID),
		NodePath:        msgPtrToNullString(msg.NodePath),
		Role:            int32(msg.Role),
		DisplayStyle:    msgDisplayStylePtrToNullInt32(msg.DisplayStyle),
		Model:           msgPtrToNullString(msg.Model),
		Agent:           msgPtrToNullString(msg.Agent),
		TokenCount:      msgIntPtrToNullInt64(msg.TokenCount),
		Cost:            msgCostMicrosPtrToNullFloat64(msg.CostMicros),
		WorkflowID:      msgPtrToNullString(msg.WorkflowID),
		RunID:           msgPtrToNullString(msg.RunID),
		ActivityID:      msgPtrToNullString(msg.ActivityID),
		CreatedAt:       msg.CreatedAt,
		UpdatedAt:       msg.UpdatedAt,
	}
}

func messageToCreateIfNotExistsParams(msg *core.Message) pgdb.CreateMessageIfNotExistsParams {
	return pgdb.CreateMessageIfNotExistsParams{
		ID:              msg.ID,
		ChatID:          msg.ChatID,
		Ordinal:         msg.Ordinal,
		ThreadID:        msg.ThreadID,
		ContextWindowID: msg.ContextWindowID,
		NodeID:          msgPtrToNullString(msg.NodeID),
		NodePath:        msgPtrToNullString(msg.NodePath),
		Role:            int32(msg.Role),
		DisplayStyle:    msgDisplayStylePtrToNullInt32(msg.DisplayStyle),
		Model:           msgPtrToNullString(msg.Model),
		Agent:           msgPtrToNullString(msg.Agent),
		TokenCount:      msgIntPtrToNullInt64(msg.TokenCount),
		Cost:            msgCostMicrosPtrToNullFloat64(msg.CostMicros),
		WorkflowID:      msgPtrToNullString(msg.WorkflowID),
		RunID:           msgPtrToNullString(msg.RunID),
		ActivityID:      msgPtrToNullString(msg.ActivityID),
		CreatedAt:       msg.CreatedAt,
		UpdatedAt:       msg.UpdatedAt,
	}
}

func contentBlockFromPG(sb pgdb.MessageContentBlock) *core.MessageContentBlock {
	return &core.MessageContentBlock{
		ID:               sb.ID,
		MessageID:        sb.MessageID,
		Position:         int(sb.Position),
		BlockType:        reliantv1.ContentBlockType(sb.BlockType),
		Content:          msgNullStringToPtr(sb.Content),
		ToolName:         msgNullStringToPtr(sb.ToolName),
		ToolInput:        msgNullStringToPtr(sb.ToolInput),
		ToolCallID:       msgNullStringToPtr(sb.ToolCallID),
		ThoughtSignature: msgNullStringToPtr(sb.ThoughtSignature),
		IsError:          msgNullBoolToPtr(sb.IsError),
		Version:          msgNullInt64ToIntPtr(sb.Version),
		ActivityID:       msgNullStringToPtr(sb.ActivityID),
		WorkflowRunID:    msgNullStringToPtr(sb.WorkflowRunID),
		AttemptNumber:    msgNullInt64ToInt(sb.AttemptNumber),
		CreatedAt:        sb.CreatedAt,
		UpdatedAt:        sb.UpdatedAt,
	}
}

func contentBlocksFromPG(rows []pgdb.MessageContentBlock) []*core.MessageContentBlock {
	items := make([]*core.MessageContentBlock, len(rows))
	for i, row := range rows {
		items[i] = contentBlockFromPG(row)
	}
	return items
}

func contentBlockToCreateParams(block *core.MessageContentBlock) pgdb.CreateContentBlockParams {
	return pgdb.CreateContentBlockParams{
		ID:               block.ID,
		MessageID:        block.MessageID,
		Position:         int64(block.Position),
		BlockType:        int32(block.BlockType),
		Content:          msgPtrToNullString(block.Content),
		ToolName:         msgPtrToNullString(block.ToolName),
		ToolInput:        msgPtrToNullString(block.ToolInput),
		ToolCallID:       msgPtrToNullString(block.ToolCallID),
		ThoughtSignature: msgPtrToNullString(block.ThoughtSignature),
		IsError:          msgPtrToNullBool(block.IsError),
		Version:          msgIntPtrToNullInt64(block.Version),
		ActivityID:       msgPtrToNullString(block.ActivityID),
		WorkflowRunID:    msgPtrToNullString(block.WorkflowRunID),
		AttemptNumber:    msgIntToNullInt64(block.AttemptNumber),
		CreatedAt:        block.CreatedAt,
		UpdatedAt:        block.UpdatedAt,
	}
}

func contentBlockToCreateIfNotExistsParams(block *core.MessageContentBlock) pgdb.CreateContentBlockIfNotExistsParams {
	return pgdb.CreateContentBlockIfNotExistsParams{
		ID:               block.ID,
		MessageID:        block.MessageID,
		Position:         int64(block.Position),
		BlockType:        int32(block.BlockType),
		Content:          msgPtrToNullString(block.Content),
		ToolName:         msgPtrToNullString(block.ToolName),
		ToolInput:        msgPtrToNullString(block.ToolInput),
		ToolCallID:       msgPtrToNullString(block.ToolCallID),
		ThoughtSignature: msgPtrToNullString(block.ThoughtSignature),
		IsError:          msgPtrToNullBool(block.IsError),
		Version:          msgIntPtrToNullInt64(block.Version),
		ActivityID:       msgPtrToNullString(block.ActivityID),
		WorkflowRunID:    msgPtrToNullString(block.WorkflowRunID),
		AttemptNumber:    msgIntToNullInt64(block.AttemptNumber),
		CreatedAt:        block.CreatedAt,
		UpdatedAt:        block.UpdatedAt,
	}
}

func msgPtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func msgNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

func msgPtrToNullBool(b *bool) sql.NullBool {
	if b != nil {
		return sql.NullBool{Bool: *b, Valid: true}
	}
	return sql.NullBool{Valid: false}
}

func msgNullBoolToPtr(nb sql.NullBool) *bool {
	if nb.Valid {
		return &nb.Bool
	}
	return nil
}

func msgIntPtrToNullInt64(i *int) sql.NullInt64 {
	if i != nil {
		return sql.NullInt64{Int64: int64(*i), Valid: true}
	}
	return sql.NullInt64{Valid: false}
}

func msgNullInt64ToIntPtr(ni sql.NullInt64) *int {
	if ni.Valid {
		v := int(ni.Int64)
		return &v
	}
	return nil
}

func msgIntToNullInt64(i int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(i), Valid: true}
}

func msgNullInt64ToInt(ni sql.NullInt64) int {
	if ni.Valid {
		return int(ni.Int64)
	}
	return 0
}

func msgInt64PtrToNullInt64(i *int64) sql.NullInt64 {
	if i != nil {
		return sql.NullInt64{Int64: *i, Valid: true}
	}
	return sql.NullInt64{Valid: false}
}

func msgNullInt64ToPtr(ni sql.NullInt64) *int64 {
	if ni.Valid {
		return &ni.Int64
	}
	return nil
}

func msgCostMicrosPtrToNullFloat64(micros *int64) sql.NullFloat64 {
	if micros != nil {
		return sql.NullFloat64{Float64: float64(*micros) / 1_000_000, Valid: true}
	}
	return sql.NullFloat64{Valid: false}
}

func msgNullFloat64ToCostMicrosPtr(cost sql.NullFloat64) *int64 {
	if cost.Valid {
		micros := int64(math.Round(cost.Float64 * 1_000_000))
		return &micros
	}
	return nil
}

func msgNullInt32ToDisplayStylePtr(ni sql.NullInt32) *reliantv1.DisplayStyle {
	if ni.Valid {
		v := reliantv1.DisplayStyle(ni.Int32)
		return &v
	}
	return nil
}

func msgDisplayStylePtrToNullInt32(i *reliantv1.DisplayStyle) sql.NullInt32 {
	if i != nil {
		return sql.NullInt32{Int32: int32(*i), Valid: true}
	}
	return sql.NullInt32{Valid: false}
}
