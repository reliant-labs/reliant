package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type toolCallStore struct {
	q  pgdb.Querier
	db pgdb.DBTX
}

// NewToolCallStore creates the Postgres tool call store implementation.
func NewToolCallStore(q pgdb.Querier, db pgdb.DBTX) core.ToolCallStore {
	return &toolCallStore{q: q, db: db}
}

func (s *toolCallStore) UpsertToolCall(ctx context.Context, call *core.ToolCall) error {
	if call == nil {
		return fmt.Errorf("tool call cannot be nil")
	}
	return s.q.UpsertToolCall(ctx, pgdb.UpsertToolCallParams{
		ID:                  call.ID,
		ChatID:              call.ChatID,
		ThreadID:            toolCallPtrToNullString(call.ThreadID),
		MessageID:           toolCallPtrToNullString(call.MessageID),
		ToolName:            call.ToolName,
		Input:               call.Input,
		Status:              int32(call.Status),
		ErrorMessage:        toolCallPtrToNullString(call.ErrorMessage),
		ChildWorkflowID:     toolCallPtrToNullString(call.ChildWorkflowID),
		BackgroundProcessID: toolCallPtrToNullString(call.BackgroundProcessID),
		RequestedAt:         call.RequestedAt,
		StartedAt:           toolCallPtrToNullTime(call.StartedAt),
		CompletedAt:         toolCallPtrToNullTime(call.CompletedAt),
		CreatedAt:           call.CreatedAt,
		UpdatedAt:           call.UpdatedAt,
	})
}

func (s *toolCallStore) UpsertToolCallResult(ctx context.Context, result *core.ToolCallResult) error {
	if result == nil {
		return fmt.Errorf("tool call result cannot be nil")
	}
	return s.q.UpsertToolCallResult(ctx, pgdb.UpsertToolCallResultParams{
		ToolCallID: result.ToolCallID,
		MessageID:  toolCallPtrToNullString(result.MessageID),
		Content:    result.Content,
		IsError:    result.IsError,
		CreatedAt:  result.CreatedAt,
		UpdatedAt:  result.UpdatedAt,
	})
}

func (s *toolCallStore) GetToolCall(ctx context.Context, id string) (*core.ToolCall, error) {
	row, err := s.q.GetToolCall(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("tool call not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get tool call: %w", err)
	}
	return toolCallFromPG(row), nil
}

func (s *toolCallStore) GetToolCallResult(ctx context.Context, toolCallID string) (*core.ToolCallResult, error) {
	row, err := s.q.GetToolCallResult(ctx, toolCallID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get tool call result: %w", err)
	}
	return toolCallResultFromPG(row), nil
}

func (s *toolCallStore) ListToolCallsByChat(ctx context.Context, chatID string) ([]*core.ToolCall, error) {
	rows, err := s.q.ListToolCallsByChat(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list tool calls by chat: %w", err)
	}
	calls := make([]*core.ToolCall, len(rows))
	for i, row := range rows {
		calls[i] = toolCallFromPG(row)
	}
	return calls, nil
}

// ListToolCallsByIDs reads calls by primary key.
//
// This is the lookup that cannot miss: a tool-call block always carries its
// tool_call_id, whereas tool_calls.message_id is a link a writer has to
// remember to set and the live writers could not.
func (s *toolCallStore) ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*core.ToolCall, error) {
	if len(toolCallIDs) == 0 {
		return []*core.ToolCall{}, nil
	}
	rows, err := s.q.ListToolCallsByIDs(ctx, toolCallIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to list tool calls by id: %w", err)
	}
	return toolCallsFromPG(rows), nil
}

// ListStrandedSpawnToolCalls returns spawn calls whose child workflow is
// terminal but which never received a result. See the query comment for why
// this cannot be repaired by Cleanup.
func (s *toolCallStore) ListStrandedSpawnToolCalls(ctx context.Context) ([]*core.ToolCall, error) {
	rows, err := s.q.ListStrandedSpawnToolCalls(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list stranded spawn tool calls: %w", err)
	}
	calls := make([]*core.ToolCall, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, toolCallFromPG(row))
	}
	return calls, nil
}

