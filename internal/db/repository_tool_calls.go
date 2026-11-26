// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The tool call methods run on internal/store (tool_call_repo_ext.go,
// tool_call_result_repo_ext.go) with the ctx they are given, so a call or
// result written inside the transaction that persists its message joins it: a
// call written outside that transaction would be visible (or lost)
// independently of the message it belongs to, which is exactly the call/result
// desynchronization these tables exist to prevent.

// UpsertToolCall writes a tool call, replacing any existing row with the same
// id. Upsert rather than insert because the callers are Temporal activities,
// which retry: a retry that re-sent the same call must converge on the same
// row instead of failing on the primary key. A row recorded for another chat
// is never replaced: the write fails with core.ErrToolCallIDInAnotherChat.
func (r *Repo) UpsertToolCall(ctx context.Context, call *ToolCall) error {
	if call == nil {
		return fmt.Errorf("tool call cannot be nil")
	}
	if call.ID == "" {
		return fmt.Errorf("tool call ID is required")
	}
	if call.ChatID == "" {
		return fmt.Errorf("chat ID is required")
	}
	// The CHECK constraint rejects this at the database, but failing here
	// names the field instead of surfacing a constraint violation.
	if call.Status == core.ToolCallStatusCompleted && call.CompletedAt == nil {
		return fmt.Errorf("completed tool call requires completed_at")
	}
	return store.UpsertToolCall(ctx, r.ORM(), call)
}

// UpsertToolCallResult writes a result for chatID's call, replacing any
// existing row for the same call (same retry-idempotency reasoning as
// UpsertToolCall). Fails with core.ErrToolCallIDInAnotherChat unless the call
// exists and is chatID's: a result belongs to its call's chat, and the writer
// has to prove it is that chat.
func (r *Repo) UpsertToolCallResult(ctx context.Context, chatID string, result *ToolCallResult) error {
	if result == nil {
		return fmt.Errorf("tool call result cannot be nil")
	}
	if result.ToolCallID == "" {
		return fmt.Errorf("tool call ID is required")
	}
	if chatID == "" {
		return fmt.Errorf("chat ID is required")
	}
	return store.UpsertToolCallResult(ctx, r.ORM(), chatID, result)
}

// GetToolCallResultForMessage reads the result belonging to an assistant message.
//
// Deprecated: use store.FindToolCallResultForMessage with Repo.ORM().
func (r *Repo) GetToolCallResultForMessage(ctx context.Context, toolCallID, messageID string) (*ToolCallResult, error) {
	if toolCallID == "" || messageID == "" {
		return nil, fmt.Errorf("tool call ID and message ID are required")
	}
	return store.FindToolCallResultForMessage(ctx, r.ORM(), toolCallID, messageID)
}

// GetToolCallResult reads the recorded result for a call, or nil if absent.
//
// Deprecated: use store.FindToolCallResult with Repo.ORM().
func (r *Repo) GetToolCallResult(ctx context.Context, toolCallID string) (*ToolCallResult, error) {
	if toolCallID == "" {
		return nil, fmt.Errorf("tool call ID cannot be empty")
	}
	result, err := store.FindToolCallResult(ctx, r.ORM(), toolCallID)
	if err != nil {
		return nil, fmt.Errorf("failed to get tool call result: %w", err)
	}
	return result, nil
}

func (r *Repo) GetToolCall(ctx context.Context, id string) (*ToolCall, error) {
	if id == "" {
		return nil, fmt.Errorf("tool call ID cannot be empty")
	}
	call, err := store.FindToolCall(ctx, r.ORM(), id)
	if err != nil {
		return nil, fmt.Errorf("failed to get tool call: %w", err)
	}
	if call == nil {
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("tool call"), id)
	}
	return call, nil
}

// ListToolCallsByMessageIDs batches the message read path's tool-call lookup:
// loading a page of messages must not issue one query per message.
func (r *Repo) ListToolCallsByMessageIDs(ctx context.Context, messageIDs []string) ([]*ToolCall, error) {
	calls, err := store.ListToolCallsByMessageIDs(ctx, r.ORM(), messageIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to list tool calls for messages: %w", err)
	}
	return calls, nil
}

// ListLiveToolCallsByChat reads a chat's non-terminal calls (pending,
// executing, backgrounded) without touching its terminal history.
func (r *Repo) ListLiveToolCallsByChat(ctx context.Context, chatID string) ([]*ToolCall, error) {
	calls, err := store.ListLiveToolCallsByChat(ctx, r.ORM(), chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list live tool calls by chat: %w", err)
	}
	return calls, nil
}

// ListToolCallsByIDs reads calls by their own primary key.
//
// The by-message read above depends on tool_calls.message_id, which is a
// link the writer has to remember to set; when a live writer did not have a
// message in hand, that column was NULL and the by-message query could not
// see the row at all. A block always carries its tool_call_id, so keying off
// it is the lookup that cannot miss. Readers use both and merge (see
// messageproto.AssembleForDisplay).
func (r *Repo) ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*ToolCall, error) {
	calls, err := store.ListToolCallsByIDs(ctx, r.ORM(), toolCallIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to list tool calls by id: %w", err)
	}
	return calls, nil
}
