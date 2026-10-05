// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"github.com/reliant-labs/reliant/internal/db/core"
)

// triggerFilter is a stored trigger's CEL filter: the triggers.filter column,
// shared by every event-driven kind and validated when the trigger is written.
// The filter is a property of the TRIGGER, not of its source (it sits beside
// the source on reliantv1.WorkflowTrigger, too).
func triggerFilter(t *core.Trigger) string { return t.Filter }
