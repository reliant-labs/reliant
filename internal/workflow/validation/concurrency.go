package validation

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

func validateConcurrency(wf *reliantv1.Workflow, result *Result) {
	if wf.GetConcurrency() == nil {
		return
	}
	for _, t := range wf.GetTriggers() {
		if t.GetSchedule() != nil {
			result.AddError(CategoryStructure, nil, "concurrency", "scheduled concurrency is not supported in stage 1; enqueue through an inbound activation instead")
		}
	}
	if !wf.GetAutomationOnly() {
		result.AddError(CategoryStructure, nil, "concurrency", "concurrency is supported only for automation_only workflows")
	}
	if err := triggerspec.CompileConcurrency(wf.Concurrency); err != nil {
		result.AddError(CategoryStructure, nil, "concurrency", err.Error())
	}
}
