// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// IntegrationWrites returns the write ledger. It never joins an ambient
// transaction: an intent row must be committed, and so visible to an event
// that arrives first, before the call it describes goes out.
func (r *Repo) IntegrationWrites() core.IntegrationWriteStore {
	return integrationWriteLedger{orm: r.ORM()}
}

// integrationWriteLedger is core.IntegrationWriteStore over the store's
// ledger queries.
type integrationWriteLedger struct{ orm *orm.Client }

// own runs fn outside any transaction ctx carries. Called inside one, the
// statement runs in a detached transaction of its own, committed before own
// returns, so the caller's rollback cannot take a ledger entry with it.
func (l integrationWriteLedger) own(ctx context.Context, fn func(db orm.Context) error) error {
	if !orm.InTx(ctx) {
		return fn(l.orm)
	}
	tx, err := l.orm.BeginDetached(ctx, orm.TxOptions{Isolation: orm.IsolationReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (l integrationWriteLedger) CreateIntegrationWrite(ctx context.Context, w *core.IntegrationWrite) error {
	if err := l.own(ctx, func(db orm.Context) error { return store.InsertIntegrationWrite(ctx, db, w) }); err != nil {
		return fmt.Errorf("create integration write: %w", err)
	}
	return nil
}

func (l integrationWriteLedger) CompleteIntegrationWrite(ctx context.Context, id string, res core.IntegrationWriteResult) (bool, error) {
	at := res.At
	if at.IsZero() {
		at = time.Now()
	}
	var settled bool
	err := l.own(ctx, func(db orm.Context) (err error) {
		settled, err = store.CompleteIntegrationWrite(ctx, db, id, res, at)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("complete integration write: %w", err)
	}
	return settled, nil
}

func (l integrationWriteLedger) GetIntegrationWriteByObject(ctx context.Context, integration, scope, objectKind, objectID string) (*core.IntegrationWrite, error) {
	var w *core.IntegrationWrite
	err := l.own(ctx, func(db orm.Context) (err error) {
		w, err = store.GetIntegrationWriteByObject(ctx, db, integration, scope, objectKind, objectID)
		return err
	})
	if err != nil {
		if errors.Is(err, core.ErrIntegrationWriteNotFound) {
			return nil, core.ErrIntegrationWriteNotFound
		}
		return nil, fmt.Errorf("get integration write by object: %w", err)
	}
	return w, nil
}

func (l integrationWriteLedger) ListRecentIntegrationWrites(ctx context.Context, integration, scope string, since time.Time, limit int) ([]*core.IntegrationWrite, error) {
	if limit <= 0 {
		limit = 50
	}
	var ws []*core.IntegrationWrite
	err := l.own(ctx, func(db orm.Context) (err error) {
		ws, err = store.ListRecentIntegrationWrites(ctx, db, integration, scope, since, limit)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list recent integration writes: %w", err)
	}
	return ws, nil
}

func (l integrationWriteLedger) PruneIntegrationWrites(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := l.own(ctx, func(db orm.Context) (err error) {
		n, err = store.PruneIntegrationWrites(ctx, db, before)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("prune integration writes: %w", err)
	}
	return n, nil
}
