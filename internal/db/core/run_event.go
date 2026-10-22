// Copyright (c) 2025 Reliant Labs
package core

import (
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
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
var ErrRunEventNotFound = svcerr.NotFound("run event")

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
