// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// LaunchEvents reads the launch event of a chat: what started a run decides
// whether its machine may be woken with the automation token.
type LaunchEvents interface {
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
}

// StatusTriggers is what a finished run's status write reads of the trigger
// that launched it: whether it opted in to completion notices, and the
// failure episode its firings form.
type StatusTriggers interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
}

// TriggerReads answers every trigger read the activities make
// (AttendanceTriggers, LaunchEvents, StatusTriggers) from the generated trigger
// stores, which share no method names. A role is handed the whole value; each
// takes only the part it declares.
type TriggerReads struct {
	store.TriggerExtStore
	store.TriggerEventExtStore
}

// NewTriggerReads is the TriggerReads over db.
func NewTriggerReads(db orm.Context) TriggerReads {
	return TriggerReads{TriggerExtStore: store.NewTriggerExtStore(db), TriggerEventExtStore: store.NewTriggerEventExtStore(db)}
}
