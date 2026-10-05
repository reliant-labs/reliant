// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ActorUser, ActorWorker and ActorTrigger name who caused a connection event,
// so shared use stays attributable per user.
func ActorUser(userID string) string { return "user:" + userID }

// ActorWorker is the actor for refreshes and uses made by the worker.
const ActorWorker = "worker"

// ActorTrigger is the actor for a use made by an unattended trigger fire.
func ActorTrigger(triggerID string) string { return "trigger:" + triggerID }

func userEvent(kind, userID string) core.ConnectionEvent {
	return core.ConnectionEvent{Kind: kind, UserID: userID, Actor: ActorUser(userID)}
}

func workerEvent(kind, userID string) core.ConnectionEvent {
	return core.ConnectionEvent{Kind: kind, UserID: userID, Actor: ActorWorker}
}

// eventLog is the append-only audit sink. Appending a "used" event must never
// fail a call that already succeeded, so it only logs.
type eventLog interface {
	AppendConnectionEvent(ctx context.Context, ev core.ConnectionEvent) error
}

func appendBestEffort(ctx context.Context, log eventLog, ev core.ConnectionEvent) {
	if err := log.AppendConnectionEvent(context.WithoutCancel(ctx), ev); err != nil {
		slog.Warn("connection audit event not recorded", "kind", ev.Kind, "connection_id", ev.ConnectionID, "error", err)
	}
}
