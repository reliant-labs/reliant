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
// that arrives first, before the call it describes goes out. Every ledger
// call is one statement run with orm.Detached(ctx): on the pool, as its own
// autocommit transaction, committed before the call returns, so a caller's
// rollback cannot take a ledger entry with it.
func (r *Repo) IntegrationWrites() IntegrationWriteLedger {
	return IntegrationWriteLedger{orm: r.ORM()}
}

// IntegrationWriteLedger is the write ledger, over the store's ledger
// queries. Its consumers declare the slice of it they use.
type IntegrationWriteLedger struct{ orm *orm.Client }

// CreateIntegrationWrite records w. It is committed on its own, before the
// call it describes, so it is visible to an event that arrives first.
func (l IntegrationWriteLedger) CreateIntegrationWrite(ctx context.Context, w *core.IntegrationWrite) error {
	if err := store.InsertIntegrationWrite(orm.Detached(ctx), l.orm, w); err != nil {
		return fmt.Errorf("create integration write: %w", err)
	}
	return nil
}

// CompleteIntegrationWrite settles an intent row. It reports whether it
// did: a row already settled is left as it is.
func (l IntegrationWriteLedger) CompleteIntegrationWrite(ctx context.Context, id string, res core.IntegrationWriteResult) (bool, error) {
	at := res.At
	if at.IsZero() {
		at = time.Now()
	}
	settled, err := store.CompleteIntegrationWrite(orm.Detached(ctx), l.orm, id, res, at)
	if err != nil {
		return false, fmt.Errorf("complete integration write: %w", err)
	}
	return settled, nil
}

// GetIntegrationWriteByObject finds the completed write that produced or
// touched object (kind, id) in scope, newest first; ErrIntegrationWriteNotFound
// on a miss.
func (l IntegrationWriteLedger) GetIntegrationWriteByObject(ctx context.Context, integration, scope, objectKind, objectID string) (*core.IntegrationWrite, error) {
	w, err := store.GetIntegrationWriteByObject(orm.Detached(ctx), l.orm, integration, scope, objectKind, objectID)
	if err != nil {
		if errors.Is(err, core.ErrIntegrationWriteNotFound) {
			return nil, core.ErrIntegrationWriteNotFound
		}
		return nil, fmt.Errorf("get integration write by object: %w", err)
	}
	return w, nil
}

// ListRecentIntegrationWrites lists writes into scope since since that
// could still be the cause of an arriving event (intent, done, or failed
// with an unknown outcome), newest first.
func (l IntegrationWriteLedger) ListRecentIntegrationWrites(ctx context.Context, integration, scope string, since time.Time, limit int) ([]*core.IntegrationWrite, error) {
	if limit <= 0 {
		limit = 50
	}
	ws, err := store.ListRecentIntegrationWrites(orm.Detached(ctx), l.orm, integration, scope, since, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent integration writes: %w", err)
	}
	return ws, nil
}

// PruneIntegrationWrites deletes entries created before before.
func (l IntegrationWriteLedger) PruneIntegrationWrites(ctx context.Context, before time.Time) (int64, error) {
	n, err := store.PruneIntegrationWrites(orm.Detached(ctx), l.orm, before)
	if err != nil {
		return 0, fmt.Errorf("prune integration writes: %w", err)
	}
	return n, nil
}
