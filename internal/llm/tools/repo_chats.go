// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"

	"github.com/reliant-labs/reliant/internal/chatviews"
	"github.com/reliant-labs/reliant/internal/db"
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

// ResolveThreadAmbientMemoryExclusion latches a thread's ambient-memory
// exclusion once and answers the value in force.
func (r RepoWithChats) ResolveThreadAmbientMemoryExclusion(ctx context.Context, chatID, threadID string, requested bool) (bool, error) {
	return chatviews.AmbientMemory(r.Repo)(ctx, chatID, threadID, requested)
}
