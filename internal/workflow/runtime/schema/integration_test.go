// Copyright (c) 2025 Reliant Labs
package schema_test

import (
	"testing"

	// Import activities to trigger init() which registers schema types
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func TestEnsureSourceDefaults_WithSaveMessage(t *testing.T) {
	// Verify schema is registered (init() ran)
	defaults := schema.GetInputDefaults("SaveMessage")
	if defaults == nil {
		t.Fatal("SaveMessage not registered in schema")
	}

	// Verify expected PUBLIC fields are present
	// Note: Token fields, context_sequence, and thread are intentionally hidden (internal)
	// Thread is auto-injected from execution context
	expectedFields := []string{
		"role",
		"content",
		"tool_calls",
		"tool_results",
	}

	for _, field := range expectedFields {
		if _, ok := defaults[field]; !ok {
			t.Errorf("Expected field %q not found in SaveMessageInput defaults", field)
		}
	}

	// Verify internal fields are NOT exposed
	internalFields := []string{
		"input_tokens",
		"output_tokens",
		"cache_creation_tokens",
		"cache_read_tokens",
		"context_sequence",
	}

	for _, field := range internalFields {
		if _, ok := defaults[field]; ok {
			t.Errorf("Internal field %q should NOT be exposed in schema", field)
		}
	}

	t.Logf("SaveMessageInput has %d public fields", len(defaults))
}

func TestChatIDHiddenFromSchema(t *testing.T) {
	// Verify chat_id is NOT exposed in schema output due to reliant:"-" tag
	// This field is internal-only, auto-injected by the runtime

	activities := []string{
		"SaveMessage",
		"CallLLM",
		"ExecuteTools",
		"Approval",
		"Compact",
	}

	for _, activity := range activities {
		t.Run(activity, func(t *testing.T) {
			defaults := schema.GetInputDefaults(activity)
			if defaults == nil {
				t.Skipf("%s not registered", activity)
				return
			}

			// chat_id should NOT be in the schema output
			if _, exists := defaults["chat_id"]; exists {
				t.Errorf("%s: chat_id should be hidden from schema (reliant:\"-\"), but was found", activity)
			}

			// Verify via GetInputFields as well
			fields := schema.GetInputFields(activity)
			for _, field := range fields {
				if field == "chat_id" {
					t.Errorf("%s: chat_id should be hidden from input fields (reliant:\"-\"), but was found", activity)
				}
			}
		})
	}
}

func TestEvaluateNodeConfig_WithExplicitNodesNamespace(t *testing.T) {
	t.Skip("TODO: CEL resolution integration test — needs workflow CEL resolver wiring")
	// Test that node evaluation uses explicit nodes.* namespace
	node := &reliantv1.Node{
		Id:   "test_call_llm",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "nodes.previous_node.message.text"}},
			},
		},
	}

	workflowInputs := map[string]interface{}{
		"chat_id": "test-chat",
	}

	nodeOutputs := map[string]interface{}{
		"previous_node": map[string]interface{}{
			"message": map[string]interface{}{
				"role": "user",
				"text": "hello",
			},
		},
	}

	result, err := v2.EvaluateNodeConfig(
		node,
		nodeOutputs,
		"test-workflow",
		"test",
		workflowInputs,
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("EvaluateNodeConfig failed: %v", err)
	}

	args, err := model.NodeArgsAsMap(result)
	if err != nil {
		t.Fatalf("ArgsAsMap failed: %v", err)
	}

	if prompt, ok := args["system_prompt"].(string); !ok {
		t.Error("system_prompt not in result")
	} else if prompt != "hello" {
		t.Errorf("Expected system_prompt to be 'hello', got %v", prompt)
	}
}
