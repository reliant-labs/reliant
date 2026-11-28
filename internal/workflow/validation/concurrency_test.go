package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestWorkflowConcurrencyRequiresPositiveLimit(t *testing.T) {
	wf := &reliantv1.Workflow{Name: "bounded", Concurrency: &reliantv1.WorkflowConcurrency{}}
	result := StaticAnalysis(wf, nil)
	found := false
	for _, issue := range result.Errors() {
		if issue.Field == "concurrency" {
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

func TestWorkflowConcurrencyPriorityAndPerKeyCapForProtoCallers(t *testing.T) {
	latest := reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST
	pr := "{{ trigger.payload.pull_request.number }}"
	for _, tc := range []struct {
		name   string
		policy *reliantv1.WorkflowConcurrency
		field  string // the field that must carry an error; "" for none
	}{
		{"priority", &reliantv1.WorkflowConcurrency{MaxRunning: 2, Priority: "'main-red' in trigger.payload.labels ? 10 : 0"}, ""},
		{"priority constant", &reliantv1.WorkflowConcurrency{MaxRunning: 2, Priority: "5"}, ""},
		{"priority not numeric", &reliantv1.WorkflowConcurrency{MaxRunning: 2, Priority: "'high'"}, "concurrency.priority"},
		{"priority does not compile", &reliantv1.WorkflowConcurrency{MaxRunning: 2, Priority: "trigger.payload."}, "concurrency.priority"},
		{"priority as template", &reliantv1.WorkflowConcurrency{MaxRunning: 2, Priority: "{{ 1 }}"}, "concurrency.priority"},
		{"per-key fifo", &reliantv1.WorkflowConcurrency{MaxRunning: 4, MaxRunningPerKey: 1, Key: pr}, ""},
		{"per-key latest", &reliantv1.WorkflowConcurrency{MaxRunning: 4, MaxRunningPerKey: 1, Key: pr, Waiting: latest}, ""},
		{"per-key alone", &reliantv1.WorkflowConcurrency{MaxRunningPerKey: 1, Key: pr}, ""},
		{"per-key without key", &reliantv1.WorkflowConcurrency{MaxRunning: 4, MaxRunningPerKey: 1}, "concurrency.max_running_per_key"},
		{"per-key key does not compile", &reliantv1.WorkflowConcurrency{MaxRunningPerKey: 1, Key: "{{ trigger.payload. }}"}, "concurrency.key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := StaticAnalysis(&reliantv1.Workflow{Name: "bounded", Concurrency: tc.policy}, nil)
			var fields []string
			for _, issue := range result.Errors() {
				if strings.HasPrefix(issue.Field, "concurrency") {
					fields = append(fields, issue.Field)
				}
			}
			if tc.field == "" {
				require.Empty(t, fields)
				return
			}
			require.Contains(t, fields, tc.field)
		})
	}
}

func TestWorkflowConcurrencyCancelRunningForProtoCallers(t *testing.T) {
	onHead := reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD
	onKey := reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT
	never := reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_NEVER
	pr := "{{ trigger.payload.data.pull_request.number }}"
	github := []*reliantv1.WorkflowTrigger{{Name: "pr", Source: &reliantv1.WorkflowTrigger_Integration{Integration: &reliantv1.IntegrationSource{Integration: "github", Events: []string{"pull_request.synchronize"}}}}}
	slack := []*reliantv1.WorkflowTrigger{{Name: "msg", Source: &reliantv1.WorkflowTrigger_Integration{Integration: &reliantv1.IntegrationSource{Integration: "slack", Events: []string{"message"}}}}}
	for _, tc := range []struct {
		name     string
		policy   *reliantv1.WorkflowConcurrency
		triggers []*reliantv1.WorkflowTrigger
		err      bool
		warn     bool
	}{
		{"on_new_head with a github trigger", &reliantv1.WorkflowConcurrency{MaxRunningPerKey: 1, Key: pr, CancelRunning: onHead}, github, false, false},
		{"on_new_head without one", &reliantv1.WorkflowConcurrency{MaxRunningPerKey: 1, Key: pr, CancelRunning: onHead}, slack, false, true},
		{"on_new_key_event on any source", &reliantv1.WorkflowConcurrency{MaxRunningPerKey: 1, Key: pr, CancelRunning: onKey}, slack, false, false},
		{"never needs nothing", &reliantv1.WorkflowConcurrency{MaxRunning: 1, CancelRunning: never}, nil, false, false},
		{"no key", &reliantv1.WorkflowConcurrency{MaxRunning: 1, CancelRunning: onKey}, nil, true, false},
		{"no per-key cap", &reliantv1.WorkflowConcurrency{MaxRunning: 1, Key: pr, Waiting: reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST, CancelRunning: onKey}, nil, true, false},
		{"two per key", &reliantv1.WorkflowConcurrency{MaxRunningPerKey: 2, Key: pr, CancelRunning: onHead}, github, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := StaticAnalysis(&reliantv1.Workflow{Name: "bounded", Concurrency: tc.policy, Triggers: tc.triggers}, nil)
			has := func(issues []*Error) bool {
				for _, issue := range issues {
					if issue.Field == "concurrency.cancel_running" {
						return true
					}
				}
				return false
			}
			require.Equal(t, tc.err, has(result.Errors()), "errors: %v", result.Errors())
			require.Equal(t, tc.warn, has(result.Warnings()), "warnings: %v", result.Warnings())
		})
	}
}
