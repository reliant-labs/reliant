// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"encoding/json"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// triggerFilter is a stored trigger's CEL filter.
//
// The filter is a property of the TRIGGER, not of its source (it sits beside
// the source on reliantv1.WorkflowTrigger, and on the triggers row). Until the
// row's filter column lands with the inbound-trigger migration, it is read from
// the config's "filter" key; afterwards this returns t.Filter.
func triggerFilter(t *core.Trigger) string {
	if len(t.Config) == 0 {
		return ""
	}
	var cfg struct {
		Filter string `json:"filter"`
	}
	if err := json.Unmarshal(t.Config, &cfg); err != nil {
		return ""
	}
	return cfg.Filter
}
