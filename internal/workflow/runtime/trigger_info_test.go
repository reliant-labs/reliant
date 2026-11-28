package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
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

func TestTriggerSenderResolvesInNodeTemplates(t *testing.T) {
	info := &TriggerInfo{
		Kind: "integration", TriggerID: "trg-1",
		Sender: &core.TriggerSender{Kind: core.TriggerSenderKindSlack, ID: "U123", DisplayName: "ada", Verified: true},
	}
	scope := &wfcel.NodeResolutionContext{Inputs: map[string]interface{}{wfcel.TriggerInputKey: info.CELValue()}, Nodes: map[string]interface{}{}}
	for template, want := range map[string]any{
		"{{trigger.sender.id}}":           "U123",
		"{{trigger.sender.kind}}":         "slack",
		"{{trigger.sender.display_name}}": "ada",
		"{{trigger.sender.verified}}":     true,
	} {
		got, err := wfcel.EvaluateTemplate(template, scope)
		require.NoError(t, err, template)
		assert.Equal(t, want, got, template)
	}
}

// A start with no sender (a chat) still has every sender key, so a template
// reading one renders blank and a filter requiring verified is false.
func TestTriggerSenderIsEmptyAndUnverifiedWithoutOne(t *testing.T) {
	info := &TriggerInfo{Kind: "chat.start"}
	assert.Equal(t, map[string]interface{}{"kind": "", "id": "", "display_name": "", "verified": false}, info.CELValue()["sender"])
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

// trigger.record names the same PR for a record_due firing and a GitHub pull
// request event, so one concurrency key serves both.
func TestTriggerRecordCELValue(t *testing.T) {
	due := (&TriggerInfo{Kind: "record_due", Payload: map[string]any{
		"table": "pr_pipeline", "record_id": "rec-1", "repository": "acme/app", "number": float64(7),
		"head_sha": "abc", "next_action": "decide", "due_at": "2026-11-26T12:00:00Z",
	}}).CELValue()["record"]
	assert.Equal(t, map[string]any{
		"id": "rec-1", "table": "pr_pipeline", "repository": "acme/app", "number": int64(7),
		"head_sha": "abc", "next_action": "decide", "due_at": "2026-11-26T12:00:00Z",
	}, due)

	pr := (&TriggerInfo{Kind: "integration", Payload: map[string]any{
		"attributes": map[string]string{"repository": "acme/app"},
		"data":       map[string]any{"pull_request": map[string]any{"number": int64(7), "head": map[string]any{"sha": "def"}}},
	}}).CELValue()["record"].(map[string]any)
	assert.Equal(t, "acme/app", pr["repository"])
	assert.Equal(t, int64(7), pr["number"])
	assert.Equal(t, "def", pr["head_sha"])
	assert.Equal(t, "", pr["id"])

	comment := (&TriggerInfo{Kind: "integration", Payload: map[string]any{
		"attributes": map[string]any{"repository": "acme/app", "is_pull_request": "true"},
		"data":       map[string]any{"issue": map[string]any{"number": float64(7)}},
	}}).CELValue()["record"].(map[string]any)
	assert.Equal(t, int64(7), comment["number"])

	issue := (&TriggerInfo{Kind: "integration", Payload: map[string]any{
		"attributes": map[string]any{"repository": "acme/app", "is_pull_request": "false"},
		"data":       map[string]any{"issue": map[string]any{"number": float64(9)}},
	}}).CELValue()["record"].(map[string]any)
	assert.Equal(t, int64(0), issue["number"], "an issue is not a PR")
	require.Equal(t, "", (&TriggerInfo{Kind: "schedule"}).CELValue()["record"].(map[string]any)["repository"])
}
