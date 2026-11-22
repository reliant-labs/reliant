// Copyright (c) 2025 Reliant Labs

// Package projectaccesstest builds projectaccess services for the tests of the
// handlers that take one. Imported by _test.go files only.
//
// A test that used to program GetProjectWithUserCheck on a mock repository now
// binds the same function to that mock (Bound, Bind) and hands the handler
// For(repo) as its Projects dependency: the function stays next to the
// fixture that owns the repository, and the handler reads it through the
// narrow interface it declares.
//
//forge:exclude-contract: test helpers over the projectaccess contract; nothing to fake behind them
package projectaccesstest

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectaccess"
)

// ReadFunc is the project read of a fixture.
type ReadFunc = func(ctx context.Context, id, userID string) (*core.Project, error)

var bound sync.Map // repository pointer -> *projectaccess.MockService

// hashable reports whether repo can key bound (a struct value holding a func
// cannot).
func hashable(repo any) bool { return repo != nil && reflect.TypeOf(repo).Comparable() }

// Bind answers For(repo)'s reads from fn and returns the service.
func Bind(repo any, fn ReadFunc) *projectaccess.MockService {
	if !hashable(repo) {
		return &projectaccess.MockService{GetProjectWithUserCheckFunc: fn}
	}
	if v, ok := bound.Load(repo); ok {
		svc := v.(*projectaccess.MockService)
		svc.GetProjectWithUserCheckFunc = fn
		return svc
	}
	svc := &projectaccess.MockService{GetProjectWithUserCheckFunc: fn}
	bound.Store(repo, svc)
	return svc
}

// Bound is Bind returning repo, for a fixture that builds its repository in
// one expression.
func Bound[R any](repo R, fn ReadFunc) R {
	Bind(repo, fn)
	return repo
}

// FuncOf is the read bound to repo, for a fixture that wraps it.
func FuncOf(repo any) ReadFunc {
	if m, ok := For(repo).(*projectaccess.MockService); ok && m.GetProjectWithUserCheckFunc != nil {
		return m.GetProjectWithUserCheckFunc
	}
	return For(repo).GetProjectWithUserCheck
}

// CallCount is how many reads For(repo) answered.
func CallCount(repo any) int {
	if !hashable(repo) {
		return 0
	}
	if v, ok := bound.Load(repo); ok {
		return v.(*projectaccess.MockService).CallCount("GetProjectWithUserCheck")
	}
	return 0
}

// For is the Projects dependency of a handler built over repo: the read bound
// to it, else the repository's own GetProjectWithUserCheck when it has one
// (a hand fixture), else the real service over its ORM handle (*db.Repo), else
// a service that fails every read, so an unprogrammed one is loud.
func For(repo any) projectaccess.Service {
	if hashable(repo) {
		if v, ok := bound.Load(repo); ok {
			return v.(*projectaccess.MockService)
		}
	}
	if r, ok := repo.(interface {
		GetProjectWithUserCheck(ctx context.Context, id, userID string) (*core.Project, error)
	}); ok {
		return &projectaccess.MockService{GetProjectWithUserCheckFunc: r.GetProjectWithUserCheck}
	}
	if r, ok := repo.(interface{ ORM() *orm.Client }); ok && r.ORM() != nil {
		return projectaccess.NewFromORM(r.ORM())
	}
	return Failing(errors.New("projectaccesstest: no project read is bound to this repository"))
}

// Func answers every read from fn.
func Func(fn ReadFunc) projectaccess.Service {
	return &projectaccess.MockService{GetProjectWithUserCheckFunc: fn}
}

// Failing answers every read with err.
func Failing(err error) projectaccess.Service {
	return Func(func(context.Context, string, string) (*core.Project, error) { return nil, err })
}
