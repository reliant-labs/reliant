// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/chatviews"
	"github.com/reliant-labs/reliant/internal/db"
)

// RepoWithChats is the repository as the tools' roles see it: the facade plus
// the reads the roles name that the generated stores answer (a chat, a chat's
// pending approvals, when a person last wrote to it).
type RepoWithChats struct {
	*db.Repo
}

// WithChats wraps repo for a caller that builds a tool's role itself.
func WithChats(repo *db.Repo) RepoWithChats { return RepoWithChats{Repo: repo} }

// GetChat reads the chat id with its computed state.
func (r RepoWithChats) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	return chatviews.Of(r.Repo).GetChat(ctx, id)
}

// ListPendingApprovalsByChat lists the chat's approvals that wait on a person.
func (r RepoWithChats) ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error) {
	return chatviews.PendingApprovals(ctx, r.Repo, chatID)
}

// ChatPersonTurnAt is when a person last sent a message into the chat, or nil.
func (r RepoWithChats) ChatPersonTurnAt(ctx context.Context, chatID string) (*time.Time, error) {
	return chatviews.PersonTurnAt(ctx, r.Repo, chatID)
}
