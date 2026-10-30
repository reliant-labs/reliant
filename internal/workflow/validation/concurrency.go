// Copyright (c) 2025 Reliant Labs
package validation

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// validateConcurrency checks the trigger admission policy. The YAML parser
// enforces the same rules; this covers proto and builder callers.
func validateConcurrency(policy *reliantv1.WorkflowConcurrency, result *Result) {
	if policy == nil {
		return
	}
	if policy.GetMaxRunning() == 0 {
		result.AddError(CategoryStructure, nil, "concurrency.max_running", "must be positive")
	}
	latest := policy.GetWaiting() == reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST
	if policy.GetKey() != "" && !latest {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "concurrency.key", "key applies to waiting: latest only",
			"add waiting: latest, or remove key: FIFO keeps every waiting firing")
		return
	}
	if err := triggerspec.CompileConcurrencyKey(policy.GetKey()); err != nil {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "concurrency.key", err.Error(),
			"key is a template over `trigger`, e.g. {{ trigger.payload.attributes.repository }}")
	}
}
