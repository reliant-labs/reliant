package postgres

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// runStore is core.RunStore over the forge ORM (internal/store's run_list.go).
type runStore struct{ db orm.Context }

// NewRunStore creates the run-list store.
func NewRunStore(db orm.Context) core.RunStore { return &runStore{db: db} }

func (s *runStore) ListRuns(ctx context.Context, filters core.RunListFilters) ([]*core.RunListItem, bool, error) {
	return store.ListRuns(ctx, s.db, filters)
}

func (s *runStore) LastRunPerWorkflow(ctx context.Context, filters core.RunListFilters) ([]*core.RunListItem, error) {
	return store.LastRunPerWorkflow(ctx, s.db, filters)
}
