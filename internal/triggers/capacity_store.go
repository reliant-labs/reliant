// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// CapacityStores is the generated capacity stores joined into CapacityStore.
// Their method sets do not overlap.
type CapacityStores struct {
	store.TriggerCapacityExtStore
	store.TriggerCapacityBindingExtStore
	store.TriggerCapacityRecoveryExtStore
	events store.TriggerEventExtStore
}

// NewCapacityStores builds the production CapacityStore over a database handle.
func NewCapacityStores(db orm.Context) CapacityStores {
	return CapacityStores{
		TriggerCapacityExtStore:         store.NewTriggerCapacityExtStore(db),
		TriggerCapacityBindingExtStore:  store.NewTriggerCapacityBindingExtStore(db),
		TriggerCapacityRecoveryExtStore: store.NewTriggerCapacityRecoveryExtStore(db),
		events:                          store.NewTriggerEventExtStore(db),
	}
}

func (c CapacityStores) ExpirePendingTriggerCapacityEvents(ctx context.Context, recordedBefore time.Time, detail string, limit int) (int64, error) {
	return c.events.ExpirePendingTriggerCapacityEvents(ctx, recordedBefore, detail, limit)
}

// Stores is the production store of the trigger package: the repository
// facade for the reads other areas still share, joined with the generated
// stores this package owns.
type Stores struct {
	*db.Repo
	CapacityStores
}

// NewStores joins the repository facade with the generated trigger stores.
func NewStores(repo *db.Repo) Stores {
	if repo == nil {
		panic("triggers: NewStores requires a repository")
	}
	return Stores{Repo: repo, CapacityStores: NewCapacityStores(repo.ORM())}
}

var (
	_ FireRepo      = Stores{}
	_ EventFireRepo = Stores{}
)

// requireCapacity is the construction-time guard: a firer or redriver built
// over a nil store fails here, not on the first capped fire.
func requireCapacity(who string, c CapacityStore) {
	if c == nil {
		panic("triggers: " + who + " requires a CapacityStore")
	}
}
