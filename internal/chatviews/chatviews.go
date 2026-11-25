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

// Reader reads a chat with its computed state, with no owner check, inside the
// ambient transaction when ctx carries one.
type Reader interface {
	GetChat(ctx context.Context, id string) (*core.Chat, error)
}

// ErrNoChatReads is what the reader of a store with no chat reads answers.
var ErrNoChatReads = errors.New("chatviews: the store reads no chats")

// Of is the chat reader of s.
func Of(s any) Reader {
	if r, ok := s.(Reader); ok {
		return r
	}
	if o, ok := s.(interface{ ORM() *orm.Client }); ok {
		if c := o.ORM(); c != nil {
			return store.NewChatsWithActivityExtStore(c)
		}
	}
	return none{}
}

type none struct{}

func (none) GetChat(context.Context, string) (*core.Chat, error) { return nil, ErrNoChatReads }
