// Copyright (c) 2025 Reliant Labs

// Package chatupdates is the one writer of the chat_updates and user_updates
// logs and of the hub publication that follows each write (facade plan: side
// effects and emission). A refetch or typed update is appended in a transaction
// of its own, or in the caller's when ctx carries one, and published to the
// hubs with orm.AfterCommit: a client is told only after the row it should read
// has committed, and an attempt that rolls back publishes nothing.
//
// internal/architecture/chat_updates_writers_test.go still holds the single
// writer line for the chat_updates table itself: the append lives in
// internal/store, and this package is its only caller outside the facade.
//
//forge:provided
package chatupdates

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Service writes refetch signals to the chat and user streams.
type Service interface {
	// EmitChatRefetch tells clients watching chatID to re-read data of the
	// given type (chat_updates, per-chat stream).
	EmitChatRefetch(ctx context.Context, chatID string, refetchType core.RefetchType) error

	// EmitUserRefetch tells userID's clients to re-read data of the given type
	// (user_updates, global stream), scoped by opts.
	EmitUserRefetch(ctx context.Context, userID string, refetchType core.RefetchType, opts core.RefetchOpts) error

	// EmitQuestionUpdate tells clients watching chatID that a question
	// changed status (chat_updates, per-chat stream); each call is a distinct
	// entity, so the feed keeps the question's whole history.
	EmitQuestionUpdate(ctx context.Context, chatID string, update core.QuestionUpdate) error
}

// Store is the two appends. store.ChatUpdateExtStore and
// store.UserUpdateExtStore satisfy the halves.
type Store interface {
	AppendChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID, data string) (core.ChatUpdate, error)
	AppendUserUpdate(ctx context.Context, update *core.UserUpdate, callerChoseID bool) error
}

// Tx opens the transaction an append runs in. *db.Repo satisfies it, and is what
// production hands over: its RunTx is the retry ladder (it also retries 25P02,
// a COMMIT-to-rollback and 40002, which orm.Client.RunTx does not), until
// forge's TxPolicy retries them too (plan gap G12).
type Tx interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// Publisher pushes a committed update to the streaming hubs. Nil publishes
// nothing (tests, the CLI).
type Publisher interface {
	PublishChat(chatID string, seq int64, update core.ChatUpdate)
	PublishUser(update *core.UserUpdate)
}

// Deps are the service's collaborators.
type Deps struct {
	Store     Store
	Tx        Tx
	Publisher Publisher
}

// NewFromORM builds the service over db's update stores, opening its
// transactions with tx.
func NewFromORM(db orm.Context, tx Tx, publisher Publisher) Service {
	return New(Deps{
		Store:     ormStore{chats: store.NewChatUpdateExtStore(db), users: store.NewUserUpdateExtStore(db)},
		Tx:        tx,
		Publisher: publisher,
	})
}

// New builds the service over deps.
func New(deps Deps) Service {
	return &service{store: deps.Store, tx: deps.Tx, publisher: deps.Publisher}
}

type ormStore struct {
	chats store.ChatUpdateExtStore
	users store.UserUpdateExtStore
}

func (s ormStore) AppendChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID, data string) (core.ChatUpdate, error) {
	return s.chats.AppendChatUpdate(ctx, chatID, updateType, entityID, data)
}

func (s ormStore) AppendUserUpdate(ctx context.Context, update *core.UserUpdate, callerChoseID bool) error {
	return s.users.AppendUserUpdate(ctx, update, callerChoseID)
}
