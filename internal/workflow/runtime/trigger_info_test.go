package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

func triggerTestInputs() map[string]interface{} {
	info := &TriggerInfo{
		Kind:       "schedule",
		TriggerID:  "trg-1",
		EventID:    "evt-1",
		OccurredAt: "2026-01-02T09:00:00Z",
		Payload:    map[string]any{"scheduled_for": "2026-01-02T09:00:00Z", "trigger_name": "nightly"},
	}
	return map[string]interface{}{wfcel.TriggerInputKey: info.CELValue()}
}

func TestTriggerNamespaceResolvesInNodeTemplates(t *testing.T) {
	scope := &wfcel.NodeResolutionContext{Inputs: triggerTestInputs(), Nodes: map[string]interface{}{}}

	for template, want := range map[string]string{
		"{{trigger.payload.trigger_name}}": "nightly",
		"{{trigger.name}}":                 "nightly",
		"{{trigger.kind}}":                 "schedule",
		"{{trigger.scheduled_for}}":        "2026-01-02T09:00:00Z",
		"{{trigger.trigger_id}}":           "trg-1",
	} {
		got, err := wfcel.EvaluateTemplate(template, scope)
		require.NoError(t, err, template)
		assert.Equal(t, want, got, template)
	}
}

func TestTriggerNamespaceDefaultsForInputlessScopes(t *testing.T) {
	// Even a scope with no trigger declares the namespace, so a template
	// referencing it fails on the missing key, not on an undeclared variable.
	scope := &wfcel.NodeResolutionContext{Inputs: map[string]interface{}{}, Nodes: map[string]interface{}{}}
	got, err := wfcel.EvaluateTemplate("{{has(trigger.kind)}}", scope)
	require.NoError(t, err)
	assert.Equal(t, false, got)
}

func TestPropagateTriggerReachesChildInputs(t *testing.T) {
	parent := triggerTestInputs()
	child := map[string]interface{}{}
	propagateTrigger(parent, child)
	assert.Equal(t, parent[wfcel.TriggerInputKey], child[wfcel.TriggerInputKey])
}
