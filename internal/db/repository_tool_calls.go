// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

// toolCalls builds the tool call store against the connection or transaction
// bound to ctx.
//
// The other stores are constructed once in NewRepo and held as struct fields,
// which pins them to the raw *sql.DB. That is fine for them, but tool call
// writes are made from inside the transactions that persist a message and its
// content blocks -- a call written outside that transaction would be visible
// (or lost) independently of the message it belongs to, which is exactly the
// call/result desynchronization these tables exist to prevent. Resolving the
// DBTX per request routes the write into the ambient transaction when there
// is one; WrappedDBTX.DB falls back to the pool when there is not.
func (r *Repo) toolCalls(ctx context.Context) (core.ToolCallStore, error) {
	if r.DB == nil {
		return nil, fmt.Errorf("repository has no database connection")
	}
	dbtx := r.DB.DB(ctx)
	return postgresstore.NewToolCallStore(pgdb.New(dbtx), dbtx), nil
}

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

	store, err := r.toolCalls(ctx)
	if err != nil {
		return err
	}
	return store.UpsertToolCall(ctx, call)
}

func (r *Repo) UpsertToolCallResult(ctx context.Context, result *ToolCallResult) error {
	if result == nil {
		return fmt.Errorf("tool call result cannot be nil")
	}
	if result.ToolCallID == "" {
		return fmt.Errorf("tool call ID is required")
	}

	store, err := r.toolCalls(ctx)
	if err != nil {
		return err
	}
	return store.UpsertToolCallResult(ctx, result)
}

func (r *Repo) GetToolCall(ctx context.Context, id string) (*ToolCall, error) {
	if id == "" {
		return nil, fmt.Errorf("tool call ID cannot be empty")
	}

	store, err := r.toolCalls(ctx)
	if err != nil {
		return nil, err
	}
	return store.GetToolCall(ctx, id)
}

func (r *Repo) ListToolCallsByChat(ctx context.Context, chatID string) ([]*ToolCall, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	store, err := r.toolCalls(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListToolCallsByChat(ctx, chatID)
}

func (r *Repo) ListToolCallsByMessageIDs(ctx context.Context, messageIDs []string) ([]*ToolCall, error) {
	if len(messageIDs) == 0 {
		return []*ToolCall{}, nil
	}

	store, err := r.toolCalls(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListToolCallsByMessageIDs(ctx, messageIDs)
}

func (r *Repo) ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*ToolCall, error) {
	if len(toolCallIDs) == 0 {
		return []*ToolCall{}, nil
	}

	store, err := r.toolCalls(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListToolCallsByIDs(ctx, toolCallIDs)
}

func (r *Repo) ListStrandedSpawnToolCalls(ctx context.Context) ([]*ToolCall, error) {
	store, err := r.toolCalls(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListStrandedSpawnToolCalls(ctx)
}

func (r *Repo) ListStrandedBackgroundSpawnToolCalls(ctx context.Context) ([]*StrandedBackgroundSpawn, error) {
	store, err := r.toolCalls(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListStrandedBackgroundSpawnToolCalls(ctx)
}

func (r *Repo) ListToolCallResultsByMessageIDs(ctx context.Context, messageIDs []string) ([]*ToolCallResult, error) {
	if len(messageIDs) == 0 {
		return []*ToolCallResult{}, nil
	}

	store, err := r.toolCalls(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListToolCallResultsByMessageIDs(ctx, messageIDs)
}
