// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"

	"github.com/reliant-labs/reliant/internal/chatviews"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// RepoWithChats is the repository as the tools' roles see it: the facade plus
// the chat read the roles name, which the generated chat store answers.
type RepoWithChats struct {
	*db.Repo
}

// WithChats wraps repo for a caller that builds a tool's role itself.
func WithChats(repo *db.Repo) RepoWithChats { return RepoWithChats{Repo: repo} }

// GetChat reads the chat id with its computed state.
func (r RepoWithChats) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	return chatviews.Of(r.Repo).GetChat(ctx, id)
}

// GetTriggerEventByChat reads the launch event of the given kind that started
// chatID, through the generated trigger event store.
func (r RepoWithChats) GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	return store.NewTriggerEventExtStore(r.ORM()).GetTriggerEventByChat(ctx, kind, chatID)
}

// GetTrigger reads a trigger through the generated trigger store.
func (r RepoWithChats) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	return store.NewTriggerExtStore(r.ORM()).GetTrigger(ctx, id)
}

// GetTriggerEventByChatID reads the launch event of chatID through the
// generated trigger event store.
func (r RepoWithChats) GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error) {
	return store.NewTriggerEventExtStore(r.ORM()).GetTriggerEventByChatID(ctx, chatID)
}
