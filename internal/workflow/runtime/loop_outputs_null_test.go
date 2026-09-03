// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// TestLoopOutputsWithCELNull reproduces the pitch-deck workflow failure:
//
//	Inline workflow 'plan_deck' failed: sub-workflow builtin://structured-agent
//	failed: loop agent_loop failed: failed to build loop outputs struct:
//	proto: invalid type: structpb.NullValue
//
// Root cause: CEL represents null as types.NullValue, whose native Value() is
// the structpb.NullValue ENUM — not Go nil. The shared converter
// (wfcel.ConvertToNative) used to pass that enum through untouched, so any loop
// output expression that legitimately evaluated to null (e.g. the ternary
// `... ? nodes.execute_tools.response_data[...] : null` in structured-agent)
// poisoned the outputs map and made structpb.NewStruct fail at the END of the
// loop (loop_executor.go). These tests drive the exact same path:
// EvaluateWorkflowOutputs -> structpb.NewStruct.
func TestLoopOutputsWithCELNull(t *testing.T) {
	t.Parallel()
	workflowContext := buildWorkflowContext("test-wf-id", "test-wf", "test-chat",
		map[string]interface{}{"response_tool_name": "submit_response"})

	tests := []struct {
		name        string
		outputDefs  map[string]string
		nodeOutputs map[string]interface{}
		// keys expected to be null (Go nil) in the evaluated outputs
		nullKeys []string
	}{
		{
			name: "ternary yielding null (structured-agent response pattern)",
			outputDefs: map[string]string{
				// Mirrors builtin/structured-agent.yaml: LLM called a regular
				// tool this iteration, so response_data lacks the response
				// tool key and the ternary takes the null branch.
				"response":  `{{has(nodes.execute_tools) && has(nodes.execute_tools.response_data) && nodes.execute_tools.response_data != null && inputs.response_tool_name in nodes.execute_tools.response_data ? nodes.execute_tools.response_data[inputs.response_tool_name] : null}}`,
				"completed": `{{has(nodes.execute_tools) && has(nodes.execute_tools.response_data) && nodes.execute_tools.response_data != null && inputs.response_tool_name in nodes.execute_tools.response_data && nodes.execute_tools.response_data[inputs.response_tool_name] != null}}`,
			},
			nodeOutputs: map[string]interface{}{
				"execute_tools": map[string]interface{}{
					"response_data": map[string]interface{}{},
				},
			},
			nullKeys: []string{"response"},
		},
		{
			name: "has() guard ternary (structured-agent has_feedback pattern)",
			outputDefs: map[string]string{
				"has_feedback": `{{has(nodes.ask_question) && has(nodes.ask_question.has_feedback) ? nodes.ask_question.has_feedback : false}}`,
				"feedback":     `{{has(nodes.ask_question) && has(nodes.ask_question.feedback) ? nodes.ask_question.feedback : null}}`,
			},
			nodeOutputs: map[string]interface{}{}, // ask_question never ran
			nullKeys:    []string{"feedback"},
		},
		{
			name: "direct null literal",
			outputDefs: map[string]string{
				"value": `{{null}}`,
			},
			nodeOutputs: map[string]interface{}{},
			nullKeys:    []string{"value"},
		},
		{
			name: "null from node output value",
			outputDefs: map[string]string{
				"result": `{{nodes.step.result}}`,
			},
			nodeOutputs: map[string]interface{}{
				"step": map[string]interface{}{"result": nil},
			},
			nullKeys: []string{"result"},
		},
		{
			name: "null nested in map literal",
			outputDefs: map[string]string{
				"obj": `{{{"a": null, "b": "kept"}}}`,
			},
			nodeOutputs: map[string]interface{}{},
		},
		{
			name: "null nested in list literal",
			outputDefs: map[string]string{
				"list": `{{[null, 1, "x"]}}`,
			},
			nodeOutputs: map[string]interface{}{},
		},
		{
			name: "null nested in map inside list",
			outputDefs: map[string]string{
				"mixed": `{{[{"inner": null}, {"inner": 2}]}}`,
			},
			nodeOutputs: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputs, err := EvaluateWorkflowOutputs(tt.outputDefs, tt.nodeOutputs, workflowContext)
			require.NoError(t, err, "output evaluation should not fail")

			// No structpb.NullValue enum may survive anywhere in the tree.
			assertNoNullValueEnum(t, outputs, "outputs")

			for _, key := range tt.nullKeys {
				val, exists := outputs[key]
				assert.True(t, exists, "output %q should exist", key)
				assert.Nil(t, val, "output %q should be Go nil, got %T(%v)", key, val, val)
			}

			// The exact conversion the loop executor performs on iteration
			// outputs (loop_executor.go) — this is what used to fail with
			// "proto: invalid type: structpb.NullValue".
			protoOutputs, err := structpb.NewStruct(outputs)
			require.NoError(t, err, "loop outputs must convert to structpb.Struct")

			// Null outputs are legitimately representable as structpb NullValue.
			for _, key := range tt.nullKeys {
				field := protoOutputs.GetFields()[key]
				require.NotNil(t, field, "proto struct should contain %q", key)
				_, isNull := field.GetKind().(*structpb.Value_NullValue)
				assert.True(t, isNull, "proto field %q should be NullValue, got %v", key, field)
			}
		})
	}
}

