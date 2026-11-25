// Copyright (c) 2025 Reliant Labs
// Package triggertest writes trigger rows for tests the way the trigger
// handler does: through the generated store, in one READ COMMITTED
// transaction, under the owner's org lock and with the change recorded.
//
// It imports internal/store and not internal/db, so a test inside either
// package can use it.
package triggertest

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Database is anything that hands out the ORM client: *db.Repo, or a
// storetest environment's repository.
type Database interface {
	ORM() *orm.Client
}

// Create inserts t with no org reads, as the facade's CreateTrigger did.
func Create(ctx context.Context, d Database, t *core.Trigger) error {
	return write(ctx, d, func(ctx context.Context, w store.TriggerWritesExtStore) error {
		return w.CreateTriggerInOrg(ctx, t)
	})
}

// Update replaces t's definition with no org reads, as the facade's
// UpdateTrigger did.
func Update(ctx context.Context, d Database, t *core.Trigger) error {
	return write(ctx, d, func(ctx context.Context, w store.TriggerWritesExtStore) error {
		return w.UpdateTriggerInOrg(ctx, t)
	})
}

func write(ctx context.Context, d Database, fn func(context.Context, store.TriggerWritesExtStore) error) error {
	client := d.ORM()
	return client.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return fn(ctx, store.NewTriggerWritesExtStore(client))
	})
}
