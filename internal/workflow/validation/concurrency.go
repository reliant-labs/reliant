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
	if policy.GetMaxRunning() == 0 && policy.GetPerMember() == 0 && policy.GetMaxRunningPerKey() == 0 {
		result.AddError(CategoryStructure, nil, "concurrency", "set max_running, per_member, max_running_per_key, or a combination")
	}
	if _, err := triggerspec.CompileConcurrencyPriority(policy.GetPriority()); err != nil {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "concurrency.priority", err.Error(),
			"priority is a CEL expression over `trigger` yielding an integer, e.g. 'main-red' in trigger.payload.labels ? 10 : 0")
	}
	if policy.GetMaxRunningPerKey() > 0 && policy.GetKey() == "" {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "concurrency.max_running_per_key", "max_running_per_key needs key",
			"add key, the group whose running runs are capped, e.g. {{ trigger.payload.pull_request.number }}")
		return
	}
	latest := policy.GetWaiting() == reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST
	if policy.GetKey() != "" && !latest && policy.GetMaxRunningPerKey() == 0 {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "concurrency.key", "key applies to waiting: latest or max_running_per_key only",
			"add waiting: latest or max_running_per_key, or remove key: FIFO keeps every waiting firing")
		return
	}
	if err := triggerspec.CompileConcurrencyKey(policy.GetKey()); err != nil {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "concurrency.key", err.Error(),
			"key is a template over `trigger`, e.g. {{ trigger.payload.attributes.repository }}")
	}
}
