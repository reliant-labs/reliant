package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	sqlitedb "github.com/reliant-labs/reliant/internal/db/sqlite/generated"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

type messageStore struct {
	q sqlitedb.Querier
}

// NewMessageStore creates the SQLite message store implementation.
func NewMessageStore(q sqlitedb.Querier) core.MessageStore {
	return &messageStore{q: q}
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
	return messageFromSQLc(sqlcMsg), nil
}

func (s *messageStore) GetNextOrdinal(ctx context.Context, threadID string) (int64, error) {
	return s.q.GetNextOrdinalByThread(ctx, threadID)
}

func (s *messageStore) ListMessages(ctx context.Context, chatID string, opts core.MessageListOptions, listContextWindowIDsByThread func(context.Context, string) ([]string, error)) ([]*core.Message, error) {
	var sqlcMsgs []sqlitedb.Message
	var err error

	if opts.ContextWindowID != nil {
		sqlcMsgs, err = s.q.GetMessagesByContextWindow(ctx, *opts.ContextWindowID)
		if err != nil {
			return nil, fmt.Errorf("failed to get messages by context window: %w", err)
		}
	} else if opts.Thread != nil && opts.ContextSequence != nil {
		sqlcMsgs, err = s.q.GetMessagesByThreadAndSequence(ctx, sqlitedb.GetMessagesByThreadAndSequenceParams{
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
			var filtered []sqlitedb.Message
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
	result := sqlcMsgs[start:end]

	return messagesFromSQLc(result), nil
}

func (s *messageStore) GetLatestMessageInThread(ctx context.Context, threadID string) (*core.Message, error) {
	msg, err := s.q.GetLatestMessageByThread(ctx, threadID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest message: %w", err)
	}
	return messageFromSQLc(msg), nil
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
	msg, err := s.q.GetLatestMessageWithTokensByThread(ctx, sqlitedb.GetLatestMessageWithTokensByThreadParams{
		ThreadID: threadID,
		Sequence: int64(contextSequence),
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest message with tokens: %w", err)
	}
	return messageFromSQLc(msg), nil
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
	return contentBlockFromSQLc(sqlcBlock), nil
}

func (s *messageStore) ListContentBlocks(ctx context.Context, messageID string) ([]*core.MessageContentBlock, error) {
	sqlcBlocks, err := s.q.ListContentBlocks(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to list content blocks: %w", err)
	}
	return contentBlocksFromSQLc(sqlcBlocks), nil
}

func (s *messageStore) ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*core.MessageContentBlock, error) {
	sqlcBlocks, err := s.q.ListContentBlocksForMessages(ctx, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to list content blocks for messages: %w", err)
	}
	return contentBlocksFromSQLc(sqlcBlocks), nil
}

func (s *messageStore) UpdateContentBlock(ctx context.Context, block *core.MessageContentBlock) error {
	return s.q.UpdateContentBlock(ctx, sqlitedb.UpdateContentBlockParams{
		ID:        block.ID,
		Content:   msgPtrToNullString(block.Content),
		ToolInput: msgPtrToNullString(block.ToolInput),
		Version:   msgIntPtrToNullInt64(block.Version),
	})
}

func (s *messageStore) AppendToContentBlock(ctx context.Context, blockID string, delta string) error {
	return s.q.AppendToContentBlock(ctx, sqlitedb.AppendToContentBlockParams{
		ID:      blockID,
		Content: sql.NullString{String: delta, Valid: true},
	})
}

func (s *messageStore) UpdateMessage(ctx context.Context, msg *core.Message) error {
	return s.q.UpdateMessage(ctx, sqlitedb.UpdateMessageParams{
		ID:         msg.ID,
		TokenCount: msgIntPtrToNullInt64(msg.TokenCount),
		Cost:       msgFloat64PtrToNullFloat64(msg.Cost),
	})
}

func messageFromSQLc(sm sqlitedb.Message) *core.Message {
	return &core.Message{
		ID:              sm.ID,
		ChatID:          sm.ChatID,
		Ordinal:         sm.Ordinal,
		ThreadID:        sm.ThreadID,
		ContextWindowID: sm.ContextWindowID,
		Role:            reliantv1.MessageRole(sm.Role),
		DisplayStyle:    msgNullInt64ToDisplayStylePtr(sm.DisplayStyle),
		Model:           msgNullStringToPtr(sm.Model),
		Agent:           msgNullStringToPtr(sm.Agent),
		TokenCount:      msgNullInt64ToIntPtr(sm.TokenCount),
		Cost:            msgNullFloat64ToPtr(sm.Cost),
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

func messagesFromSQLc(rows []sqlitedb.Message) []*core.Message {
	items := make([]*core.Message, len(rows))
	for i, row := range rows {
		items[i] = messageFromSQLc(row)
	}
	return items
}

func messageToCreateParams(msg *core.Message) sqlitedb.CreateMessageParams {
	return sqlitedb.CreateMessageParams{
		ID:              msg.ID,
		ChatID:          msg.ChatID,
		Ordinal:         msg.Ordinal,
		ThreadID:        msg.ThreadID,
		ContextWindowID: msg.ContextWindowID,
		NodeID:          msgPtrToNullString(msg.NodeID),
		NodePath:        msgPtrToNullString(msg.NodePath),
		Role:            int64(msg.Role),
		DisplayStyle:    msgDisplayStylePtrToNullInt64(msg.DisplayStyle),
		Model:           msgPtrToNullString(msg.Model),
		Agent:           msgPtrToNullString(msg.Agent),
		TokenCount:      msgIntPtrToNullInt64(msg.TokenCount),
		Cost:            msgFloat64PtrToNullFloat64(msg.Cost),
		WorkflowID:      msgPtrToNullString(msg.WorkflowID),
		RunID:           msgPtrToNullString(msg.RunID),
		ActivityID:      msgPtrToNullString(msg.ActivityID),
		CreatedAt:       msg.CreatedAt,
		UpdatedAt:       msg.UpdatedAt,
	}
}

func messageToCreateIfNotExistsParams(msg *core.Message) sqlitedb.CreateMessageIfNotExistsParams {
	return sqlitedb.CreateMessageIfNotExistsParams{
		ID:              msg.ID,
		ChatID:          msg.ChatID,
		Ordinal:         msg.Ordinal,
		ThreadID:        msg.ThreadID,
		ContextWindowID: msg.ContextWindowID,
		NodeID:          msgPtrToNullString(msg.NodeID),
		NodePath:        msgPtrToNullString(msg.NodePath),
		Role:            int64(msg.Role),
		DisplayStyle:    msgDisplayStylePtrToNullInt64(msg.DisplayStyle),
		Model:           msgPtrToNullString(msg.Model),
		Agent:           msgPtrToNullString(msg.Agent),
		TokenCount:      msgIntPtrToNullInt64(msg.TokenCount),
		Cost:            msgFloat64PtrToNullFloat64(msg.Cost),
		WorkflowID:      msgPtrToNullString(msg.WorkflowID),
		RunID:           msgPtrToNullString(msg.RunID),
		ActivityID:      msgPtrToNullString(msg.ActivityID),
		CreatedAt:       msg.CreatedAt,
		UpdatedAt:       msg.UpdatedAt,
	}
}

func contentBlockFromSQLc(sb sqlitedb.MessageContentBlock) *core.MessageContentBlock {
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

func contentBlocksFromSQLc(rows []sqlitedb.MessageContentBlock) []*core.MessageContentBlock {
	items := make([]*core.MessageContentBlock, len(rows))
	for i, row := range rows {
		items[i] = contentBlockFromSQLc(row)
	}
	return items
}

func contentBlockToCreateParams(block *core.MessageContentBlock) sqlitedb.CreateContentBlockParams {
	return sqlitedb.CreateContentBlockParams{
		ID:               block.ID,
		MessageID:        block.MessageID,
		Position:         int64(block.Position),
		BlockType:        int64(block.BlockType),
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

func contentBlockToCreateIfNotExistsParams(block *core.MessageContentBlock) sqlitedb.CreateContentBlockIfNotExistsParams {
	return sqlitedb.CreateContentBlockIfNotExistsParams{
		ID:               block.ID,
		MessageID:        block.MessageID,
		Position:         int64(block.Position),
		BlockType:        int64(block.BlockType),
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

func msgFloat64PtrToNullFloat64(f *float64) sql.NullFloat64 {
	if f != nil {
		return sql.NullFloat64{Float64: *f, Valid: true}
	}
	return sql.NullFloat64{Valid: false}
}

func msgNullFloat64ToPtr(nf sql.NullFloat64) *float64 {
	if nf.Valid {
		return &nf.Float64
	}
	return nil
}

func msgNullInt64ToDisplayStylePtr(ni sql.NullInt64) *reliantv1.DisplayStyle {
	if ni.Valid {
		v := reliantv1.DisplayStyle(ni.Int64)
		return &v
	}
	return nil
}

func msgDisplayStylePtrToNullInt64(i *reliantv1.DisplayStyle) sql.NullInt64 {
	if i != nil {
		return sql.NullInt64{Int64: int64(*i), Valid: true}
	}
	return sql.NullInt64{Valid: false}
}
