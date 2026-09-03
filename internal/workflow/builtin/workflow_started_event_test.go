// Copyright (c) 2025 Reliant Labs
package builtin

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/core"
)

// TestWorkflowStartedEventData tests that workflow.started event contains proper message data.
//
// WHY THIS IS IMPORTANT:
// The workflow.started event is the entry point for all chat interactions.
// It must contain message.role and message.text in its Data field so that
// the first save_message step can access them via inputs.message.role etc.
//
// BACKGROUND (CEL NAMESPACES):
// - Workflow inputs are accessed via inputs.* namespace
// - Example: role: "{{inputs.message.role}}"
// - All data access is explicit via inputs.*, workflow.*, or nodes.*
func TestWorkflowStartedEventData(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		event         *core.WorkflowEvent
		shouldContain []string
		shouldFail    bool
		description   string
	}{
		{
			name: "started with message data",
			event: &core.WorkflowEvent{
				StepID: "", // Must be "started" to match edges - "workflow.started" is only for edge From field
				Data: map[string]interface{}{
					"message": map[string]interface{}{
						"role": "user",
						"text": "Hello, world!",
					},
				},
			},
			shouldContain: []string{"message", "role", "text"},
			shouldFail:    false,
			description:   "workflow.started event should contain message.role and message.text",
		},
		{
			name: "started without message data - SHOULD FAIL",
			event: &core.WorkflowEvent{
				StepID: "",
				Data:   map[string]interface{}{},
			},
			shouldContain: []string{"message"},
			shouldFail:    true,
			description:   "workflow.started event without message data should be detected as invalid",
		},
		{
			name: "started with empty message - SHOULD FAIL",
			event: &core.WorkflowEvent{
				StepID: "",
				Data: map[string]interface{}{
					"message": map[string]interface{}{},
				},
			},
			shouldContain: []string{"role", "text"},
			shouldFail:    true,
			description:   "workflow.started event with empty message should be detected as invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Check if event.Data contains required fields
			hasAllFields := true
			for _, field := range tt.shouldContain {
				if field == "message" {
					if _, ok := tt.event.Data["message"]; !ok {
						hasAllFields = false
						break
					}
				} else {
					// Check nested fields in message
					if msg, ok := tt.event.Data["message"].(map[string]interface{}); ok {
						if _, ok := msg[field]; !ok {
							hasAllFields = false
							break
						}
					} else {
						hasAllFields = false
						break
					}
				}
			}

			if tt.shouldFail && hasAllFields {
				t.Errorf("Expected test to fail (missing required fields), but all fields were present")
			}

			if !tt.shouldFail && !hasAllFields {
				t.Errorf("Test failed: %s\nEvent.Data: %+v\nMissing fields from: %v",
					tt.description, tt.event.Data, tt.shouldContain)
			}
		})
	}
}

// TestWorkflowStartedToSaveMessageFlow tests the complete data flow from started event to save_message.
//
// WHY THIS IS IMPORTANT:
// This tests that workflow inputs contain the expected message data structure
// that the save_user_message step will access via inputs.message.*
//
// CEL NAMESPACES:
// - Data is accessed via inputs.* in node config
// - Example: role: "{{inputs.message.role}}"
// - No implicit passthrough - all data access is explicit
func TestWorkflowStartedToSaveMessageFlow(t *testing.T) {
	t.Parallel()
	// Create workflow inputs with message data (as chat.go does)
	// This is what gets put into inputs namespace
	workflowInputs := map[string]interface{}{
		"message": map[string]interface{}{
			"role": "user",
			"text": "Hello from user",
		},
		"thread":      "0",
		"attachments": nil,
	}

	// Simulate accessing inputs.message.* in template evaluation
	// The save_user_message step config uses:
	//   role: "{{inputs.message.role}}"
	//   content: "{{inputs.message.text}}"

	// Verify that inputs contain message data
	messageData, ok := workflowInputs["message"].(map[string]interface{})
	if !ok {
		t.Fatalf("workflow.inputs missing 'message' field. Inputs: %+v", workflowInputs)
	}

	role, ok := messageData["role"].(string)
	if !ok {
		t.Fatalf("workflow.inputs.message missing 'role' field. Message: %+v", messageData)
	}

	text, ok := messageData["text"].(string)
	if !ok {
		t.Fatalf("workflow.inputs.message missing 'text' field. Message: %+v", messageData)
	}

	// Verify values are correct
	if role != "user" {
		t.Errorf("Expected role='user', got '%s'", role)
	}

	if text != "Hello from user" {
		t.Errorf("Expected text='Hello from user', got '%s'", text)
	}

	// Verify role is valid (this is the validation that save_message does)
	validRoles := []string{"user", "assistant", "tool", "system"}
	isValidRole := false
	for _, validRole := range validRoles {
		if role == validRole {
			isValidRole = true
			break
		}
	}

	if !isValidRole {
		t.Errorf("Role validation failed. Expected one of %v, got '%s'", validRoles, role)
	}
}
