// Copyright (c) 2025 Reliant Labs
package workersetup

import "github.com/reliant-labs/reliant/internal/store"

// dispatchTriggerReads is what the workflow-event dispatcher reads of the stored
// triggers: the event owner's triggers and the launch event of a run.
type dispatchTriggerReads struct {
	store.TriggerExtStore
	store.TriggerEventExtStore
}
