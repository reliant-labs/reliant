// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"sync"
)

// accessRefreshMemo remembers, for one request, which owners' integration
// access refreshTriggerAccess has already re-read, and with what result.
//
// A refresh is a full re-read of the owner's provider access (GitHub: the
// user, every App installation, every repository of each, paginated), about
// 1.2-1.6s against GitHub in prod. A request that touches several triggers of
// one owner (EnableWorkflow validates each declared trigger in phase 1 and
// writes each in phase 2; an agent activation prepares, then writes) re-read
// the same access once per trigger per phase: EnableWorkflow of a workflow
// declaring a handful of GitHub triggers spent ~8s of its 10s in identical
// refreshes. The first refresh in a request is as fresh as the request needs,
// so the rest reuse its outcome.
type accessRefreshMemo struct {
	mu   sync.Mutex
	done map[accessRefreshKey]error
}

type accessRefreshKey struct {
	integration string
	userID      string
}

type accessRefreshMemoKey struct{}

// withAccessRefreshMemo scopes refreshTriggerAccess's memo to ctx: every
// refresh of one owner's access to one integration under ctx runs once.
func withAccessRefreshMemo(ctx context.Context) context.Context {
	if accessRefreshMemoFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, accessRefreshMemoKey{}, &accessRefreshMemo{done: map[accessRefreshKey]error{}})
}

func accessRefreshMemoFrom(ctx context.Context) *accessRefreshMemo {
	m, _ := ctx.Value(accessRefreshMemoKey{}).(*accessRefreshMemo)
	return m
}

// refresh runs fn once per key for the memo's lifetime and returns its result,
// the first call's for every later one. The lock is held across fn so that
// concurrent callers for one key wait for the single refresh rather than
// starting their own.
func (m *accessRefreshMemo) refresh(key accessRefreshKey, fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.done[key]; ok {
		return err
	}
	err := fn()
	m.done[key] = err
	return err
}
