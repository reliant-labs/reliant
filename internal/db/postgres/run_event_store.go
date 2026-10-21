package postgres

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// runEventStore is core.RunEventStore over the forge ORM (internal/store's
// run_event_repo_ext.go). Every call joins the transaction ctx carries.
type runEventStore struct{ db orm.Context }

// NewRunEventStore creates the run-event outbox implementation.
func NewRunEventStore(db orm.Context) core.RunEventStore { return &runEventStore{db: db} }

func (s *runEventStore) CreateRunEvent(ctx context.Context, ev *core.RunEvent) (bool, error) {
	return store.InsertRunEvent(ctx, s.db, ev)
}

func (s *runEventStore) GetRunEvent(ctx context.Context, id string) (*core.RunEvent, error) {
	return store.LoadRunEvent(ctx, s.db, id)
}

func (s *runEventStore) GetRunEventByDedupe(ctx context.Context, dedupeKey string) (*core.RunEvent, error) {
	return store.LoadRunEventByDedupe(ctx, s.db, dedupeKey)
}

func (s *runEventStore) HasEnabledTriggerOfKind(ctx context.Context, userID string, kind core.TriggerKind) (bool, error) {
	return store.HasEnabledTriggerOfKind(ctx, s.db, userID, kind)
}

func (s *runEventStore) LockChatForRunEvent(ctx context.Context, chatID string) error {
	return store.LockChatForRunEvent(ctx, s.db, chatID)
}

func (s *runEventStore) CountOtherPendingBlockers(ctx context.Context, chatID, excludeID string) (int, error) {
	return store.CountOtherPendingBlockers(ctx, s.db, chatID, excludeID)
}

func (s *runEventStore) ClaimRunEvents(ctx context.Context, now, leaseUntil time.Time, max int) ([]*core.RunEvent, error) {
	return store.ClaimRunEvents(ctx, s.db, now, leaseUntil, max)
}

func (s *runEventStore) MarkRunEventDispatched(ctx context.Context, id string, at time.Time) error {
	return store.MarkRunEventDispatched(ctx, s.db, id, at)
}

func (s *runEventStore) DeleteDispatchedRunEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return store.DeleteDispatchedRunEventsBefore(ctx, s.db, cutoff)
}