// ListStrandedBackgroundSpawnToolCalls returns backgrounded spawn calls
// whose child workflow is terminal but which never reported back to the
// parent's mailbox. See the query comment for why the sync repair's anchor
// (tool_call_results) cannot see these.
func (s *toolCallStore) ListStrandedBackgroundSpawnToolCalls(ctx context.Context) ([]*core.StrandedBackgroundSpawn, error) {
	rows, err := s.q.ListStrandedBackgroundSpawnToolCalls(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list stranded background spawn tool calls: %w", err)
	}
	calls := make([]*core.StrandedBackgroundSpawn, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, &core.StrandedBackgroundSpawn{
			ToolCallID:     row.ToolCallID,
			ChatID:         row.ChatID,
			ParentThreadID: toolCallNullStringToPtr(row.ParentThreadID),
			ChildThreadID:  row.ChildThreadID,
			WorkflowStatus: core.WorkflowStatus{
				State:      core.WorkflowState(row.WorkflowState),
				StopReason: core.WorkflowStopReason(row.WorkflowStopReason),
			},
		})
	}
	return calls, nil
}

// ListToolCallsByMessageIDs returns every call whose message_id is in
// messageIDs, ordered by message then request time.
func (s *toolCallStore) ListToolCallsByMessageIDs(ctx context.Context, messageIDs []string) ([]*core.ToolCall, error) {
	if len(messageIDs) == 0 {
		return []*core.ToolCall{}, nil
	}
	rows, err := s.q.ListToolCallsByMessageIDs(ctx, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to list tool calls for messages: %w", err)
	}
	return toolCallsFromPG(rows), nil
}

// ListToolCallResultsByMessageIDs returns every result whose message_id is in
// messageIDs, ordered by message then creation time.
func (s *toolCallStore) ListToolCallResultsByMessageIDs(ctx context.Context, messageIDs []string) ([]*core.ToolCallResult, error) {
	if len(messageIDs) == 0 {
		return []*core.ToolCallResult{}, nil
	}
	rows, err := s.q.ListToolCallResultsByMessageIDs(ctx, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to list tool call results for messages: %w", err)
	}
	results := make([]*core.ToolCallResult, len(rows))
	for i, row := range rows {
		results[i] = toolCallResultFromPG(row)
	}
	return results, nil
}

// Mappers

func toolCallsFromPG(rows []pgdb.ToolCall) []*core.ToolCall {
	calls := make([]*core.ToolCall, len(rows))
	for i, row := range rows {
		calls[i] = toolCallFromPG(row)
	}
	return calls
}

func toolCallFromPG(row pgdb.ToolCall) *core.ToolCall {
	return &core.ToolCall{
		ID:                  row.ID,
		ChatID:              row.ChatID,
		ThreadID:            toolCallNullStringToPtr(row.ThreadID),
		MessageID:           toolCallNullStringToPtr(row.MessageID),
		ToolName:            row.ToolName,
		Input:               row.Input,
		Status:              core.ToolCallStatus(row.Status),
		ErrorMessage:        toolCallNullStringToPtr(row.ErrorMessage),
		ChildWorkflowID:     toolCallNullStringToPtr(row.ChildWorkflowID),
		BackgroundProcessID: toolCallNullStringToPtr(row.BackgroundProcessID),
		RequestedAt:         row.RequestedAt,
		StartedAt:           toolCallNullTimeToPtr(row.StartedAt),
		CompletedAt:         toolCallNullTimeToPtr(row.CompletedAt),
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

func toolCallResultFromPG(row pgdb.ToolCallResult) *core.ToolCallResult {
	return &core.ToolCallResult{
		ToolCallID: row.ToolCallID,
		MessageID:  toolCallNullStringToPtr(row.MessageID),
		Content:    row.Content,
		IsError:    row.IsError,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func toolCallPtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func toolCallNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		v := ns.String
		return &v
	}
	return nil
}

func toolCallPtrToNullTime(t *time.Time) sql.NullTime {
	if t != nil {
		return sql.NullTime{Time: *t, Valid: true}
	}
	return sql.NullTime{Valid: false}
}

func toolCallNullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		v := nt.Time
		return &v
	}
	return nil
}
