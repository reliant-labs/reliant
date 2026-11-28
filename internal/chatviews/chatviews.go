// Copyright (c) 2025 Reliant Labs

// Package chatviews finds the chat reader of a store a caller already holds.
//
// The activities and the run-event emitter take one store value for everything
// they touch, and read a chat's computed state (activity, main thread, run)
// beside it. Of derives that read from the store they were given instead of
// widening each of their ~25 constructors: the store itself when it reads
// chats (a fake that programs GetChat), else the generated chat store over the
// store's database handle (a *db.Repo), else a reader that fails every read.
//
//forge:exclude-contract: a function over a store handle; nothing to fake behind it
package chatviews

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Chats reads a chat with its computed state, with no owner check, inside the
// ambient transaction when ctx carries one. It is the concrete value Of
// returns, so no package declares an interface the module's stores satisfy
// by accident.
type Chats struct {
	get func(ctx context.Context, id string) (*core.Chat, error)
}

// GetChat reads the chat id.
func (c Chats) GetChat(ctx context.Context, id string) (*core.Chat, error) { return c.get(ctx, id) }

// ErrNoChatReads is what the reader of a store with no chat reads answers.
var ErrNoChatReads = errors.New("chatviews: the store reads no chats")

// Of is the chat reader of s.
func Of(s any) Chats {
	if r, ok := s.(interface {
		GetChat(ctx context.Context, id string) (*core.Chat, error)
	}); ok {
		return Chats{get: r.GetChat}
	}
	if o, ok := s.(interface{ ORM() *orm.Client }); ok {
		if c := o.ORM(); c != nil {
			return Chats{get: store.NewChatsWithActivityExtStore(c).GetChat}
		}
	}
	return Chats{get: func(context.Context, string) (*core.Chat, error) { return nil, ErrNoChatReads }}
}

// AmbientMemory latches a thread's ambient-memory exclusion through the
// generated thread store over the database handle of s (a *db.Repo), or fails
// when s has none.
func AmbientMemory(s any) func(ctx context.Context, chatID, threadID string, requested bool) (bool, error) {
	if r, ok := s.(interface {
		ResolveThreadAmbientMemoryExclusion(context.Context, string, string, bool) (bool, error)
	}); ok {
		return r.ResolveThreadAmbientMemoryExclusion
	}
	if o, ok := s.(interface{ ORM() *orm.Client }); ok {
		if c := o.ORM(); c != nil {
			return store.NewThreadExtStore(c).ResolveThreadAmbientMemoryExclusion
		}
	}
	return func(context.Context, string, string, bool) (bool, error) { return false, ErrNoChatReads }
}
