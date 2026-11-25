// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// RowStores is the generated stores behind the machine, project and repo rows
// a trigger fire and the alerting sync read by id. They check no owner: every
// caller holds a row (a trigger, an event) that already names the id. Empty
// ids fail closed, as the repository reads they replace did.
type RowStores struct {
	daemons store.DaemonExtStore
}

// NewRowStores returns the row reads over the repository's database handle.
func NewRowStores(repo *db.Repo) RowStores {
	return RowStores{daemons: store.NewDaemonExtStore(repo.ORM())}
}

// GetDaemon reads a machine's record; a missing row is svcerr.NotFound.
func (s RowStores) GetDaemon(ctx context.Context, id string) (*db.Daemon, error) {
	if id == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}
	d, err := s.daemons.GetDaemon(ctx, id)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get daemon %s: %w", id, err)
	}
	return d, err
}
