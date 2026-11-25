// Copyright (c) 2025 Reliant Labs

// Package chataccess is the one place a caller's right to WRITE a chat is
// decided and its row read in a single call: the owner-filtered store read,
// then authz's org-aware decision on the row it returned.
//
// The store read alone filters on the owner, which is not enough once a chat
// belongs to an org: an owner who has left the org, a credential bound to
// another org, or an outage of the membership answer must all read as "no such
// chat". Handlers that held *db.Repo got this from Repo.GetChatWithUserCheck
// and Repo.GetChatRowWithUserCheck; a handler that calls the store directly
// would skip it, so they take this package instead and the check-plus-read
// stays one call (facade plan §1.1).
//
// Every refusal is core.ErrChatNotFound, so a caller cannot probe which ids
// exist: another user's chat, a missing one and one authz refuses read alike.
//
//forge:provided
package chataccess

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Service reads a chat the caller may write.
type Service interface {
	// GetChatRowWithUserCheck returns the chat's own row when userID owns it
	// and may write it (in its org, through this ctx's credential), else
	// core.ErrChatNotFound. An empty id or userID is an error, never a read.
	GetChatRowWithUserCheck(ctx context.Context, id, userID string) (*core.ChatRow, error)

	// GetChatWithUserCheck is GetChatRowWithUserCheck over the activity view,
	// for a caller that needs a derived field (state, activity, root workflow
	// status).
	GetChatWithUserCheck(ctx context.Context, id, userID string) (*core.Chat, error)
}

// Store is the owner-filtered chat reads. store.ChatExtStore and
// store.ChatsWithActivityExtStore satisfy the two halves.
type Store interface {
	// GetChatRowWithUserCheck returns the chat row when it is the user's,
	// else core.ErrChatNotFound. It decides nothing about the chat's org.
	GetChatRowWithUserCheck(ctx context.Context, id, userID string) (*core.ChatRow, error)
	// GetChatWithUserCheck is the same over the activity view.
	GetChatWithUserCheck(ctx context.Context, id, userID string) (*core.Chat, error)
}

// Deps are the service's collaborators.
type Deps struct {
	Store Store
}

// NewFromORM builds the service over the owner-filtered reads of db's chats.
func NewFromORM(db orm.Context) Service {
	return New(Deps{Store: ormStore{
		rows:  store.NewChatExtStore(db),
		views: store.NewChatsWithActivityExtStore(db),
	}})
}

// New builds the service over deps.
func New(deps Deps) Service {
	return &service{store: deps.Store}
}

// ormStore joins the two generated stores into Store.
type ormStore struct {
	rows  store.ChatExtStore
	views store.ChatsWithActivityExtStore
}

func (s ormStore) GetChatRowWithUserCheck(ctx context.Context, id, userID string) (*core.ChatRow, error) {
	return s.rows.GetChatRowWithUserCheck(ctx, id, userID)
}

func (s ormStore) GetChatWithUserCheck(ctx context.Context, id, userID string) (*core.Chat, error) {
	return s.views.GetChatWithUserCheck(ctx, id, userID)
}
