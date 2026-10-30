package validation

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestWorkflowConcurrencyRequiresPositiveLimit(t *testing.T) {
	wf := &reliantv1.Workflow{Name: "bounded", Concurrency: &reliantv1.WorkflowConcurrency{}}
	result := StaticAnalysis(wf, nil)
	found := false
	for _, issue := range result.Errors() {
		if issue.Field == "concurrency.max_running" {
			found = true
		}
	}
	require.True(t, found, "proto/builder callers must not bypass the YAML policy check")
}

func TestWorkflowConcurrencyKeyIsCheckedForProtoCallers(t *testing.T) {
	latest := reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST
	for name, tc := range map[string]struct {
		policy *reliantv1.WorkflowConcurrency
		ok     bool
	}{
		"latest keyed":         {&reliantv1.WorkflowConcurrency{MaxRunning: 1, Waiting: latest, Key: "{{ trigger.payload.attributes.repository }}"}, true},
		"latest unkeyed":       {&reliantv1.WorkflowConcurrency{MaxRunning: 1, Waiting: latest}, true},
		"key without latest":   {&reliantv1.WorkflowConcurrency{MaxRunning: 1, Key: "{{ trigger.kind }}"}, false},
		"key without {{ }}":    {&reliantv1.WorkflowConcurrency{MaxRunning: 1, Waiting: latest, Key: "repo"}, false},
		"key does not compile": {&reliantv1.WorkflowConcurrency{MaxRunning: 1, Waiting: latest, Key: "{{ trigger.payload. }}"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			result := StaticAnalysis(&reliantv1.Workflow{Name: "bounded", Concurrency: tc.policy}, nil)
			found := false
			for _, issue := range result.Errors() {
				if issue.Field == "concurrency.key" {
					found = true
				}
			}
			require.Equal(t, !tc.ok, found)
		})
	}
}
