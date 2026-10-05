// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestFromWorkflowTriggerAgreesWithFromDefinition(t *testing.T) {
	// One rule set: a declaration is valid exactly when the same source on a
	// stored trigger would be.
	every := "1h"
	cases := []struct {
		name string
		wt   *reliantv1.WorkflowTrigger
		def  *reliantv1.TriggerDefinition
		kind core.TriggerKind
		err  string
	}{
		{
			name: "schedule",
			wt:   &reliantv1.WorkflowTrigger{Source: &reliantv1.WorkflowTrigger_Schedule{Schedule: &reliantv1.ScheduleSource{Cron: []string{"0 9 * * *"}}}},
			def:  &reliantv1.TriggerDefinition{Source: &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{Cron: []string{"0 9 * * *"}}}},
			kind: core.TriggerKindSchedule,
		},
		{
			name: "bad cron",
			wt:   &reliantv1.WorkflowTrigger{Source: &reliantv1.WorkflowTrigger_Schedule{Schedule: &reliantv1.ScheduleSource{Cron: []string{"0 9 * *"}}}},
			def:  &reliantv1.TriggerDefinition{Source: &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{Cron: []string{"0 9 * *"}}}},
			err:  "want 5 fields",
		},
		{
			name: "bad timezone",
			wt:   &reliantv1.WorkflowTrigger{Source: &reliantv1.WorkflowTrigger_Schedule{Schedule: &reliantv1.ScheduleSource{Interval: &every, Timezone: "Mars/Olympus"}}},
			def:  &reliantv1.TriggerDefinition{Source: &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{Interval: &every, Timezone: "Mars/Olympus"}}},
			err:  "unknown IANA time zone",
		},
		{
			name: "webhook",
			wt:   &reliantv1.WorkflowTrigger{Source: &reliantv1.WorkflowTrigger_Webhook{Webhook: &reliantv1.WebhookSource{}}},
			def:  &reliantv1.TriggerDefinition{Source: &reliantv1.TriggerDefinition_Webhook{Webhook: &reliantv1.WebhookSource{}}},
			kind: core.TriggerKindWebhook,
		},
		{
			name: "integration without events",
			wt:   &reliantv1.WorkflowTrigger{Source: &reliantv1.WorkflowTrigger_Integration{Integration: &reliantv1.IntegrationSource{Integration: "github"}}},
			def:  &reliantv1.TriggerDefinition{Source: &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{Integration: "github"}}},
			err:  "at least one event",
		},
		{
			name: "workflow event",
			wt:   &reliantv1.WorkflowTrigger{Source: &reliantv1.WorkflowTrigger_WorkflowEvent{WorkflowEvent: &reliantv1.WorkflowEventSource{Outcomes: []string{"failed"}}}},
			def:  &reliantv1.TriggerDefinition{Source: &reliantv1.TriggerDefinition_WorkflowEvent{WorkflowEvent: &reliantv1.WorkflowEventSource{Outcomes: []string{"failed"}}}},
			kind: core.TriggerKindWorkflowEvent,
		},
		{
			name: "no source",
			wt:   &reliantv1.WorkflowTrigger{},
			def:  &reliantv1.TriggerDefinition{},
			err:  "a source is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fromWT, wtErr := FromWorkflowTrigger(tc.wt)
			fromDef, defErr := FromDefinition(tc.def)
			if tc.err != "" {
				require.Error(t, wtErr)
				require.Error(t, defErr)
				assert.Contains(t, wtErr.Error(), tc.err)
				assert.Contains(t, defErr.Error(), tc.err)
				var cfgErr *ConfigError
				assert.ErrorAs(t, wtErr, &cfgErr)
				return
			}
			require.NoError(t, wtErr)
			require.NoError(t, defErr)
			assert.Equal(t, tc.kind, fromWT.Kind)
			assert.Equal(t, fromDef, fromWT)
		})
	}
}

func TestFromDefinitionRefusesAnUnresolvedWorkflowTrigger(t *testing.T) {
	_, err := FromDefinition(&reliantv1.TriggerDefinition{Source: &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: "nightly"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve it against the workflow")
}

func TestValidateScheduleAppliesDefaults(t *testing.T) {
	spec, err := ValidateSchedule(core.ScheduleConfig{Cron: []string{" 0 9 * * 1-5 "}})
	require.NoError(t, err)
	assert.Equal(t, []string{"0 9 * * 1-5"}, spec.Cron)
	assert.Equal(t, "UTC", spec.Location.String())
	assert.Equal(t, core.ScheduleOverlapSkip, spec.Overlap)
	assert.Equal(t, DefaultCatchupWindow, spec.CatchupWindow)
}

func TestCompileInputs(t *testing.T) {
	errs := CompileInputs(map[string]string{
		"literal": "main",
		"number":  "{{ trigger.payload.data.issue.number }}",
		"mixed":   "PR #{{ trigger.payload.data.number }} on {{ trigger.payload.data.repo }}",
		"bad":     "{{ trigger.payload.x == }}",
		"scope":   "{{ inputs.other }}",
	})
	require.Len(t, errs, 2)
	assert.Equal(t, "bad", errs[0].Input)
	assert.Equal(t, "scope", errs[1].Input, "only `trigger` exists when inputs are mapped")
	assert.Contains(t, errs[1].Reason, "inputs")
}

func TestLooksLikeBareExpression(t *testing.T) {
	assert.True(t, LooksLikeBareExpression("trigger.payload.data.issue.number"))
	assert.False(t, LooksLikeBareExpression("{{ trigger.payload.data.issue.number }}"))
	assert.False(t, LooksLikeBareExpression("main"))
	assert.False(t, LooksLikeBareExpression("triggered by a robot"))
}

func TestEvaluateInputs(t *testing.T) {
	root := map[string]any{
		"kind": "integration",
		"payload": map[string]any{
			"data": map[string]any{"issue": map[string]any{"number": int64(42), "title": "Crash"}},
		},
	}
	got, err := EvaluateInputs(map[string]string{
		"issue_number": "{{ trigger.payload.data.issue.number }}",
		"summary":      "#{{ trigger.payload.data.issue.number }}: {{ trigger.payload.data.issue.title }}",
		"branch":       "main",
		"source":       "{{ trigger.kind }}",
	}, root)
	require.NoError(t, err)
	assert.Equal(t, int64(42), got["issue_number"], "a pure expression keeps its native type")
	assert.Equal(t, "#42: Crash", got["summary"])
	assert.Equal(t, "main", got["branch"])
	assert.Equal(t, "integration", got["source"])

	_, err = EvaluateInputs(map[string]string{"missing": "{{ trigger.payload.data.pull_request.number }}"}, root)
	var inErr *InputsError
	require.ErrorAs(t, err, &inErr)
	assert.Equal(t, "missing", inErr.Input)
}

func TestFilterMatchRoot(t *testing.T) {
	f, err := CompileFilter("trigger.payload.action == 'opened'")
	require.NoError(t, err)
	hit, err := f.MatchRoot(map[string]any{"payload": map[string]any{"action": "opened"}})
	require.NoError(t, err)
	assert.True(t, hit)

	_, err = CompileFilter("{{ trigger.payload.action }}")
	var fe *FilterError
	require.ErrorAs(t, err, &fe)
}
