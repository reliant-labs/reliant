// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
)

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
