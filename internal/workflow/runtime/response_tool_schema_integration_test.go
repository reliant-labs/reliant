// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func celStringLiteral(s string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
}

// TestResponseToolSchema_CELResolution_Integration exercises the full path:
//
//	YAML sentinel {"__cel_expr__": "{{inputs.response_schema}}"} stored in proto
//	→ ResolveCELFields evaluates the CEL expression inside the Struct
//	→ resolveResponseToolSchema unwraps the sentinel key
//	→ call_llm gets the actual schema with properties
//
// This is the exact flow that was broken: ResolveCELFields walks into
// google.protobuf.Struct and resolves the string value to a map, but keeps the
// "__cel_expr__" key. The resulting schema was {"__cel_expr__": {actual schema}}
// instead of just {actual schema}.
func TestResponseToolSchema_CELResolution_Integration(t *testing.T) {
	// This is the schema the user passes as a workflow input
	inputSchema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"verdict", "reason"},
		"properties": map[string]interface{}{
			"verdict": map[string]interface{}{
				"type":        "string",
				"enum":        []interface{}{"approve", "reject"},
				"description": "Your decision",
			},
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "Why you made this decision",
			},
		},
	}

	// Build the sentinel Struct that the YAML parser creates for CEL expressions.
	// This simulates: schema: "{{inputs.response_schema}}" in the YAML.
	sentinelStruct, err := structpb.NewStruct(map[string]interface{}{
		"__cel_expr__": "{{inputs.response_schema}}",
	})
	require.NoError(t, err)

	// Build a call_llm node with the sentinel schema
	node := &reliantv1.Node{
		Id:   "call_llm",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				ResponseTool: &reliantv1.ResponseTool{
					Name:        celStringLiteral("submit_verdict"),
					Description: celStringLiteral("Submit your verdict"),
					Schema:      sentinelStruct,
				},
			},
		},
	}

	// Run EvaluateNodeConfig — this is the exact function called at runtime.
	// It runs ResolveCELFields + resolveResponseToolSchema.
	resolved, err := EvaluateNodeConfig(
		node,
		nil,             // nodeOutputs
		"wf-1",          // workflowID
		"test-workflow", // workflowName
		map[string]interface{}{
			"response_schema": inputSchema,
		},
		nil, // iterContext
		nil, // loopOutputs
		nil, // execContext
	)
	require.NoError(t, err)

	// The resolved node should have the actual schema, NOT the sentinel.
	rt := resolved.GetCallLlm().GetResponseTool()
	require.NotNil(t, rt, "ResponseTool should not be nil after resolution")
	require.NotNil(t, rt.GetSchema(), "ResponseTool.Schema should not be nil after resolution")

	schema := rt.GetSchema().AsMap()

	// The sentinel key must NOT be present
	_, hasSentinel := schema["__cel_expr__"]
	assert.False(t, hasSentinel, "sentinel __cel_expr__ key should be removed after resolution, got: %v", schema)

	// The actual schema properties must be present
	assert.Equal(t, "object", schema["type"], "schema type should be 'object'")

	props, ok := schema["properties"].(map[string]interface{})
	require.True(t, ok, "schema should have 'properties' map, got: %T (%v)", schema["properties"], schema)
	assert.Contains(t, props, "verdict", "schema properties should contain 'verdict'")
	assert.Contains(t, props, "reason", "schema properties should contain 'reason'")

	required, ok := schema["required"].([]interface{})
	require.True(t, ok, "required should be a slice, got %T", schema["required"])
	assert.Contains(t, required, "verdict")
	assert.Contains(t, required, "reason")
}

// TestResponseToolSchema_InlineSchema_NotMutated verifies that a normal inline
// schema (not a CEL expression) passes through EvaluateNodeConfig unchanged.
func TestResponseToolSchema_InlineSchema_NotMutated(t *testing.T) {
	inlineSchema, err := structpb.NewStruct(map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"verdict"},
		"properties": map[string]interface{}{
			"verdict": map[string]interface{}{
				"type": "string",
			},
		},
	})
	require.NoError(t, err)

	node := &reliantv1.Node{
		Id:   "call_llm",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				ResponseTool: &reliantv1.ResponseTool{
					Name:        celStringLiteral("submit_verdict"),
					Description: celStringLiteral("Submit your verdict"),
					Schema:      inlineSchema,
				},
			},
		},
	}

	resolved, err := EvaluateNodeConfig(
		node,
		nil, "wf-1", "test-workflow",
		map[string]interface{}{}, // no inputs needed
		nil, nil, nil,
	)
	require.NoError(t, err)

	rt := resolved.GetCallLlm().GetResponseTool()
	require.NotNil(t, rt)
	require.NotNil(t, rt.GetSchema())

	schema := rt.GetSchema().AsMap()
	assert.Equal(t, "object", schema["type"])

	props, ok := schema["properties"].(map[string]interface{})
	require.True(t, ok, "inline schema properties should survive resolution")
	assert.Contains(t, props, "verdict")
}
