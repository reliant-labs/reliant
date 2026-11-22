// Copyright (c) 2025 Reliant Labs
package writeledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// LedgerQueries is the ledger's queries. store.IntegrationWriteExtStore
// satisfies it.
type LedgerQueries interface {
	InsertIntegrationWrite(ctx context.Context, w *core.IntegrationWrite) error
	CompleteIntegrationWrite(ctx context.Context, id string, res core.IntegrationWriteResult, at time.Time) (bool, error)
	GetIntegrationWriteByObject(ctx context.Context, integration, scope, objectKind, objectID string) (*core.IntegrationWrite, error)
	ListRecentIntegrationWrites(ctx context.Context, integration, scope string, since time.Time, limit int) ([]*core.IntegrationWrite, error)
	PruneIntegrationWrites(ctx context.Context, before time.Time) (int64, error)
}

// Ledger is the write ledger over its queries. It never joins an ambient
// transaction: an intent row must be committed, and so visible to an event
// that arrives first, before the call it describes goes out. Every call is
// one statement run with orm.Detached(ctx): on the pool, as its own
// autocommit transaction, committed before the call returns, so a caller's
// rollback cannot take a ledger entry with it. It satisfies Store, Pruner and
// triggers.LedgerReader.
type Ledger struct{ q LedgerQueries }

// NewLedger returns the ledger over q.
func NewLedger(q LedgerQueries) *Ledger { return &Ledger{q: q} }

// CreateIntegrationWrite records w. It is committed on its own, before the
// call it describes, so it is visible to an event that arrives first.
func (l *Ledger) CreateIntegrationWrite(ctx context.Context, w *core.IntegrationWrite) error {
	if err := l.q.InsertIntegrationWrite(orm.Detached(ctx), w); err != nil {
		return fmt.Errorf("create integration write: %w", err)
	}
	return nil
}

// CompleteIntegrationWrite settles an intent row. It reports whether it did:
// a row already settled is left as it is.
func (l *Ledger) CompleteIntegrationWrite(ctx context.Context, id string, res core.IntegrationWriteResult) (bool, error) {
	at := res.At
	if at.IsZero() {
		at = time.Now()
	}
	settled, err := l.q.CompleteIntegrationWrite(orm.Detached(ctx), id, res, at)
	if err != nil {
		return false, fmt.Errorf("complete integration write: %w", err)
	}
	return settled, nil
}

// GetIntegrationWriteByObject finds the completed write that produced or
// touched object (kind, id) in scope, newest first;
// core.ErrIntegrationWriteNotFound on a miss.
func (l *Ledger) GetIntegrationWriteByObject(ctx context.Context, integration, scope, objectKind, objectID string) (*core.IntegrationWrite, error) {
	w, err := l.q.GetIntegrationWriteByObject(orm.Detached(ctx), integration, scope, objectKind, objectID)
	if err != nil {
		if errors.Is(err, core.ErrIntegrationWriteNotFound) {
			return nil, core.ErrIntegrationWriteNotFound
		}
		return nil, fmt.Errorf("get integration write by object: %w", err)
	}
	return w, nil
}

// ListRecentIntegrationWrites lists writes into scope since since that could
// still be the cause of an arriving event (intent, done, or failed with an
// unknown outcome), newest first. A limit of zero or less is 50.
func (l *Ledger) ListRecentIntegrationWrites(ctx context.Context, integration, scope string, since time.Time, limit int) ([]*core.IntegrationWrite, error) {
	if limit <= 0 {
		limit = 50
	}
	ws, err := l.q.ListRecentIntegrationWrites(orm.Detached(ctx), integration, scope, since, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent integration writes: %w", err)
	}
	return ws, nil
}

// PruneIntegrationWrites deletes entries created before before.
func (l *Ledger) PruneIntegrationWrites(ctx context.Context, before time.Time) (int64, error) {
	n, err := l.q.PruneIntegrationWrites(orm.Detached(ctx), before)
	if err != nil {
		return 0, fmt.Errorf("prune integration writes: %w", err)
	}
	return n, nil
}
