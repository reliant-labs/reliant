// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// EventStores is the generated stores behind the event, registration and
// root-status reads of this package. They are forwarded one by one rather than
// embedded: the stores also carry GetTriggerEventByDedupe, which the repository
// facade still serves for other areas, and two embedded copies would make
// that selector ambiguous.
type EventStores struct {
	events        store.TriggerEventExtStore
	registrations store.TriggerRegistrationExtStore
	workflows     store.WorkflowExtStore
}

func (s EventStores) ListStalePendingTriggerEvents(ctx context.Context, olderThan time.Time, limit int) ([]*core.TriggerEvent, error) {
	return s.events.ListStalePendingTriggerEvents(ctx, olderThan, limit)
}

func (s EventStores) SettlePendingTriggerEvent(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, def *core.TriggerEventDefinition) (bool, error) {
	return s.events.SettlePendingTriggerEvent(ctx, id, outcome, detail, def)
}

func (s EventStores) GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error) {
	return s.events.GetLatestTriggerEvent(ctx, triggerID, outcome)
}

func (s EventStores) GetTriggerRegistration(ctx context.Context, triggerID string) (*core.TriggerRegistration, error) {
	return s.registrations.GetTriggerRegistration(ctx, triggerID)
}

func (s EventStores) UpsertTriggerRegistration(ctx context.Context, reg *core.TriggerRegistration) error {
	return s.registrations.UpsertTriggerRegistration(ctx, reg)
}

func (s EventStores) RootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]core.WorkflowStatus, error) {
	return s.workflows.RootWorkflowStatusForChats(ctx, chatIDs)
}

// TriggerStores is the generated trigger store behind the whole-table reads
// and the lock and projection writes of the syncer, reconciler and alerting.
// Forwarded one by one for the same reason as EventStores.
type TriggerStores struct {
	triggers store.TriggerExtStore
}

func (s TriggerStores) SetTriggerWebhookTokenHash(ctx context.Context, id string, hash []byte) error {
	return s.triggers.SetTriggerWebhookTokenHash(ctx, id, hash)
}

func (s TriggerStores) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	return s.triggers.GetTrigger(ctx, id)
}

func (s TriggerStores) ListAllTriggers(ctx context.Context) ([]*core.Trigger, error) {
	return s.triggers.ListAllTriggers(ctx)
}

func (s TriggerStores) ListAllWorkflowTriggerActivations(ctx context.Context) ([]*core.Trigger, error) {
	return s.triggers.ListAllWorkflowTriggerActivations(ctx)
}

func (s TriggerStores) LockTrigger(ctx context.Context, id string) error {
	return s.triggers.LockTrigger(ctx, id)
}

func (s TriggerStores) SetTriggerProjection(ctx context.Context, id string, p core.TriggerProjection) error {
	return s.triggers.SetTriggerProjection(ctx, id, p)
}

// Stores is the production store of the trigger package: the repository
// facade for the reads other areas still share, joined with the generated
// stores this package owns.
type Stores struct {
	*db.Repo
	CapacityStores
	EventStores
	TriggerStores
}

// NewStores joins the repository facade with the generated trigger stores.
func NewStores(repo *db.Repo) Stores {
	if repo == nil {
		panic("triggers: NewStores requires a repository")
	}
	orm := repo.ORM()
	return Stores{
		Repo:           repo,
		CapacityStores: NewCapacityStores(orm),
		EventStores: EventStores{
			events:        store.NewTriggerEventExtStore(orm),
			registrations: store.NewTriggerRegistrationExtStore(orm),
			workflows:     store.NewWorkflowExtStore(orm),
		},
		TriggerStores: TriggerStores{triggers: store.NewTriggerExtStore(orm)},
	}
}

var (
	_ FireRepo        = Stores{}
	_ EventFireRepo   = Stores{}
	_ IntakeRepo      = Stores{}
	_ PollRepo        = Stores{}
	_ Repo            = Stores{}
	_ ProjectionStore = Stores{}
	_ AlertingStore   = Stores{}
)
