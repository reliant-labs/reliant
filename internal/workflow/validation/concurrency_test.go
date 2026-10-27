package validation

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"testing"
)

func TestScheduledConcurrencyFailsBeforeActivation(t *testing.T) {
	wf := &reliantv1.Workflow{AutomationOnly: true, Concurrency: &reliantv1.WorkflowConcurrency{MaxRunning: 6, Key: "'pr'", Supersede: "latest"}, Triggers: []*reliantv1.WorkflowTrigger{{Source: &reliantv1.WorkflowTrigger_Schedule{Schedule: &reliantv1.ScheduleSource{}}}}}
	result := NewResult()
	validateConcurrency(wf, result)
	if !result.HasErrors() {
		t.Fatal("scheduled workflow would bypass admission")
	}
}
