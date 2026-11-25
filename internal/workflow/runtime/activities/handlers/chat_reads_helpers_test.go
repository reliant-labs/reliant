package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// A fixture that programmed GetChatFunc on a store mock wraps the mock with the
// chat read the activities take from their store (chatviews.Of). The wrappers
// embed the mock, so every other method is the mock's.

type withChatWorkflowStatus struct {
	*MockWorkflowStatusStore
	GetChatFunc func(context.Context, string) (*db.Chat, error)
}

func (w withChatWorkflowStatus) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	return w.GetChatFunc(ctx, id)
}

type withChatLoadWorkflow struct {
	*MockLoadWorkflowStore
	GetChatFunc func(context.Context, string) (*db.Chat, error)
}

func (w withChatLoadWorkflow) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	return w.GetChatFunc(ctx, id)
}

type withChatExecuteTools struct {
	*MockExecuteToolsStore
	GetChatFunc func(context.Context, string) (*db.Chat, error)
}

func (w withChatExecuteTools) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	return w.GetChatFunc(ctx, id)
}
