// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCELEval(t *testing.T, p CELEvalParams) map[string]any {
	t.Helper()
	tool := NewCELEvalTool().(*ToolWrapper[CELEvalParams, ToolResponse])
	resp, err := tool.tool.Execute(&rctx.ToolContext{}, p)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &out), resp.Content)
	return out
}

func TestCELEvalTool_GeneratesTheTriggerAndReturnsValueAndType(t *testing.T) {
	out := runCELEval(t, CELEvalParams{
		Expression: "!('wontfix' in trigger.payload.data.issue.labels)", Scope: "trigger",
		Integration: "github", Event: "issues.opened",
	})
	assert.Equal(t, true, out["value"])
	assert.Equal(t, "bool", out["type"])
	assert.Nil(t, out["error"])
}

func TestCELEvalTool_AFailedEvaluationIsAResultNotAToolError(t *testing.T) {
	out := runCELEval(t, CELEvalParams{Expression: "trigger.payload.nope", Scope: "trigger", TriggerPayload: map[string]any{"a": 1}})
	errObj, ok := out["error"].(map[string]any)
	require.True(t, ok, "%v", out)
	assert.Equal(t, "eval", errObj["phase"])
	assert.Contains(t, errObj["message"], "no such key")
}

func TestCELEvalTool_IsAReadOnlyWorkflowTool(t *testing.T) {
	var found bool
	for _, def := range GetToolRegistry() {
		if def.Name == ToolCELEval {
			found = true
			assert.True(t, def.hasTag(TagWorkflow))
			assert.True(t, def.hasTag(TagReadOnly))
		}
	}
	assert.True(t, found, "cel_eval is registered")
}
