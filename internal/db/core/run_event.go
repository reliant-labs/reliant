// Copyright (c) 2025 Reliant Labs
package core

import (
	"context"
	"errors"
	"time"
)

// A RunEvent is one transition of a run that another run may react to: the
// root run finished, failed, or became blocked on an approval or a question.
// It is the outbox row workflow-event triggers are fired from (see
// internal/triggers/workflowevent), written in the same transaction as the
// state it reports so that it is exactly as durable as the transition itself.

// RunEventOutcome is the transition a RunEvent reports.
type RunEventOutcome string

const (
	// RunEventFinished is a root run that completed without declaring a
	// failure outcome.
	RunEventFinished RunEventOutcome = "finished"
	// RunEventFailed is a root run that failed, or completed into a declared
	// failure outcome.
	RunEventFailed RunEventOutcome = "failed"
	// RunEventBlocked is a run that started waiting on a human: its first
	// pending approval or question.
	RunEventBlocked RunEventOutcome = "blocked"
)

// ErrRunEventNotFound is returned when a run event lookup misses.
var ErrRunEventNotFound = errors.New("run event not found")

// RunEvent is one outbox row.
type RunEvent struct {
	ID           string
	UserID       string
	ChatID       string
	WorkflowName string
	Outcome      RunEventOutcome
	// DedupeKey identifies the transition, so a retried write is a no-op. A
	// terminal outcome is keyed by the Temporal run; a blocked outcome by the
	// approval or question that blocked it.
	DedupeKey  string
	Payload    map[string]any
	OccurredAt time.Time
	// DispatchedAt is set once the relay has handed the event to its
	// dispatch workflow; nil while it is still queued.
	DispatchedAt *time.Time
	CreatedAt    time.Time
}

// RunEventStore is the persistence contract for the run-event outbox. Every
// write honors the ambient transaction from RunTx, which is the point: the
// emitter writes inside the transaction of the state change it reports.
type RunEventStore interface {
	// CreateRunEvent inserts the event, doing nothing when its DedupeKey
	// already exists. created=false means the transition was already recorded.
	CreateRunEvent(ctx context.Context, ev *RunEvent) (created bool, err error)
	// GetRunEvent returns ErrRunEventNotFound on a miss.
	GetRunEvent(ctx context.Context, id string) (*RunEvent, error)
	// GetRunEventByDedupe returns ErrRunEventNotFound on a miss.
	GetRunEventByDedupe(ctx context.Context, dedupeKey string) (*RunEvent, error)
	// HasEnabledTriggerOfKind reports whether the user owns an enabled trigger
	// of the kind.
	HasEnabledTriggerOfKind(ctx context.Context, userID string, kind TriggerKind) (bool, error)
	// LockChatForRunEvent row-locks the chat until the transaction ends.
	LockChatForRunEvent(ctx context.Context, chatID string) error
	// CountOtherPendingBlockers counts the chat's pending approvals and
	// questions other than excludeID.
	CountOtherPendingBlockers(ctx context.Context, chatID, excludeID string) (int, error)
	// ClaimRunEvents leases up to max undispatched events until leaseUntil.
	ClaimRunEvents(ctx context.Context, now, leaseUntil time.Time, max int) ([]*RunEvent, error)
	// MarkRunEventDispatched records that the event's dispatch has started.
	MarkRunEventDispatched(ctx context.Context, id string, at time.Time) error
	// DeleteDispatchedRunEventsBefore prunes dispatched events older than
	// cutoff, returning how many were removed.
	DeleteDispatchedRunEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)
}