// TestConvertToNativeNormalizesNullValueEnum documents the failure class the
// shared normalizer prevents: the structpb.NullValue enum is rejected by
// structpb.NewStruct/NewValue, so it must be normalized to Go nil.
func TestConvertToNativeNormalizesNullValueEnum(t *testing.T) {
	t.Parallel()
	t.Run("structpb rejects the raw NullValue enum (the old failure)", func(t *testing.T) {
		_, err := structpb.NewStruct(map[string]interface{}{
			"response": structpb.NullValue(0),
		})
		require.Error(t, err, "structpb.NewStruct must reject the NullValue enum")
		assert.Contains(t, err.Error(), "invalid type", "error should match the observed loop failure")
	})

	t.Run("normalizer converts NullValue enum to nil at all nesting levels", func(t *testing.T) {
		input := map[string]interface{}{
			"top": structpb.NullValue(0),
			"nested": map[string]interface{}{
				"inner": structpb.NullValue(0),
			},
			"list": []interface{}{structpb.NullValue(0), "kept", int64(1)},
		}

		normalized, ok := wfcel.ConvertToNative(input).(map[string]interface{})
		require.True(t, ok)

		assert.Nil(t, normalized["top"])
		nested, ok := normalized["nested"].(map[string]interface{})
		require.True(t, ok)
		assert.Nil(t, nested["inner"])
		list, ok := normalized["list"].([]interface{})
		require.True(t, ok)
		assert.Nil(t, list[0])
		assert.Equal(t, "kept", list[1])

		// And the normalized tree is structpb-compatible.
		s, err := structpb.NewStruct(normalized)
		require.NoError(t, err)
		_, isNull := s.GetFields()["top"].GetKind().(*structpb.Value_NullValue)
		assert.True(t, isNull, "nil normalizes to structpb NullValue — null outputs stay representable")
	})
}

// assertNoNullValueEnum walks a value tree and fails if any structpb.NullValue
// enum survived normalization.
func assertNoNullValueEnum(t *testing.T, v interface{}, path string) {
	t.Helper()
	switch val := v.(type) {
	case structpb.NullValue:
		t.Errorf("structpb.NullValue enum leaked at %s — must be Go nil", path)
	case map[string]interface{}:
		for k, item := range val {
			assertNoNullValueEnum(t, item, path+"."+k)
		}
	case []interface{}:
		for i, item := range val {
			assertNoNullValueEnum(t, item, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}
