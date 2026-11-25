// Copyright (c) 2025 Reliant Labs

// Package chataccesstest builds chataccess services for the tests of the
// handlers that take one. Imported by _test.go files only.
//
// A test that used to program GetChatWithUserCheck on a mock repository now
// binds the same function to that mock (Bind) and hands the handler
// For(repo) as its Chats dependency: the function stays next to the fixture
// that owns the repository, and the handler reads it through the narrow
// interface it declares.
//
//forge:exclude-contract: test helpers over the chataccess contract; nothing to fake behind them
package chataccesstest

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// RowFunc is the chat-row read of a fixture; ViewFunc the activity-view read.
type (
	RowFunc  = func(ctx context.Context, id, userID string) (*core.ChatRow, error)
	ViewFunc = func(ctx context.Context, id, userID string) (*core.Chat, error)
)

var bound sync.Map // repository pointer -> *chataccess.MockService

func hashable(repo any) bool { return repo != nil && reflect.TypeOf(repo).Comparable() }

// Bind answers For(repo)'s reads from row and view (either may be nil) and
// returns the service. A nil read stays whatever it was.
func Bind(repo any, row RowFunc, view ViewFunc) *chataccess.MockService {
	var svc *chataccess.MockService
	if hashable(repo) {
		if v, ok := bound.Load(repo); ok {
			svc = v.(*chataccess.MockService)
		}
	}
	if svc == nil {
		svc = &chataccess.MockService{}
		if hashable(repo) {
			bound.Store(repo, svc)
		}
	}
	if row != nil {
		svc.GetChatRowWithUserCheckFunc = row
	}
	if view != nil {
		svc.GetChatWithUserCheckFunc = view
	}
	return svc
}

// Bound is Bind returning repo, for a fixture that builds its repository in
// one expression.
func Bound[R any](repo R, row RowFunc, view ViewFunc) R {
	Bind(repo, row, view)
	return repo
}

// For is the Chats dependency of a handler built over repo: the reads bound to
// it, else the repository's own methods when it has them (a hand fixture),
// else the real service over its ORM handle (*db.Repo), else a service that
// fails every read, so an unprogrammed one is loud.
func For(repo any) chataccess.Service {
	if hashable(repo) {
		if v, ok := bound.Load(repo); ok {
			return v.(*chataccess.MockService)
		}
	}
	if r, ok := repo.(interface{ ORM() *orm.Client }); ok && r.ORM() != nil {
		return chataccess.NewFromORM(r.ORM())
	}
	svc := &chataccess.MockService{}
	if r, ok := repo.(interface {
		GetChatRowWithUserCheck(ctx context.Context, id, userID string) (*core.ChatRow, error)
	}); ok {
		svc.GetChatRowWithUserCheckFunc = r.GetChatRowWithUserCheck
	}
	if r, ok := repo.(interface {
		GetChatWithUserCheck(ctx context.Context, id, userID string) (*core.Chat, error)
	}); ok {
		svc.GetChatWithUserCheckFunc = r.GetChatWithUserCheck
	}
	if svc.GetChatRowWithUserCheckFunc != nil || svc.GetChatWithUserCheckFunc != nil {
		return svc
	}
	return Failing(errors.New("chataccesstest: no chat read is bound to this repository"))
}

// Func answers every read from the given functions.
func Func(row RowFunc, view ViewFunc) chataccess.Service {
	return &chataccess.MockService{GetChatRowWithUserCheckFunc: row, GetChatWithUserCheckFunc: view}
}

// Failing answers every read with err.
func Failing(err error) chataccess.Service {
	return Func(
		func(context.Context, string, string) (*core.ChatRow, error) { return nil, err },
		func(context.Context, string, string) (*core.Chat, error) { return nil, err })
}
