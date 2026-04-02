// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
)

// ============================================================================
// DATA FLOW TESTS
//
// These tests verify that data actually flows between workflow steps via edge bindings.
// This is the critical test for V3 workflow model correctness.
//
// Key insight: The agent workflow fails because CallLLM outputs don't properly
// flow to SaveMessage via edge bindings. These tests catch that exact bug.
// ============================================================================

// TestDataFlow_AgentWorkflow_CallLLMToSaveMessage tests the core agent loop data flow:
// CallLLM outputs (message, tool_calls, tokens) must flow to SaveMessage inputs.
//
// This is the exact data flow that fails in production with:
//
//	"undeclared reference to 'message'"
//
// If this test fails, edge bindings are broken.
func TestDataFlow_AgentWorkflow_CallLLMToSaveMessage(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure mock LLM to return a simple response (no tools)
	h.MockLLM.SetResponse("Hello, I'm the assistant!")

	// Create chat and run agent workflow using real gRPC service
	history := h.RunAgentWorkflowViaGRPCAndGetHistory(t, "Hello")

	// Debug: print all activities to see what actually happened
	history.PrintActivities()

	// Verify the workflow completed without failures
	history.AssertNoActivityFailures()

	// Verify CallLLM was executed and completed
	history.AssertActivitySucceeded("CallLLM")

	// Verify SaveMessage received the data from CallLLM via inline save_message
	// The initial user message is saved BEFORE the workflow starts (outside Temporal).
	// Only assistant/tool messages go through V2_SaveMessage activity.
	//
	// If this fails, inline save_message bindings are not working.
	saveMessageActivities := history.GetActivitiesOfType("SaveMessage")
	if len(saveMessageActivities) < 1 {
		t.Fatalf("expected at least 1 SaveMessage call (assistant), got %d", len(saveMessageActivities))
	}

	// The first SaveMessage should be the assistant message with data from CallLLM
	assistantSave := history.GetNthActivity("SaveMessage", 0)
	if assistantSave == nil {
		t.Fatal("SaveMessage (assistant) not found")
	}

	input := assistantSave.MustParseInput(t)

	// These are the critical assertions - data must have flowed from CallLLM
	// If these fail, the node reference like nodes.call_llm.content didn't work
	t.Logf("SaveMessage input: %+v", input)

	// SaveMessage inputs use ActivityInput structure: {node: {save_message_node: {...}}, runtime: {...}}
	// The proto oneof field name is "save_message_node" (snake_case with UseProtoNames).
	var role, content interface{}
	if nodeMap, ok := input["node"].(map[string]interface{}); ok {
		if argsMap, ok := nodeMap["save_message_node"].(map[string]interface{}); ok {
			role = argsMap["resolved_role"]
			content = argsMap["resolved_content"]
		}
	}

	// Check that role is "assistant" (bound from message.role)
	if role != "assistant" {
		t.Errorf("SaveMessage missing or incorrect role: got %v", role)
	}

	// Check that content is present (bound from message.text or response_text)
	switch content {
	case nil:
		t.Error("SaveMessage missing content field - edge binding failed!")
	case "":
		t.Error("SaveMessage content is empty - edge binding may have failed")
	default:
		t.Logf("SaveMessage content: %s", content)
	}
}

// TestDataFlow_LoopBinding tests that bindings work across loop iterations
func TestDataFlow_LoopBinding(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure LLM to return tool call first, then completion
	h.MockLLM.AddResponse(MockResponse{
		Text: "Let me run that for you",
		ToolCalls: []MockToolCall{{
			Name:  "Bash",
			Input: map[string]interface{}{"command": "echo test"},
		}},
	})
	h.MockLLM.AddResponse(MockResponse{
		Text: "Done! The command succeeded.",
	})

	// Mock tool execution
	h.MockTools.On("Bash", MockToolResponse{
		Result:  "test\n",
		Success: true,
	})

	// Create chat and run agent workflow using real gRPC service
	history := h.RunAgentWorkflowViaGRPCAndGetHistory(t, "Run echo test")

	history.PrintActivities()

	// Should have 2 CallLLM invocations (loop)
	history.AssertActivityCount("CallLLM", 2)

	// Both should succeed
	callLLMs := history.GetActivitiesOfType("CallLLM")
	for i, call := range callLLMs {
		if !call.Completed {
			t.Errorf("CallLLM %d did not complete", i)
		}
	}

	// ExecuteTools should have been called
	history.AssertActivityExecuted("ExecuteTools")

	// Verify no failures (binding errors would cause failures)
	history.AssertNoActivityFailures()
}

// ============================================================================
// HELPER METHODS
// ============================================================================

// WriteWorkflowFile writes a workflow YAML file to the harness temp dir AND
// registers it in the database so it can be loaded at runtime.
// The name should include the .yaml extension (e.g., "my_workflow.yaml").
// The workflow slug is derived from the name (e.g., "my_workflow").
func (h *TestHarness) WriteWorkflowFile(t *testing.T, name, content string) string {
	t.Helper()

	// Ensure .reliant/workflows directory exists
	workflowDir := h.TmpDir() + "/.reliant/workflows"
	err := os.MkdirAll(workflowDir, 0755)
	if err != nil {
		t.Fatalf("failed to create workflow directory: %v", err)
	}

	path := workflowDir + "/" + name
	err = os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write workflow file: %v", err)
	}

	// Also register the workflow in the database so it can be loaded.
	// The slug must be normalized the same way as generateWorkflowSlug in
	// load_workflow.go: lowercase, underscores/spaces → hyphens, strip non-alnum.
	slug := strings.TrimSuffix(name, ".yaml")
	slug = strings.TrimSuffix(slug, ".yml")
	slug = strings.ToLower(strings.TrimSpace(slug))
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")

	// Create a workflow draft in the database
	now := time.Now().UTC()
	draft := &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     h.userID,
		Name:       slug,
		Slug:       slug,
		Definition: content,
		IsValid:    true,  // Mark as valid so it can be used
		IsHidden:   false, // Mark as visible so it can be found
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	err = h.DB.CreateWorkflowDraft(context.Background(), draft)
	if err != nil {
		t.Fatalf("failed to register workflow in database: %v", err)
	}

	t.Logf("Registered test workflow: slug=%s, id=%s", slug, draft.ID)
	return path
}
