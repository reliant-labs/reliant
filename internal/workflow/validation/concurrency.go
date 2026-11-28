// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// validateConcurrency checks the trigger admission policy. The YAML parser
// enforces the same rules; this covers proto and builder callers.
func validateConcurrency(wf *reliantv1.Workflow, result *Result) {
	policy := wf.GetConcurrency()
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
	validateCancelRunning(wf, result)
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

// validateCancelRunning checks concurrency.cancel_running.
//
// It cancels the one run holding a key's slot, so it needs a key and
// max_running_per_key: 1: without a key there is no group to supersede within,
// and with a cap above one "the run holding the slot" is not one run.
//
// on_new_head reads the GitHub pull request head off the firing. A workflow
// that declares no GitHub integration trigger gets a warning, not an error: an
// activation the owner wrote by hand can still deliver GitHub events to it,
// but the likelier reading is a policy that will never cancel anything.
func validateCancelRunning(wf *reliantv1.Workflow, result *Result) {
	policy := wf.GetConcurrency()
	mode := policy.GetCancelRunning()
	switch mode {
	case reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT,
		reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD:
	default:
		return
	}
	if policy.GetKey() == "" || policy.GetMaxRunningPerKey() != 1 {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "concurrency.cancel_running",
			"cancel_running needs key and max_running_per_key: 1: it cancels the one run holding a key's slot",
			"add key (the group, e.g. {{ trigger.payload.data.pull_request.number }}) and max_running_per_key: 1, or remove cancel_running")
	}
	if mode == reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD && !declaresGitHubTrigger(wf) {
		result.AddWarning(CategoryStructure, nil, "concurrency.cancel_running",
			"cancel_running: on_new_head compares the pull request head of GitHub firings (trigger.payload.data.pull_request.head.sha), "+
				"and this workflow declares no GitHub integration trigger, so it may never cancel anything; use on_new_key_event for other sources")
	}
}

// declaresGitHubTrigger reports whether wf declares a trigger on the GitHub
// integration.
func declaresGitHubTrigger(wf *reliantv1.Workflow) bool {
	for _, wt := range wf.GetTriggers() {
		if strings.EqualFold(strings.TrimSpace(wt.GetIntegration().GetIntegration()), "github") {
			return true
		}
	}
	return false
}
